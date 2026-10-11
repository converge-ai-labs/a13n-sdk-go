package a13n

import (
	"bytes"
	"context"
	"encoding/json"
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
	IdempotencyKey  string
	AgentRevisionId nullable.Nullable[string]
	Delivery        *Delivery
	Environments    *InitialMounts
	Kind            *NewThreadKind
	McpHeaders      *McpHeaders
	Memories        *[]MemoryMount
	MessageHistory  *MessageHistory
	// Options includes the complete typed Configuration snapshot, independent
	// of Agent Overrides. Its nullable fields are forwarded without merging.
	Options   *RunOptions
	SessionId nullable.Nullable[string]
}

// SendOptions exposes all user-selectable Message fields. The Agent and payload
// come from the binding and Send/SendInput arguments.
type SendOptions struct {
	IdempotencyKey  string
	AgentRevisionId nullable.Nullable[string]
	Delivery        *Delivery
	Kind            *MessageKind
	// Options.Configuration is forwarded as authored; the Service decides
	// whether it matches an active Run or selects a new next_run snapshot.
	Options *RunOptions
}

// submission is acceptance, not completion. Run is nil for queued input.
type submission struct {
	Receipt    Result[generated.Submitted]
	Thread     Thread
	Entry      Entry
	Run        *Run
	Key        string
	HasReceipt bool
}

// RunOutcome projects one authoritative snapshot, not mutable server state.
// Waiting/failed/cancelled status is not a success promise.
type RunOutcome struct {
	Run       Run
	ThreadID  string
	RunID     string
	Status    RunStatus
	Output    any
	Pending   *Pending
	Failure   *Failure
	evidence  Result[generated.RunView]
	rawOutput json.RawMessage
}

// Text returns only a string Run.output; empty strings remain present.
func (o RunOutcome) Text() (string, bool) { text, ok := o.Output.(string); return text, ok }

// Evidence exposes the original typed HTTP response, including omission/null.
func (o RunOutcome) Evidence() Result[generated.RunView] { return o.evidence }

// DecodeOutput decodes the original JSON directly, never through float64.
func DecodeOutput[T any](o RunOutcome) (T, error) {
	var value T
	if len(o.rawOutput) == 0 {
		return value, errors.New("Run output is absent")
	}
	decoder := json.NewDecoder(bytes.NewReader(o.rawOutput))
	decoder.UseNumber()
	err := decoder.Decode(&value)
	return value, err
}

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

// Start submits text; at most one options value is accepted.
func (a Agent) Start(ctx context.Context, text string, options ...StartOptions) (*Interaction, error) {
	return a.StartInput(ctx, Input{Content: []Part{TextPart(text)}}, options...)
}

// StartInput submits ordered multipart content and the complete creation options.
func (a Agent) StartInput(ctx context.Context, input Input, options ...StartOptions) (*Interaction, error) {
	if err := validateClient(a.client, a.ID); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, errors.New("Start accepts at most one options value")
	}
	var opts StartOptions
	if len(options) == 1 {
		opts = options[0]
	}
	payload, err := input.Wire()
	if err != nil {
		return nil, err
	}
	body := generated.NewThread{AgentId: a.ID, Payload: payload, AgentRevisionId: opts.AgentRevisionId,
		Delivery: opts.Delivery, Environments: opts.Environments, Kind: opts.Kind, McpHeaders: opts.McpHeaders,
		Memories: opts.Memories, MessageHistory: opts.MessageHistory, Options: opts.Options, SessionId: opts.SessionId}
	observation, cancel := observationContext(ctx)
	mutation, dispatch, key, err := mutationContext(observation, a.client, opts.IdempotencyKey)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := a.client.api.CreateThreadApiV1ThreadsPost(mutation, &generated.CreateThreadApiV1ThreadsPostParams{IdempotencyKey: key, XWorkspaceID: a.client.semanticWorkspace()}, body)
	result, err := jsonResult[generated.Submitted](a.client, resp, err, 200, 201)
	var submitted *submission
	if err == nil {
		submitted, err = bindSubmitted(a.client, result)
	}
	if err != nil {
		cancel()
		return nil, mutationError(err, dispatch, key, "start", "", "")
	}
	submitted.Key, submitted.HasReceipt = key, true
	return newInteraction(observation, cancel, submitted), nil
}

