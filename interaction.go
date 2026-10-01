package a13n

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

// Agent is a local Agent-ID binding, not a claim about Service authorization.
type Agent struct {
	client *Client
	ID     string
}

// Thread has no implicit Agent; continuation always names an Agent.
type Thread struct {
	client *Client
	ID     string
}

type Entry struct {
	client   *Client
	ThreadID string
	ID       string
}

type Run struct {
	client *Client
	ID     string
}

// StartOptions exposes all user-selectable NewThread fields without duplicating
// AgentId or Payload. Nullable fields preserve omission versus explicit null.
type StartOptions struct {
	RequestKey      string
	AgentRevisionId nullable.Nullable[string]
	Delivery        *generated.Delivery
	Environments    *generated.InitialMounts
	Kind            *generated.NewThreadKind
	McpHeaders      *generated.McpHeaders
	Memories        *[]generated.MemoryMount
	MessageHistory  *generated.MessageHistory
	// Options includes the complete typed Configuration snapshot, independent
	// of Agent Overrides. Its nullable fields are forwarded without merging.
	Options   *generated.RunOptionsInput
	SessionId nullable.Nullable[string]
}

// SendOptions exposes all user-selectable Message fields. The Agent and payload
// come from the binding and Send/SendPayload arguments.
type SendOptions struct {
	RequestKey      string
	AgentRevisionId nullable.Nullable[string]
	Delivery        *generated.Delivery
	Kind            *generated.MessageKind
	// Options.Configuration is forwarded as authored; the Service decides
	// whether it matches an active Run or selects a new next_run snapshot.
	Options *generated.RunOptionsInput
}

// submission is acceptance, not completion. Run is nil for queued input.
type submission struct {
	Receipt Result[generated.Submitted]
	Thread  Thread
	Entry   Entry
	Run     *Run
}

// RunOutcome projects one authoritative snapshot, not mutable server state.
// Waiting/failed/cancelled status is not a success promise.
type RunOutcome struct {
	Run      Run
	Snapshot Result[generated.RunView]
}

func (o RunOutcome) Status() generated.RunStatus                    { return o.Snapshot.Value.Status }
func (o RunOutcome) Output() nullable.Nullable[generated.JsonValue] { return o.Snapshot.Value.Output }
func (o RunOutcome) Pending() nullable.Nullable[generated.Pending]  { return o.Snapshot.Value.Pending }
func (o RunOutcome) Failure() nullable.Nullable[generated.Failure]  { return o.Snapshot.Value.Failure }

// EntryDispositionError retains the failed or withdrawn Entry and request
// evidence without echoing its arbitrary payload in an error string.
type EntryDispositionError struct {
	Entry    Entry
	Snapshot Result[generated.EntryView]
}

func (e *EntryDispositionError) Error() string {
	return fmt.Sprintf("submission entry %s is %s", e.Entry.ID, e.Snapshot.Value.Status)
}

func validateID(id string) error {
	if id == "" || id == "." || id == ".." {
		return errors.New("resource ID is required and cannot be a dot segment")
	}
	return nil
}

