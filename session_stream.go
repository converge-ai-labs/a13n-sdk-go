package a13n

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/converge-ai-labs/a13n-sdk-go/generated/sessionstream"
)

// SessionFrame is one native Session snapshot or live update. The generated
// sessionstream package owns its complete schema graph. Frames have no cursor.
type SessionFrame interface {
	EventType() string
	RawJSON() []byte
	sessionFrame()
}

type sessionFrameData struct{ raw []byte }

func (f sessionFrameData) RawJSON() []byte { return bytes.Clone(f.raw) }
func (sessionFrameData) sessionFrame()     {}

type SessionThreadFrame struct {
	sessionFrameData
	sessionstream.ThreadState
}
type SessionItemsFrame struct {
	sessionFrameData
	sessionstream.RunSnapshot
}
type SessionDeltaFrame struct {
	sessionFrameData
	sessionstream.Delta
}
type SessionGapFrame struct {
	sessionFrameData
	sessionstream.Gap
}

type SessionScheduleFrame struct {
	sessionFrameData
	sessionstream.ScheduleEvent
}
type SessionOccurrenceFrame struct {
	sessionFrameData
	sessionstream.OccurrenceEvent
}

type SessionAppReadyFrame struct {
	sessionFrameData
	sessionstream.AppReady
}

func (SessionAppReadyFrame) EventType() string { return "app_ready" }

func (SessionScheduleFrame) EventType() string   { return "schedule" }
func (SessionOccurrenceFrame) EventType() string { return "occurrence" }

func (SessionThreadFrame) EventType() string { return "thread" }
func (SessionItemsFrame) EventType() string  { return "items" }
func (SessionDeltaFrame) EventType() string  { return "delta" }
func (SessionGapFrame) EventType() string    { return "gap" }

type SessionStreamOptions struct {
	// MaxFrameBytes bounds a complete SSE frame, including ignored events and
	// comments. Zero imposes no SDK limit. A negative bound is invalid.
	MaxFrameBytes int
}

// SessionStream owns one connection with one Next reader. Close can run
// concurrently. EOF never reconnects; reopening SessionEvents starts a fresh
// snapshot of all Session threads, including child threads.
type SessionStream struct {
	client   *Client
	ctx      context.Context
	cancel   context.CancelFunc
	response *BinaryResult
	parser   *sseParser
	closed   atomic.Bool
	reading  sync.Mutex
}

// SessionEvents opens the native Session SSE operation. It sends neither
// Last-Event-ID nor progress query parameters and performs no automatic retry.
// Callers own reconnection, backoff and applying the native snapshots.
func (c *Client) SessionEvents(ctx context.Context, sessionID string, options SessionStreamOptions) (*SessionStream, error) {
	if err := validateClient(c, sessionID); err != nil {
		return nil, err
	}
	if options.MaxFrameBytes < 0 {
		return nil, fmt.Errorf("stream bound cannot be negative")
	}
	ctx, cancel := context.WithCancel(ctx)
	raw, err := c.api.SessionStream(ctx, sessionID,
		&generated.SessionStreamParams{XWorkspaceID: c.semanticWorkspace()})
	response, err := binaryResult(c, raw, err, http.StatusOK)
	if err != nil {
		cancel()
		return nil, err
	}
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if kind != "text/event-stream" {
		cancel()
		_ = response.Close()
		return nil, protocolError(raw, "content_type")
	}
	return &SessionStream{client: c, ctx: ctx, cancel: cancel, response: response,
		parser: &sseParser{reader: bufio.NewReader(response.Body), limit: options.MaxFrameBytes, first: true}}, nil
}

func (s *SessionStream) Response() (int, http.Header) {
	return s.response.StatusCode, s.response.Header.Clone()
}
func (s *SessionStream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.cancel()
	return s.response.Close()
}
func (s *SessionStream) observationError() error {
	if s.closed.Load() {
		return io.EOF
	}
	if s.client.lifetime.Err() != nil {
		return ErrClosed
	}
	return s.ctx.Err()
}
func (s *SessionStream) Next() (SessionFrame, error) {
	if !s.reading.TryLock() {
		return nil, fmt.Errorf("SessionStream supports one active reader")
	}
	defer s.reading.Unlock()
	for {
		if err := s.observationError(); err != nil {
			_ = s.Close()
			return nil, err
		}
		event, err := s.parser.nextEvent()
		if stopped := s.observationError(); stopped != nil {
			err = stopped
		}
		if err != nil {
			_ = s.Close()
			return nil, s.frameError(err)
		}
		frame, err := parseSessionFrame(event)
		if err != nil {
			_ = s.Close()
			return nil, s.frameError(err)
		}
		if frame != nil {
			return frame, nil
		}
	}
}

func (s *SessionStream) frameError(err error) error {
	if errors.Is(err, ErrProtocol) {
		return &ProtocolError{Kind: "stream_frame", StatusCode: s.response.StatusCode, RequestID: s.response.Header.Get("X-Request-ID")}
	}
	return err
}

func parseSessionFrame(event sseEvent) (SessionFrame, error) {
	// Unknown names are ignored before interpreting their payload or SSE ID.
	var target any
	meta := sessionFrameData{raw: event.data}
	switch event.event {
	case "thread":
		target = &SessionThreadFrame{sessionFrameData: meta}
	case "items":
		target = &SessionItemsFrame{sessionFrameData: meta}
	case "delta":
		target = &SessionDeltaFrame{sessionFrameData: meta}
	case "schedule":
		target = &SessionScheduleFrame{sessionFrameData: meta}
	case "occurrence":
		target = &SessionOccurrenceFrame{sessionFrameData: meta}
	case "app_ready":
		target = &SessionAppReadyFrame{sessionFrameData: meta}
	case "gap":
		target = &SessionGapFrame{sessionFrameData: meta}
	default:
		return nil, nil
	}
	invalid := func() (SessionFrame, error) { return nil, fmt.Errorf("%w: malformed Session frame", ErrProtocol) }
	if event.hasID {
		return invalid()
	}
	if err := validateSessionFrame(event.event, event.data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(event.data))
	decoder.UseNumber()
	if decoder.Decode(target) != nil {
		return invalid()
	}

	return target.(SessionFrame), nil
}
