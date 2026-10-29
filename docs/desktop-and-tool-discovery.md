# Desktop control, tool discovery and context usage

These are native Service APIs on the existing `client.API()` transport. Service owns desktop permissions, target reservations, provider access and tool discovery execution. The SDK neither launches a desktop nor renews a control lease, answers a deferred call or resumes a Run automatically.

## Configure discovery without changing selected tools

Tool search presents already selected sources through bounded proxy discovery. It grants no source authority. The root aliases canonical generated types:

```go
func discoveryOptions() a13n.RunOptions {
    search := a13n.ToolSearch{
        Config: &a13n.ToolSearchConfig{SearchName: a13n.Ptr("find_tools"), MaxResults: a13n.Ptr(7)},
        Groups: a13n.Ptr(map[string]a13n.ToolSearchGroup{}),
    }
    return a13n.RunOptions{Overrides: a13n.Value(a13n.Overrides{ToolSearch: a13n.Value(search)})}
}
```

Omitted mode uses Service's proxy default. Native mode and strategy fields are rejected. `config:null` and `groups:null` are invalid. An omitted/null Run override inherits the revision; an explicit block replaces it, and an empty groups map disables grouping.

## Observe a desktop wait, then decide explicitly

`Interaction.Result` finishes normally when its exact Run seals `waiting`, including `wait_reason="desktop_control"`. Read `outcome.Evidence().Value.WaitReason`, `outcome.Pending` and the raw evidence. User-origin desktop waiting exposes `Pending.DesktopControlId`; its approvals/calls may both be empty. No pending tool call is fabricated. Local `Close`, context cancellation and observation EOF do not complete or interrupt the durable wait.

Desktop assistance is a separate control record. Native operations cover:

- `GET /runs/{run}/desktop-assistance/{call}`: Agent-requested assistance metadata.
- `GET /runs/{run}/desktop-control`: user-control metadata or successful JSON null.
- `POST /runs/{run}/desktop-control`: open a user-origin context for a frozen mount and reason; viewing does not pause execution.
- `POST /desktop-assistance/{id}/request-control`: request durable suspension; `preparing` is not an input grant.
- `POST /desktop-assistance/{id}/view`, `/control`, `/disconnect`, `/complete`: explicit grant, handback and completion operations.

The `Control.SessionId` / `Completion.SessionId` field is a caller-managed browser-session token (20–72 URL-safe characters), **not** a Service Session resource ID. Epoch is the server's fencing value, including zero. Only access responses contain direct connection credentials; retain them in memory rather than logs. Generated raw bodies remain caller-owned; `ParseJSON` closes them. Session-authenticated generated calls pass `XWorkspaceID` explicitly.

After the person hands back control, the application can explicitly persist completion. Import `context`, `a13n` and `generated`:

```go
func completeDesktop(ctx context.Context, client *a13n.Client, controlID, browserSession string, epoch int) (generated.View, error) {
    api, err := client.API()
    if err != nil { return generated.View{}, err }
    response, err := api.CompleteAssistance(ctx, controlID, nil,
        generated.Completion{SessionId: browserSession, Epoch: epoch, Note: a13n.Ptr("Handed back to the agent.")})
    result, err := a13n.ParseJSON[generated.View](client, response, err, 200)
    return result.Value, err
}
```

Completion revokes connections and confirms input cleanup; it does not start a successor. Only after successful handback/reconciliation may the application explicitly call `client.Run(runID).Resume(ctx, a13n.Answers{})` for a desktop-only user pause. Mixed waits still require every native call and approval. For Agent-requested assistance, return the exact persisted assistance outcome for that call along with all other pending answers. Service validates the complete batch and stored completion transactionally; do not fabricate an outcome from the viewer state. The returned Interaction binds the explicit successor, not the original waiting Run.

Disconnect or lease expiry revokes access but leaves the task paused. Reopening a viewer never reacquires control implicitly. There is no SDK lease loop, automatic retry/reclaim, ordinary resume on completion, or automatic successor-following. See the upstream [desktop contract](https://github.com/converge-ai-labs/agent-foundation/blob/647371cdc6f1e85b9c4ccfe954999823f52f08e8/spec/a13n-service/06a-environment-management.md).

## Read native context usage

Context usage reports one latest completed generation contribution and its currently configured context window, not a pending prompt estimate. Service validates **exactly one** of Thread ID, Run ID or model-request record ID. A successful JSON null means no completed contribution; nullable token/window fields mean unavailable values, not zero. Do not fabricate totals, capacity or ratios.

Use an explicit pointer type to retain nullable success. This function returns nil without an error for JSON null:

```go
func readContextUsage(ctx context.Context, client *a13n.Client, runID string) (*generated.ContextUsage, error) {
    api, err := client.API()
    if err != nil { return nil, err }
    response, err := api.ContextUsage(ctx,
        &generated.ContextUsageParams{RunId: &runID})
    result, err := a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
    return result.Value, err
}
```

For other selectors, set only `ThreadId` or `RecordId` instead. A missing target is a Service error, distinct from successful null. Generated `WithResponse` calls expose the native response bytes/status as well; choose `ParseJSON[*T]` when typed nullable success and bounded decoding are useful. A non-pointer `ParseJSON[T]` continues to reject a null resource response.

Session observation remains one cursor-free connection with no default extra frame limit or automatic reconnect. A fresh connection to an already-sealed Run yields Thread state with `last_run`; read that Run's historical Items explicitly if needed. Desktop waiting and tool-search configuration use the same pinned Session schema; no UI reducer or recovery policy is added.

## Browse bounded run history

Run lists return `RunListItem` metadata with `input_preview.text` (at most 200 Unicode code points) and `input_preview.truncated`. Fetch an individual Run for full input, output, options or memory mounts. List `view` selection is removed.

Thread runs default to newest first. `ThreadRunPage` exposes `total`, `previous_cursor` (newer) and `next_cursor` (older). Each `ThreadRunItem` carries its chronological `number`, nullable decimal-string `cost`, `model_calls` and `unpriced_requests`; use the item's `number` instead of the removed page-level `first_number`. Use `anchor` for the first page around an exact Run; subsequent requests send the returned cursor without the anchor. Alternatively, `sort=cost|calls|duration` with `direction=asc|desc` ranks the whole Thread before pagination and cannot be combined with `anchor`. Ranking has no previous cursor. Reads remain explicit.

Session lists support `sort=updated_at|runs|cost` and `direction=asc|desc`. Session views require `run_count`, nullable decimal-string `cost` and `unpriced_requests`. Unknown cost remains null, distinct from measured zero. Session streams also expose native schedule and occurrence frames.