func validateClient(c *Client, ids ...string) error {
	if c == nil {
		return errors.New("resource is not bound to a client")
	}
	for _, id := range ids {
		if err := validateID(id); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) semanticWorkspace() *string {
	if c.workspaceID == "" {
		return nil
	}
	return &c.workspaceID
}

// TextPayload builds the typed Service message payload for a text greeting.
func TextPayload(text string) generated.MessagePayload {
	var part generated.Part
	_ = part.FromTextPart(generated.TextPart{Text: text, Type: "text"})
	return generated.MessagePayload{Content: []generated.Part{part}}
}

// Start submits text and returns one finite, locally cancellable interaction.
func (a Agent) Start(ctx context.Context, text string, options StartOptions) (*Interaction, error) {
	return a.StartPayload(ctx, TextPayload(text), options)
}

// StartPayload accepts the full typed Service MessagePayload. All other
// NewThread fields remain available through StartOptions.
func (a Agent) StartPayload(ctx context.Context, payload generated.MessagePayload, options StartOptions) (*Interaction, error) {
	if err := validateClient(a.client, a.ID); err != nil {
		return nil, err
	}
	if options.RequestKey == "" {
		return nil, errors.New("Idempotency-Key is required")
	}
	body := generated.NewThread{AgentId: a.ID, Payload: payload, AgentRevisionId: options.AgentRevisionId,
		Delivery: options.Delivery, Environments: options.Environments, Kind: options.Kind,
		McpHeaders: options.McpHeaders, Memories: options.Memories, MessageHistory: options.MessageHistory,
		Options: options.Options, SessionId: options.SessionId}
	observation, cancel := observationContext(ctx)
	r, err := a.client.api.CreateThreadApiV1ThreadsPost(observation, &generated.CreateThreadApiV1ThreadsPostParams{IdempotencyKey: options.RequestKey, XWorkspaceID: a.client.semanticWorkspace()}, body)
	result, err := jsonResult[generated.Submitted](a.client, r, err, 200, 201)
	if err != nil {
		cancel()
		return nil, err
	}
	bound, err := bindSubmitted(a.client, result)
	if err != nil {
		cancel()
		return nil, err
	}
	return newInteraction(observation, cancel, bound), nil
}

// Send continues an existing Thread using this explicit Agent, never an Agent
// inferred from a Thread's previous Run.
func (a Agent) Send(ctx context.Context, threadID, text string, options SendOptions) (*Interaction, error) {
	return a.SendPayload(ctx, threadID, TextPayload(text), options)
}

// SendPayload keeps the complete Message options, delivery and revision choice.
func (a Agent) SendPayload(ctx context.Context, threadID string, payload generated.MessagePayload, options SendOptions) (*Interaction, error) {
	if err := validateClient(a.client, a.ID, threadID); err != nil {
		return nil, err
	}
	if options.RequestKey == "" {
		return nil, errors.New("Idempotency-Key is required")
	}
	body := generated.Message{AgentId: a.ID, Payload: payload, AgentRevisionId: options.AgentRevisionId,
		Delivery: options.Delivery, Kind: options.Kind, Options: options.Options}
	observation, cancel := observationContext(ctx)
	r, err := a.client.api.SubmitMessageApiV1ThreadsThreadIdInboxPost(observation, threadID, &generated.SubmitMessageApiV1ThreadsThreadIdInboxPostParams{IdempotencyKey: options.RequestKey, XWorkspaceID: a.client.semanticWorkspace()}, body)
	result, err := jsonResult[generated.Submitted](a.client, r, err, 200, 201)
	if err != nil {
		cancel()
		return nil, err
	}
	submitted, err := bindSubmitted(a.client, result)
	if err != nil {
		cancel()
		return nil, err
	}
	if submitted.Thread.ID != threadID {
		cancel()
		return nil, fmt.Errorf("%w: submitted to another Thread", ErrProtocol)
	}
	return newInteraction(observation, cancel, submitted), nil
}

func bindSubmitted(c *Client, receipt Result[generated.Submitted]) (*submission, error) {
	v := receipt.Value
	if err := validateClient(c, v.Thread.Id, v.Entry.Id); err != nil {
		return nil, fmt.Errorf("%w: invalid receipt identity", ErrProtocol)
	}
	if v.Entry.ThreadId != v.Thread.Id || !v.Run.IsSpecified() {
		return nil, fmt.Errorf("%w: inconsistent receipt", ErrProtocol)
	}
	thread := c.Thread(v.Thread.Id)
	result := &submission{Receipt: receipt, Thread: thread, Entry: c.Entry(thread.ID, v.Entry.Id)}
	if run, err := v.Run.Get(); err == nil {
		if run.Id == "" || run.ThreadId != thread.ID {
			return nil, fmt.Errorf("%w: inconsistent receipt Run", ErrProtocol)
		}
		bound := c.Run(run.Id)
		result.Run = &bound
	}
	return result, nil
}

func (t Thread) Get(ctx context.Context) (Result[generated.ThreadView], error) {
	if err := validateClient(t.client, t.ID); err != nil {
		return Result[generated.ThreadView]{}, err
	}
	r, err := t.client.api.GetThreadApiV1ThreadsThreadIdGet(ctx, t.ID, &generated.GetThreadApiV1ThreadsThreadIdGetParams{XWorkspaceID: t.client.semanticWorkspace()})
	result, err := jsonResult[generated.ThreadView](t.client, r, err, 200)
	if err == nil && result.Value.Id != t.ID {
		return result, fmt.Errorf("%w: inconsistent Thread identity", ErrProtocol)
	}
	return result, err
}

func (e Entry) Get(ctx context.Context) (Result[generated.EntryView], error) {
	if err := validateClient(e.client, e.ThreadID, e.ID); err != nil {
		return Result[generated.EntryView]{}, err
	}
	r, err := e.client.api.GetEntryApiV1ThreadsThreadIdInboxEntryIdGet(ctx, e.ThreadID, e.ID, &generated.GetEntryApiV1ThreadsThreadIdInboxEntryIdGetParams{XWorkspaceID: e.client.semanticWorkspace()})
	result, err := jsonResult[generated.EntryView](e.client, r, err, 200)
	if err == nil && (result.Value.Id != e.ID || result.Value.ThreadId != e.ThreadID) {
		return result, fmt.Errorf("%w: inconsistent Entry identity", ErrProtocol)
	}
	return result, err
}

func (r Run) Get(ctx context.Context) (Result[generated.RunView], error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return Result[generated.RunView]{}, err
	}
	resp, err := r.client.api.GetRunApiV1RunsRunIdGet(ctx, r.ID, &generated.GetRunApiV1RunsRunIdGetParams{XWorkspaceID: r.client.semanticWorkspace()})
	result, err := jsonResult[generated.RunView](r.client, resp, err, 200)
	if err == nil && result.Value.Id != r.ID {
		return result, fmt.Errorf("%w: inconsistent Run identity", ErrProtocol)
	}
	return result, err
}

