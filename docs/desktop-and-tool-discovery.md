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

## Environment desktop control and historical waits

Current desktop access is scoped to an Environment. Generated `GetDesktopControl`, `OpenDesktopViewer`, `TakeOverDesktop`, `RenewDesktopControl`, and `ReturnDesktopControl` operations use `client.API()`. Viewer access and Takeover are distinct; takeovers are explicitly ended through the Environment. Keep returned connection credentials private. The SDK does not automatically acquire control or resume Runs.

Sealed historical Runs may retain `wait_reason="desktop_control"` and a historical `desktop_control_id`. `Interaction.Result` preserves the pending union and raw evidence: use `outcome.Pending.AsHistoricalDesktopPending()` for that historical form, or `AsPending()` for current deferred calls and approvals. Historical records do not establish a current desktop-control endpoint or a resumable desktop-only wait. Session projections carry the same historical distinction.

## Read native context usage

Context usage reports one latest completed generation contribution and its currently configured context window, not a pending prompt estimate. Service validates **exactly one** of Thread ID, Run ID or model-request record ID. A Thread without Runs returns JSON null; a Run without an eligible contribution returns its ID and configured capacity with null record ID and token usage. Nullable token/window fields mean unavailable values, not zero. Do not fabricate totals, capacity or ratios.

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

Session observation remains one cursor-free connection with no default extra frame limit or automatic reconnect. A fresh connection to an already-sealed Run yields Thread state with `last_run`; read that Run's historical Items explicitly if needed. Historical desktop waits and tool-search configuration use the same pinned Session schema; no UI reducer or recovery policy is added.

## Browse bounded run history

Run lists return `RunListItem` metadata with `input_preview.text` (at most 200 Unicode code points) and `input_preview.truncated`. Fetch an individual Run for full input, output, options or memory mounts. List `view` selection is removed.

Thread runs default to newest first. `ThreadRunPage` exposes `total`, `previous_cursor` (newer) and `next_cursor` (older). Each `ThreadRunItem` carries its chronological `number`, nullable decimal-string `cost`, `model_calls` and `unpriced_requests`; use the item's `number` instead of the removed page-level `first_number`. Use `anchor` for the first page around an exact Run; subsequent requests send the returned cursor without the anchor. Alternatively, `sort=cost|calls|duration` with `direction=asc|desc` ranks the whole Thread before pagination and cannot be combined with `anchor`. Ranking has no previous cursor. Reads remain explicit.

Session lists support `sort=updated_at|runs|cost` and `direction=asc|desc`. Session views require `run_count`, nullable decimal-string `cost` and `unpriced_requests`. Unknown cost remains null, distinct from measured zero. Session streams also expose native schedule and occurrence frames.
