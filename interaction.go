package a13n

import (
	"context"
	"fmt"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"time"
)

// Submitted binds references from the server receipt, not from the requested
// workspace key. Run is nil for a queued entry; acceptance is not completion.
type Submitted struct {
	Receipt Result[generated.Submitted]
	Thread  ThreadResource
	Entry   EntryResource
	Run     *RunResource
}

func bindSubmitted(c *Client, receipt Result[generated.Submitted]) (*Submitted, error) {
	value := receipt.Value
	if value.Thread.WorkspaceId == "" || value.Thread.Id == "" || value.Entry.Id == "" || !value.Run.IsSpecified() {
		return nil, fmt.Errorf("%w: invalid submission identity", ErrProtocol)
	}
	workspace := c.Resources().Workspaces().Ref(value.Thread.WorkspaceId)
	thread := workspace.Threads().Ref(value.Thread.Id)
	result := &Submitted{Receipt: receipt, Thread: thread, Entry: thread.Inbox().Ref(value.Entry.Id)}
	if run, err := value.Run.Get(); err == nil {
		if run.Id == "" {
			return nil, fmt.Errorf("%w: invalid submitted Run identity", ErrProtocol)
		}
		ref := workspace.Runs().Ref(run.Id)
		result.Run = &ref
	}
	return result, nil
}

// TextPayload is a convenience for the canonical full MessagePayload request.
func TextPayload(text string) generated.MessagePayload {
	var part generated.Part
	// TextPart contains only strings; encoding cannot fail.
	_ = part.FromTextPart(generated.TextPart{Text: text, Type: "text"})
	return generated.MessagePayload{Content: []generated.Part{part}}
}

// Wait observes exactly this Run until completed, waiting, failed or cancelled.
// The context bounds requests, body delivery and sleeps; it never stops the Run.
// Resume returns a successor: bind its returned ID before waiting. This method
// neither follows successors nor interprets waiting/failed/cancelled as success.
func (r RunResource) Wait(ctx context.Context, interval time.Duration) (Result[generated.RunView], error) {
	return waitFor(ctx, r.client, interval, r.Get, func(run generated.RunView) bool {
		switch run.Status {
		case generated.RunStatusCompleted, generated.RunStatusWaiting, generated.RunStatusFailed, generated.RunStatusCancelled:
			return true
		default:
			return false
		}
	})
}

// Wait observes the entry until consumed, failed or withdrawn. Assigned is not
// consumed and an assigned_run_id is not necessarily a new child Run.
func (r EntryResource) Wait(ctx context.Context, interval time.Duration) (Result[generated.EntryView], error) {
	return waitFor(ctx, r.client, interval, r.Get, func(entry generated.EntryView) bool {
		return entry.Status == generated.EntryStatusConsumed || entry.Status == generated.EntryStatusFailed || entry.Status == generated.EntryStatusWithdrawn
	})
}

func waitFor[T any](ctx context.Context, client *Client, interval time.Duration, get func(context.Context) (Result[T], error), done func(T) bool) (Result[T], error) {
	var zero Result[T]
	if client == nil {
		return zero, fmt.Errorf("resource is not bound to a client")
	}
	if interval < 0 {
		return zero, fmt.Errorf("poll interval cannot be negative")
	}
	if interval == 0 {
		interval = 250 * time.Millisecond
	}
	for {
		result, err := get(ctx)
		if err != nil || done(result.Value) {
			return result, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-client.lifetime.Done():
			timer.Stop()
			return zero, ErrClosed
		case <-timer.C:
		}
	}
}