// Send names both Agent and Thread explicitly, never an inferred previous Agent.
func (a Agent) Send(ctx context.Context, threadID, text string, options ...SendOptions) (*Interaction, error) {
	return a.SendInput(ctx, threadID, Input{Content: []Part{TextPart(text)}}, options...)
}
func (a Agent) SendInput(ctx context.Context, threadID string, input Input, options ...SendOptions) (*Interaction, error) {
	if err := validateClient(a.client, a.ID, threadID); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, errors.New("Send accepts at most one options value")
	}
	var opts SendOptions
	if len(options) == 1 {
		opts = options[0]
	}
	payload, err := input.Wire()
	if err != nil {
		return nil, err
	}
	body := generated.Message{AgentId: a.ID, Payload: payload, AgentRevisionId: opts.AgentRevisionId, Delivery: opts.Delivery, Kind: opts.Kind, Options: opts.Options}
	observation, cancel := observationContext(ctx)
	mutation, dispatch, key, err := mutationContext(observation, a.client, opts.IdempotencyKey)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := a.client.api.SubmitMessageApiV1ThreadsThreadIdInboxPost(mutation, threadID, &generated.SubmitMessageApiV1ThreadsThreadIdInboxPostParams{IdempotencyKey: key, XWorkspaceID: a.client.semanticWorkspace()}, body)
	result, err := jsonResult[generated.Submitted](a.client, resp, err, 200, 201)
	var submitted *submission
	if err == nil {
		submitted, err = bindSubmitted(a.client, result)
	}
	if err == nil && submitted.Thread.ID != threadID {
		err = fmt.Errorf("%w: submitted to another Thread", ErrProtocol)
	}
	if err != nil {
		cancel()
		return nil, mutationError(err, dispatch, key, "send", threadID, "")
	}
	submitted.Key, submitted.HasReceipt = key, true
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

// RunItemsOptions selects one ordinal window. Before and After exclude each
// other. Nil Limit uses the Service default (200); the Service validates bounds.
// A historical window cannot establish live display coverage.
type RunItemsOptions struct {
	Before *int
	After  *int
	Limit  *int
}

// Items reads one committed display window, not necessarily all Run history.
// With no Before/After it includes the entire mutable tail, even beyond Limit,
// and can seed live coverage. Complete means sealed, not all history loaded.
// At most one options value may be supplied; omitting it uses Service defaults.
func (r Run) Items(ctx context.Context, options ...RunItemsOptions) (Result[generated.RunItems], error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return Result[generated.RunItems]{}, err
	}
	if len(options) > 1 {
		return Result[generated.RunItems]{}, errors.New("Items accepts at most one options value")
	}
	params := generated.RunItemsApiV1RunsRunIdItemsGetParams{XWorkspaceID: r.client.semanticWorkspace()}
	if len(options) == 1 {
		params.Before, params.After, params.Limit = options[0].Before, options[0].After, options[0].Limit
	}
	resp, err := r.client.api.RunItemsApiV1RunsRunIdItemsGet(ctx, r.ID, &params)
	result, err := jsonResult[generated.RunItems](r.client, resp, err, 200)
	if err == nil && result.Value.Run.Id != r.ID {
		return result, fmt.Errorf("%w: inconsistent Run Items identity", ErrProtocol)
	}
	return result, err
}

func outcome(r Run, snapshot Result[generated.RunView]) RunOutcome {
	v := snapshot.Value
	o := RunOutcome{Run: r, RunID: r.ID, ThreadID: v.ThreadId, Status: v.Status, evidence: snapshot}
	if pending, err := v.Pending.Get(); err == nil {
		o.Pending = &pending
	}
	if failure, err := v.Failure.Get(); err == nil {
		o.Failure = &failure
	}
	var raw struct {
		Output json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(snapshot.raw, &raw)
	o.rawOutput = raw.Output
	if len(raw.Output) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw.Output))
		decoder.UseNumber()
		_ = decoder.Decode(&o.Output)
	}
	return o
}

// Observe binds this exact Run to a lazy finite Interaction without I/O.
func (r Run) Observe(ctx context.Context) (*Interaction, error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return nil, err
	}
	observation, cancel := observationContext(ctx)
	return newRunInteraction(observation, cancel, r, "", nil, "", "observed_run"), nil
}

