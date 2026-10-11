package a13n

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// Receipt contains safe-to-persist identity evidence, never a completion promise.
// Origin is submitted_entry, recovered_entry, resumed_run or observed_run.
type Receipt struct {
	Origin           string `json:"origin"`
	ThreadID         string `json:"thread_id,omitempty"`
	EntryID          string `json:"entry_id,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	ProvisionalRunID string `json:"provisional_run_id,omitempty"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
}

// ReceiptEvidence preserves the optional original HTTP response for each origin.
// Both fields are nil when observation is recovered from identities.
type ReceiptEvidence struct {
	Submission *Result[generated.Submitted]
	Resume     *Result[generated.RunView]
}

// Interaction owns one lazy observer and optional finite reader. Always Close it.
// Cancellation is local only; final output comes from Result, never a transcript.
type Interaction struct {
	ctx         context.Context
	cancel      context.CancelFunc
	streamCtx   context.Context
	stopStream  context.CancelFunc
	done        chan struct{}
	bound       chan struct{}
	startOnce   sync.Once
	observe     func(context.Context, func(Run, string)) (RunOutcome, error)
	mu          sync.Mutex
	thread      Thread
	entry       *Entry
	receipt     Receipt
	evidence    ReceiptEvidence
	stream      *ThreadStream
	outcome     RunOutcome
	err         error
	runID       string
	closed      bool
	reading     sync.Mutex
	rawUsed     bool
	previewUsed bool
	previewErr  error
}

func baseInteraction(ctx context.Context, cancel context.CancelFunc) *Interaction {
	streamCtx, stopStream := context.WithCancel(ctx)
	return &Interaction{ctx: ctx, cancel: cancel, streamCtx: streamCtx, stopStream: stopStream, done: make(chan struct{}), bound: make(chan struct{})}
}
func newInteraction(ctx context.Context, cancel context.CancelFunc, s *submission) *Interaction {
	i := baseInteraction(ctx, cancel)
	entry := s.Entry
	i.thread = s.Thread
	i.entry = &entry
	i.receipt = Receipt{Origin: "recovered_entry", ThreadID: s.Thread.ID, EntryID: s.Entry.ID, IdempotencyKey: s.Key}
	if s.HasReceipt {
		i.receipt.Origin = "submitted_entry"
		receipt := s.Receipt
		i.evidence.Submission = &receipt
	}
	if s.Run != nil {
		i.receipt.ProvisionalRunID = s.Run.ID
	}
	i.observe = s.wait
	return i
}
func newRunInteraction(ctx context.Context, cancel context.CancelFunc, r Run, threadID string, initial *Result[generated.RunView], key, origin string) *Interaction {
	i := baseInteraction(ctx, cancel)
	i.thread = r.client.Thread(threadID)
	i.runID = r.ID
	i.receipt = Receipt{Origin: origin, ThreadID: threadID, RunID: r.ID, IdempotencyKey: key}
	if initial != nil {
		receipt := *initial
		i.evidence.Resume = &receipt
	}
	i.observe = func(ctx context.Context, bind func(Run, string)) (RunOutcome, error) {
		if initial != nil && sealed(initial.Value) {
			if err := ctx.Err(); err != nil {
				return RunOutcome{}, err
			}
			bind(r, threadID)
			return outcome(r, *initial), nil
		}
		return r.wait(ctx, threadID, bind)
	}
	return i
}
func (i *Interaction) start() {
	i.startOnce.Do(func() {
		go func() {
			result, err := i.observe(i.ctx, func(run Run, threadID string) {
				i.mu.Lock()
				i.runID = run.ID
				i.thread = run.client.Thread(threadID)
				i.receipt.ThreadID = threadID
				i.mu.Unlock()
				close(i.bound)
			})
			i.mu.Lock()
			i.outcome, i.err = result, err
			stream := i.stream
			i.mu.Unlock()
			i.stopStream()
			if stream != nil {
				_ = stream.Close()
			}
			close(i.done)
		}()
	})
}
func (i *Interaction) Receipt() Receipt { i.mu.Lock(); defer i.mu.Unlock(); return i.receipt }
func (i *Interaction) ReceiptEvidence() ReceiptEvidence {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.evidence
}
func (i *Interaction) Thread() Thread { i.mu.Lock(); defer i.mu.Unlock(); return i.thread }

// Entry is absent for resume and exact Run observation: no Entry is fabricated.
func (i *Interaction) Entry() *Entry {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.entry == nil {
		return nil
	}
	copy := *i.entry
	return &copy
}

// Run is authoritative only after consumption, or for an exact Run origin.
func (i *Interaction) Run() (*Run, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runID == "" {
		return nil, false
	}
	r := i.thread.client.Run(i.runID)
	return &r, true
}

// Result works without calling Next; a shorter caller context only stops this
// caller's wait, leaving the interaction usable until Close or its own deadline.
func (i *Interaction) Result(ctx context.Context) (RunOutcome, error) {
	if i == nil {
		return RunOutcome{}, errors.New("interaction is required")
	}
	i.start()
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
	i.rawUsed = true
	return i.next()
}

// next requires the single reader lock, shared by Next and Text.
func (i *Interaction) next() (ThreadFrame, error) {
	i.start()
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
		opened, err := i.Thread().events(i.streamCtx, StreamOptions{MaxReconnects: 3})
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
	i.startOnce.Do(func() {
		i.mu.Lock()
		i.err = context.Canceled
		i.mu.Unlock()
		close(i.done)
	})
	<-i.done // the one owned observer has exited before Close returns
	return nil
}