// Items reads authoritative committed Run history; stream deltas are not final.
func (r Run) Items(ctx context.Context) (Result[generated.RunItems], error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return Result[generated.RunItems]{}, err
	}
	resp, err := r.client.api.RunItemsApiV1RunsRunIdItemsGet(ctx, r.ID, &generated.RunItemsApiV1RunsRunIdItemsGetParams{XWorkspaceID: r.client.semanticWorkspace()})
	result, err := jsonResult[generated.RunItems](r.client, resp, err, 200)
	if err == nil && result.Value.Run.Id != r.ID {
		return result, fmt.Errorf("%w: inconsistent Run Items identity", ErrProtocol)
	}
	return result, err
}

func outcome(r Run, snapshot Result[generated.RunView]) RunOutcome {
	return RunOutcome{Run: r, Snapshot: snapshot}
}

// Wait observes this exact Run until completed, waiting, failed or cancelled.
// One caller deadline covers every request, response body and polling sleep.
func (r Run) Wait(ctx context.Context) (RunOutcome, error) {
	ctx, cancel := observationContext(ctx)
	defer cancel()
	snapshot, err := waitFor(ctx, r.client, r.Get, func(v generated.RunView) bool {
		switch v.Status {
		case generated.RunStatusCompleted, generated.RunStatusWaiting, generated.RunStatusFailed, generated.RunStatusCancelled:
			return true
		default:
			return false
		}
	})
	if err != nil {
		return RunOutcome{}, err
	}
	return outcome(r, snapshot), nil
}

