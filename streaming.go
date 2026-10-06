package a13n

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

var streamCursor = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,20}$`)
var streamPosition = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})-(0|[1-9][0-9]{0,19})$`)

// ThreadFrame is a closed set of five provisional stream variants. Applications
// explicitly reconcile gap/reset with Run Items and changed with Thread Get.
type ThreadFrame interface {
	EventType() string
	Cursor() string
	threadFrame()
}
type frameMeta struct{ event, cursor string }

func (f frameMeta) EventType() string { return f.event }
func (f frameMeta) Cursor() string    { return f.cursor }
func (frameMeta) threadFrame()        {}

type ItemRef struct {
	ID            string                                        `json:"id"`
	Kind          string                                        `json:"kind"`
	State         string                                        `json:"state"`
	Ordinal       nullable.Nullable[int]                        `json:"ordinal,omitempty"`
	ResponseGroup nullable.Nullable[string]                     `json:"response_group,omitempty"`
	Failure       nullable.Nullable[map[string]json.RawMessage] `json:"failure,omitempty"`
}
type BoundaryFrame struct {
	frameMeta
	RunID    string `json:"run_id"`
	Attempt  int    `json:"attempt"`
	Sequence int    `json:"sequence"`
}
type DeltaFrame struct {
	BoundaryFrame
	Event map[string]json.RawMessage `json:"event"`
	Item  *ItemRef                   `json:"item"`
}
type ChangedFrame struct {
	frameMeta
	Version int `json:"version"`
}
type ResetFrame struct {
	frameMeta
	RunID string `json:"run_id"`
}
type GapFrame struct {
	frameMeta
	RunID    string  `json:"run_id"`
	Position *string `json:"position,omitempty"` // nil means the missing range is unknown
}

type StreamOptions struct {
	After string
	// RunID and Position describe applied display coverage; they are paired.
	// After is only a retained Redis hint when coverage is supplied.
	RunID    string
	Position string
	// Zero disables reconnection. Progress resets this bounded failure budget.
	MaxReconnects  int
	MaxFrameBytes  int
	ReconnectDelay time.Duration
}

// ThreadStream has one Next reader; Close may run concurrently. A subsequent
// Next acknowledges the previously returned data frame. Closing does not ack it.
type ThreadStream struct {
	thread   Thread
	ctx      context.Context
	cancel   context.CancelFunc
	options  StreamOptions
	closed   atomic.Bool
	reading  sync.Mutex
	mu       sync.Mutex
	response *BinaryResult
	parser   *sseParser
	applied  string
	received string
	pending  ThreadFrame
	position string
	frozen   bool
	retries  int
}