// Result is the result-only convenience over the same exact observer.
func (r Run) Result(ctx context.Context) (RunOutcome, error) {
	i, err := r.Observe(ctx)
	if err != nil {
		return RunOutcome{}, err
	}
	defer i.Close()
	return i.Result(ctx)
}

// Observe recovers the exact Entry without a mutation or fabricated receipt.
func (e Entry) Observe(ctx context.Context) (*Interaction, error) {
	if err := validateClient(e.client, e.ThreadID, e.ID); err != nil {
		return nil, err
	}
	observation, cancel := observationContext(ctx)
	return newInteraction(observation, cancel, &submission{Thread: e.client.Thread(e.ThreadID), Entry: e}), nil
}
func sealed(v generated.RunView) bool {
	switch v.Status {
	case generated.RunStatusCompleted, generated.RunStatusWaiting, generated.RunStatusFailed, generated.RunStatusCancelled:
		return true
	}
	return false
}
func (r Run) wait(ctx context.Context, threadID string, incorporated func(Run, string)) (RunOutcome, error) {
	bound := false
	snapshot, err := waitFor(ctx, r.client, func(ctx context.Context) (Result[generated.RunView], error) {
		result, err := r.Get(ctx)
		if err == nil && (result.Value.ThreadId == "" || (threadID != "" && result.Value.ThreadId != threadID)) {
			return result, fmt.Errorf("%w: inconsistent Run Thread", ErrProtocol)
		}
		if err == nil && !bound {
			bound = true
			if incorporated != nil {
				incorporated(r, result.Value.ThreadId)
			}
		}
		return result, err
	}, sealed)
	if err != nil {
		return RunOutcome{}, err
	}
	return outcome(r, snapshot), nil
}

// Wait first awaits durable incorporation of this Entry, then the *exact* Run
// that consumed it. An assigned_run_id on a pending Entry is not incorporation.
func (s submission) wait(ctx context.Context, incorporated func(Run, string)) (RunOutcome, error) {
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
		incorporated(run, s.Thread.ID)
	}
	return run.wait(ctx, s.Thread.ID, nil)
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

// Resume submits one answer batch and observes the distinct exact successor.
func (r Run) Resume(ctx context.Context, answers Answers, options ...ResumeOptions) (*Interaction, error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, errors.New("Resume accepts at most one options value")
	}
	var opts ResumeOptions
	if len(options) == 1 {
		opts = options[0]
	}
	request, err := answers.wire(opts)
	if err != nil {
		return nil, err
	}
	observation, cancel := observationContext(ctx)
	mutation, dispatch, key, err := mutationContext(observation, r.client, opts.IdempotencyKey)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := r.client.api.ResumeRunApiV1RunsRunIdResumePost(mutation, r.ID, &generated.ResumeRunApiV1RunsRunIdResumePostParams{IdempotencyKey: key, XWorkspaceID: r.client.semanticWorkspace()}, request)
	result, err := jsonResult[generated.RunView](r.client, resp, err, 200, 201)
	if err == nil && (validateID(result.Value.Id) != nil || result.Value.Id == r.ID || validateID(result.Value.ThreadId) != nil) {
		err = fmt.Errorf("%w: invalid successor Run", ErrProtocol)
	}
	if err != nil {
		cancel()
		return nil, mutationError(err, dispatch, key, "resume", "", r.ID)
	}
	return newRunInteraction(observation, cancel, r.client.Run(result.Value.Id), result.Value.ThreadId, &result, key, "resumed_run"), nil
}

// Interrupt requests a remote mutation; Close and local cancellation do not.
func (r Run) Interrupt(ctx context.Context) (Result[generated.RunView], error) {
	if err := validateClient(r.client, r.ID); err != nil {
		return Result[generated.RunView]{}, err
	}
	resp, err := r.client.api.InterruptRunApiV1RunsRunIdInterruptPost(ctx, r.ID, &generated.InterruptRunApiV1RunsRunIdInterruptPostParams{XWorkspaceID: r.client.semanticWorkspace()})
	return jsonResult[generated.RunView](r.client, resp, err, 200)
}