// Wait first awaits durable incorporation of this Entry, then the *exact* Run
// that consumed it. An assigned_run_id on a pending Entry is not incorporation.
func (s submission) wait(ctx context.Context, incorporated func(Run)) (RunOutcome, error) {
	ctx, cancel := observationContext(ctx)
	defer cancel()
	entry, err := waitFor(ctx, s.Entry.client, s.Entry.Get, func(v generated.EntryView) bool {
		return v.Status == generated.EntryStatusConsumed || v.Status == generated.EntryStatusFailed || v.Status == generated.EntryStatusWithdrawn
	})
	if err != nil {
		return RunOutcome{}, err
	}
	if entry.Value.ThreadId != s.Thread.ID || entry.Value.Id != s.Entry.ID {
		return RunOutcome{}, fmt.Errorf("%w: inconsistent entry identity", ErrProtocol)
	}
	if entry.Value.Status != generated.EntryStatusConsumed {
		return RunOutcome{}, &EntryDispositionError{Entry: s.Entry, Snapshot: entry}
	}
	id, err := entry.Value.AssignedRunId.Get()
	if err != nil || id == "" {
		return RunOutcome{}, fmt.Errorf("%w: consumed entry without Run", ErrProtocol)
	}
	run := s.Thread.client.Run(id)
	if incorporated != nil {
		incorporated(run)
	}
	snapshot, err := waitFor(ctx, run.client, run.Get, func(v generated.RunView) bool {
		switch v.Status {
		case generated.RunStatusCompleted, generated.RunStatusWaiting, generated.RunStatusFailed, generated.RunStatusCancelled:
			return true
		default:
			return false
		}
	})
	if err != nil {
		return RunOutcome{}, err
	}
	if snapshot.Value.ThreadId != s.Thread.ID {
		return RunOutcome{}, fmt.Errorf("%w: entry Run belongs to another Thread", ErrProtocol)
	}
	return outcome(run, snapshot), nil
}

func observationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 300*time.Second)
}

func waitFor[T any](ctx context.Context, c *Client, get func(context.Context) (Result[T], error), done func(T) bool) (Result[T], error) {
	var zero Result[T]
	if c == nil {
		return zero, errors.New("resource is not bound to a client")
	}
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := get(ctx)
		if err != nil || done(result.Value) {
			return result, err
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-c.lifetime.Done():
			timer.Stop()
			return zero, ErrClosed
		case <-timer.C:
		}
	}
}

// Resume requests a distinct successor Run and returns it with original status
// and headers. It never mutates the bound predecessor's identity.
func (r Run) Resume(ctx context.Context, request generated.Resume, requestKey string) (Run, Result[generated.RunView], error) {
	var zero Result[generated.RunView]
	if err := validateClient(r.client, r.ID); err != nil {
		return Run{}, zero, err
	}
	if requestKey == "" {
		return Run{}, zero, errors.New("Idempotency-Key is required")
	}
	resp, err := r.client.api.ResumeRunApiV1RunsRunIdResumePost(ctx, r.ID, &generated.ResumeRunApiV1RunsRunIdResumePostParams{IdempotencyKey: requestKey, XWorkspaceID: r.client.semanticWorkspace()}, request)
	result, err := jsonResult[generated.RunView](r.client, resp, err, 200, 201)
	if err != nil {
		return Run{}, result, err
	}
	if result.Value.Id == "" || result.Value.Id == r.ID {
		return Run{}, result, fmt.Errorf("%w: invalid successor Run", ErrProtocol)
	}
	return r.client.Run(result.Value.Id), result, nil
}

// Interrupt requests a remote mutation; Close and local cancellation do not.
func (r Run) Interrupt(ctx context.Context) (Result[generated.RunView], error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return Result[generated.RunView]{}, err
	}
	resp, err := r.client.api.InterruptRunApiV1RunsRunIdInterruptPost(ctx, r.ID, &generated.InterruptRunApiV1RunsRunIdInterruptPostParams{XWorkspaceID: r.client.semanticWorkspace()})
	return jsonResult[generated.RunView](r.client, resp, err, 200)
}