// events is the internal typed parser used by finite interactions. The
// generated API retains direct access to the persistent Thread-wide SSE route.
func (r Thread) events(ctx context.Context, options StreamOptions) (*ThreadStream, error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return nil, err
	}
	if (options.RunID == "") != (options.Position == "") || (options.Position != "" && !streamPosition.MatchString(options.Position)) {
		return nil, fmt.Errorf("stream RunID and canonical Position must be supplied together")
	}
	if options.After != "" && !streamCursor.MatchString(options.After) {
		return nil, fmt.Errorf("invalid stream cursor")
	}
	if options.MaxReconnects < 0 || options.MaxFrameBytes < 0 || options.ReconnectDelay < 0 {
		return nil, fmt.Errorf("stream bounds cannot be negative")
	}
	if options.MaxFrameBytes == 0 {
		options.MaxFrameBytes = 1 << 20
	}
	if options.ReconnectDelay == 0 {
		options.ReconnectDelay = 250 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	stream := &ThreadStream{thread: r, ctx: ctx, cancel: cancel, options: options, applied: options.After, position: options.Position}
	if err := stream.attachWithRecovery(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

func (s *ThreadStream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.cancel()
	return s.detach()
}
func (s *ThreadStream) detach() error {
	s.mu.Lock()
	response := s.response
	s.response = nil
	s.mu.Unlock()
	if response != nil {
		return response.Close()
	}
	return nil
}
func (s *ThreadStream) AppliedCursor() string   { s.mu.Lock(); defer s.mu.Unlock(); return s.applied }
func (s *ThreadStream) AppliedPosition() string { s.mu.Lock(); defer s.mu.Unlock(); return s.position }
func (s *ThreadStream) LastReceivedCursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received
}
func (s *ThreadStream) Response() (int, http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.response == nil {
		return 0, nil
	}
	return s.response.StatusCode, s.response.Header.Clone()
}
func (s *ThreadStream) Next() (ThreadFrame, error) {
	if !s.reading.TryLock() {
		return nil, fmt.Errorf("ThreadStream supports one active reader")
	}
	defer s.reading.Unlock()
	if err := s.observationError(); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.mu.Lock()
	if s.pending != nil {
		s.acknowledge(s.pending)
		s.pending = nil
	}
	s.mu.Unlock()
	for {
		frame, err := s.parser.next()
		if stopped := s.observationError(); stopped != nil {
			_ = s.Close()
			return nil, stopped
		}
		if err == nil {
			s.mu.Lock()
			if frame.Cursor() != "" {
				s.received = frame.Cursor()
			}
			s.pending = frame
			s.mu.Unlock()
			return frame, nil
		}
		_ = s.detach()
		if s.closed.Load() {
			return nil, io.EOF
		}
		if err == io.EOF && s.options.MaxReconnects == 0 {
			_ = s.Close()
			return nil, io.EOF
		}
		if err == io.EOF {
			err = ErrTransport
		}
		if err = s.recover(err); err == nil {
			err = s.attachWithRecovery()
		}
		if err != nil {
			if s.closed.Load() {
				return nil, io.EOF
			}
			_ = s.Close()
			return nil, err
		}
	}
}

// acknowledge runs under mu only after the caller advances Next. A Redis ID is
// not display coverage: scoped reconnects retain only a contiguous applied tail.
func (s *ThreadStream) acknowledge(frame ThreadFrame) {
	if s.options.RunID == "" {
		if cursor := frame.Cursor(); cursor != "" && cursor != s.applied {
			s.applied, s.retries = cursor, 0
		}
		return
	}
	if s.frozen {
		return
	}
	var boundary BoundaryFrame
	isDelta := false
	switch f := frame.(type) {
	case GapFrame:
		if f.RunID == s.options.RunID {
			s.frozen = true
		}
		return
	case ResetFrame:
		if f.RunID == s.options.RunID {
			s.frozen = true
		}
		return
	case DeltaFrame:
		boundary, isDelta = f.BoundaryFrame, true
	case BoundaryFrame:
		boundary = f
	default: // A gap is missing output, not acknowledgement of it.
		return
	}
	if boundary.RunID != s.options.RunID || boundary.Attempt < 0 || boundary.Sequence < 0 {
		return
	}
	attempt, sequence := strconv.Itoa(boundary.Attempt), strconv.Itoa(boundary.Sequence)
	coveredAttempt, coveredSequence, _ := strings.Cut(s.position, "-")
	switch compareComponent(attempt, coveredAttempt) {
	case -1:
		return
	case 1:
		// A new attempt invalidates the caller's applied display baseline.
		// Only applying a fresh snapshot and explicitly reopening restores it.
		s.frozen = true
		return
	}
	if (isDelta && boundary.Sequence > 0 && strconv.Itoa(boundary.Sequence-1) == coveredSequence) || (!isDelta && sequence == coveredSequence) {
		s.position = attempt + "-" + sequence
		if s.applied != frame.Cursor() {
			s.applied, s.retries = frame.Cursor(), 0
		}
	} else if compareComponent(sequence, coveredSequence) > 0 {
		s.frozen = true // A later delta or boundary cannot bridge unseen output.
	}
}

// Components remain decimal strings because the wire permits 20 digits.
func compareComponent(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// Check outside transport reads too: the parser may already hold complete frames.
func (s *ThreadStream) observationError() error {
	if s.closed.Load() {
		return io.EOF
	}
	if s.thread.client.lifetime.Err() != nil {
		return ErrClosed
	}
	return s.ctx.Err()
}
func (s *ThreadStream) attachWithRecovery() error {
	for {
		err := s.attach()
		if err == nil {
			return nil
		}
		if err = s.recover(err); err != nil {
			return err
		}
	}
}
func (s *ThreadStream) attach() error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	options := generated.ThreadStreamApiV1ThreadsThreadIdStreamGetParams{XWorkspaceID: s.thread.client.semanticWorkspace()}
	s.mu.Lock()
	cursor, position := s.applied, s.position
	s.mu.Unlock()
	if s.options.RunID != "" {
		options.Run, options.Position = &s.options.RunID, &position
	}
	if cursor != "" {
		options.LastEventID = &cursor
	}
	raw, err := s.thread.client.api.ThreadStreamApiV1ThreadsThreadIdStreamGet(s.ctx, s.thread.ID, &options)
	response, err := binaryResult(s.thread.client, raw, err, 200)
	if err != nil {
		return err
	}
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if kind != "text/event-stream" {
		_ = response.Close()
		return fmt.Errorf("%w: expected text/event-stream", ErrProtocol)
	}
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		_ = response.Close()
		return io.EOF
	}
	s.response = response
	s.parser = &sseParser{reader: bufio.NewReader(response.Body), limit: s.options.MaxFrameBytes, first: true}
	s.mu.Unlock()
	return nil
}
func (s *ThreadStream) recover(err error) error {
	if s.thread.client.lifetime.Err() != nil {
		return ErrClosed
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	retry := errors.Is(err, ErrTransport)
	var api *ApiError
	if errors.As(err, &api) {
		retry = api.Status == 429 || api.Status == 502 || api.Status == 503 || api.Status == 504
	}
	if !retry || s.retries >= s.options.MaxReconnects {
		return err
	}
	s.retries++
	delay := s.options.ReconnectDelay
	if api != nil {
		if seconds, parseErr := strconv.Atoi(api.RetryAfter); parseErr == nil && seconds >= 0 && seconds <= 86400 {
			delay = max(delay, time.Duration(seconds)*time.Second)
		} else if date, parseErr := http.ParseTime(api.RetryAfter); parseErr == nil {
			delay = max(delay, time.Until(date))
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.thread.client.lifetime.Done():
		return ErrClosed
	case <-timer.C:
		return nil
	}
}

type sseParser struct {
	reader *bufio.Reader
	limit  int
	skipLF bool
	first  bool
}

func (p *sseParser) line() (string, error) {
	raw := make([]byte, 0, 128)
	for {
		b, err := p.reader.ReadByte()
		if err != nil {
			if err == io.EOF && len(raw) > 0 {
				return "", fmt.Errorf("%w: truncated SSE line", ErrProtocol)
			}
			return "", err
		}
		if p.skipLF {
			p.skipLF = false
			if b == '\n' {
				continue
			}
		}
		if b == '\r' || b == '\n' {
			p.skipLF = b == '\r'
			if !utf8.Valid(raw) {
				return "", fmt.Errorf("%w: invalid SSE UTF-8", ErrProtocol)
			}
			return string(raw), nil
		}
		raw = append(raw, b)
		if len(raw) > p.limit {
			return "", fmt.Errorf("%w: oversized SSE line", ErrProtocol)
		}
	}
}
func (p *sseParser) next() (ThreadFrame, error) {
	var data []string
	event, cursor := "", ""
	hasID, size := false, 0
	for {
		line, err := p.line()
		if err != nil {
			if err == io.EOF && (len(data) > 0 || event != "" || hasID) {
				return nil, fmt.Errorf("%w: truncated SSE frame", ErrProtocol)
			}
			return nil, err
		}
		if p.first {
			line = strings.TrimPrefix(line, "\ufeff")
			p.first = false
		}
		size += len(line) + 1
		if size > p.limit {
			return nil, fmt.Errorf("%w: oversized SSE frame", ErrProtocol)
		}
		if line == "" {
			if len(data) > 0 {
				return parseThreadFrame(event, cursor, hasID, []byte(strings.Join(data, "\n")))
			}
			event, cursor, hasID, size = "", "", false, 0
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "id":
			cursor, hasID = value, true
		case "data":
			data = append(data, value)
		}
	}
}
func parseThreadFrame(event, cursor string, hasID bool, data []byte) (ThreadFrame, error) {
	invalid := func() (ThreadFrame, error) { return nil, fmt.Errorf("%w: malformed Thread frame", ErrProtocol) }
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return invalid()
	}
	required := func(names ...string) bool {
		for _, name := range names {
			raw, exists := fields[name]
			if !exists || string(raw) == "null" {
				return false
			}
		}
		return true
	}
	meta := frameMeta{event: event, cursor: cursor}
	switch event {
	case "delta", "boundary":
		if !hasID || !streamCursor.MatchString(cursor) || !required("run_id", "attempt", "sequence") {
			return invalid()
		}
		var boundary BoundaryFrame
		if json.Unmarshal(data, &boundary) != nil || boundary.RunID == "" {
			return invalid()
		}
		boundary.frameMeta = meta
		if event == "boundary" {
			return boundary, nil
		}
		var delta DeltaFrame
		if _, exists := fields["item"]; !exists || !required("event") {
			return invalid()
		}
		if json.Unmarshal(data, &delta) != nil || delta.Event == nil {
			return invalid()
		}
		if item := delta.Item; item != nil {
			if item.ID == "" || !contains([]string{"text_message", "reasoning_message", "tool_call", "observation"}, item.Kind) || !contains([]string{"in_progress", "completed", "interrupted", "failed"}, item.State) {
				return invalid()
			}
		}
		delta.frameMeta = meta
		return delta, nil
	case "changed":
		if hasID || !required("version") {
			return invalid()
		}
		frame := ChangedFrame{frameMeta: meta}
		if json.Unmarshal(data, &frame) != nil {
			return invalid()
		}
		return frame, nil
	case "gap", "reset":
		if hasID || !required("run_id") {
			return invalid()
		}
		var payload struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal(data, &payload) != nil || payload.RunID == "" {
			return invalid()
		}
		if event == "gap" {
			var frame GapFrame
			if json.Unmarshal(data, &frame) != nil || (frame.Position != nil && !streamPosition.MatchString(*frame.Position)) {
				return invalid()
			}
			frame.frameMeta = meta
			return frame, nil
		}
		return ResetFrame{meta, payload.RunID}, nil
	default:
		return invalid()
	}
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
