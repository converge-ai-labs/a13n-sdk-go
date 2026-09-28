package a13n

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// Interaction is one submitted Agent invocation. Next observes provisional
// frames for its incorporating Run; Result reads the authoritative sealed Run.
// Close stops local observation only, never the remote Run. Always Close it.
// A completed Run may precede stream attachment; Result/Run.Items remain the
// authoritative readback, not a promise of complete retained stream history.
type Interaction struct {
	Receipt Result[generated.Submitted]
	Thread  Thread
	Entry   Entry
	Run     *Run // optional Run on the initial receipt; nil while queued

	ctx        context.Context
	cancel     context.CancelFunc
	streamCtx  context.Context
	stopStream context.CancelFunc
	done       chan struct{}
	bound      chan struct{}

	mu      sync.Mutex
	stream  *ThreadStream
	outcome RunOutcome
	err     error
	runID   string
	closed  bool
	reading sync.Mutex
}

func newInteraction(ctx context.Context, cancel context.CancelFunc, s *submission) *Interaction {
	streamCtx, stopStream := context.WithCancel(ctx)
	i := &Interaction{Receipt: s.Receipt, Thread: s.Thread, Entry: s.Entry, Run: s.Run,
		ctx: ctx, cancel: cancel, streamCtx: streamCtx, stopStream: stopStream, done: make(chan struct{}), bound: make(chan struct{})}
	go func() {
		result, err := s.wait(ctx, func(run Run) {
			i.mu.Lock()
			i.runID = run.ID
			i.mu.Unlock()
			close(i.bound)
		})
		i.mu.Lock()
		i.outcome, i.err = result, err
		stream := i.stream
		i.mu.Unlock()
		i.stopStream() // also cancel a still-in-flight SSE HTTP/header attach
		if stream != nil {
			_ = stream.Close() // unblock an idle SSE body after the Run seals
		}
		close(i.done) // observer has exited; result fields are stable
	}()
	return i
}

// Result works without calling Next; a shorter caller context only stops this
// caller's wait, leaving the interaction usable until Close or its own deadline.
func (i *Interaction) Result(ctx context.Context) (RunOutcome, error) {
	if i == nil {
		return RunOutcome{}, errors.New("interaction is required")
	}
	select {
	case <-i.done:
		i.mu.Lock()
		defer i.mu.Unlock()
		return i.outcome, i.err
	case <-ctx.Done():
		return RunOutcome{}, ctx.Err()
	}
}

// Next returns only frames belonging to the Run that consumed this Entry. It
// does not follow successor Runs. A terminal result or local Close yields EOF;
// observation errors are returned directly and Result gives the final outcome.
func (i *Interaction) Next() (ThreadFrame, error) {
	if i == nil {
		return nil, errors.New("interaction is required")
	}
	if !i.reading.TryLock() {
		return nil, errors.New("Interaction supports one active Next caller")
	}
	defer i.reading.Unlock()
	select {
	case <-i.bound:
	case <-i.done:
		return i.stopNext()
	}
	select { // if both signals are ready, never attach after completion
	case <-i.done:
		return i.stopNext()
	default:
	}
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil, io.EOF
	}
	stream := i.stream
	i.mu.Unlock()
	if stream == nil {
		// No Last-Event-ID replays retained frames. Attach only after durable
		// Entry consumption, so an unrelated Run cannot be mistaken for ours.
		opened, err := i.Thread.events(i.streamCtx, StreamOptions{MaxReconnects: 3})
		if err != nil {
			if i.streamCtx.Err() != nil {
				<-i.done
				return i.stopNext()
			}
			return nil, err
		}
		i.mu.Lock()
		if i.closed {
			i.mu.Unlock()
			_ = opened.Close()
			return nil, io.EOF
		}
		if i.stream == nil {
			i.stream = opened
			stream = opened
		} else {
			stream = i.stream
			_ = opened.Close()
		}
		i.mu.Unlock()
		select {
		case <-i.done:
			_ = stream.Close()
		default:
		}
	}
	for {
		frame, err := stream.Next()
		if err != nil {
			select {
			case <-i.done:
				return i.stopNext()
			default:
			}
			if i.streamCtx.Err() != nil {
				<-i.done
				return i.stopNext()
			}
			return nil, err
		}
		if i.ownFrame(frame) {
			return frame, nil
		}
	}
}

func (i *Interaction) ownFrame(frame ThreadFrame) bool {
	i.mu.Lock()
	id := i.runID
	i.mu.Unlock()
	switch f := frame.(type) {
	case BoundaryFrame:
		return f.RunID == id
	case DeltaFrame:
		return f.RunID == id
	case GapFrame:
		return f.RunID == id
	case ResetFrame:
		return f.RunID == id
	default: // Thread changed is not a Run-scoped execution frame.
		return false
	}
}

func (i *Interaction) stopNext() (ThreadFrame, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, io.EOF
	}
	if i.err != nil {
		return nil, i.err
	}
	return nil, io.EOF
}

func (i *Interaction) Close() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	i.closed = true
	stream := i.stream
	i.mu.Unlock()
	i.stopStream()
	i.cancel()
	if stream != nil {
		_ = stream.Close()
	}
	<-i.done // the one owned observer has exited before Close returns
	return nil
}
