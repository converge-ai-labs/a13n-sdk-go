# Streaming and readback

Use `Result(ctx)` alone for the final answer: it opens no SSE connection and needs no Items request. For optional progress, call `Text()` on the **same** finite Interaction. Import `context`, `errors`, `fmt`, `io` and the root SDK (`a13n`):

```go
func streamAndRead(ctx context.Context, interaction *a13n.Interaction, out io.Writer) error {
    defer interaction.Close()
    for chunk, err := range interaction.Text() {
        if err != nil {
            var interrupted *a13n.PreviewInterruptedError
            if !errors.As(err, &interrupted) { return err }
            fmt.Fprintln(out, "Preview interrupted:", interrupted.Reason)
            break
        }
        fmt.Fprint(out, chunk)
    }
    result, err := interaction.Result(ctx)
    if err != nil { return err }
    if result.Status != a13n.RunCompleted {
        return fmt.Errorf("run %s ended %s", result.RunID, result.Status)
    }
    fmt.Fprintln(out, "Final output:", result.Output)
    return nil
}
```

Preview emits only string CONTENT with a matching root assistant START, not user, child or `display:false` content. It keeps at most eight active message identities, including suppressed STARTs until END; `display:false` need not repeat on every content frame. It is not a transcript buffer. An initial boundary establishes only an attempt/sequence coordinate, not active message state; following orphan content or a visible unmatched END still interrupts. Later late/equal boundaries are valid and do not move delta coverage; a boundary ahead of received deltas interrupts rather than hiding missing output. Same-attempt duplicate or descending deltas also interrupt rather than being silently discarded. Protocol parsing failures likewise become sticky preview interruptions with their original error cause. Gap/reset, a missing/ambiguous START, discontinuity in the same Run envelope's attempt/sequence, unsupported content or identity-bound exhaustion yields typed `PreviewInterruptedError`. The interruption is sticky for preview and does not cancel the outcome observer. Unknown unframed CUSTOM events are ignored; canonical exact-root `CUSTOM name:"a13n.stream.fragment"` interrupts as conservative **unsupported framing**, not proof of text loss. The helper never parses opaque fragment IDs/data or assembles original custom events; explicitly attributed child fragments and hidden events are excluded first.

One preview may begin, and only one reader may be active. Early break does not drain or close: always defer Close. Deliberate sequential `Next()` after interruption remains available for advanced native frames; concurrent raw/preview reading is rejected, including while a preview yield is paused. Raw frames consumed before preview make its missing start state explicit rather than pretending to reconstruct it.

A Run can seal before SSE attachment, so zero chunks or frames is valid. Final `Output` is authoritative and may be structured rather than a string. Items are advanced committed display windows, not the final reply or automatically complete history. Content can carry truncation/omission markers.

## Advanced: native finite frames

`Next()` returns typed Run-scoped delta/boundary/gap/reset frames, not an assistant transcript. It filters other Runs and Thread-wide changed hints. Its `io.EOF` is normal finite completion; check Result separately. Stream/protocol/deadline errors are not natural EOF. A broken optional reader does not prove Run failure.

```go
for {
    frame, err := interaction.Next()
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    if delta, ok := frame.(a13n.DeltaFrame); ok { fmt.Println(delta.Event) }
}
```

## Read one ordinal history window

`Run.Items(ctx)` uses Service defaults. Pass a single `RunItemsOptions` to request a different recent limit or one historical window. The pointers distinguish omission from zero (`After: &zero` starts just after ordinal 0). Service validates `Before >= 1`, `After >= 0`, mutually exclusive Before/After, and `Limit` 1–500 (default 200). Import `context`, `a13n` and `generated`:

```go
func earlierWindow(ctx context.Context, client *a13n.Client, runID string, before int) (
    a13n.Result[generated.RunItems], error,
) {
    limit := 50
    return client.Run(runID).Items(ctx, a13n.RunItemsOptions{
        Before: &before, Limit: &limit,
    })
}
```

If the current window's first ordinal exceeds 1, pass that ordinal as Before to request earlier display. After requests the items just after an ordinal; both boundaries are exclusive. The SDK neither uses the generic cursor `Pages` iterator here nor automatically loads unbounded history. Explicit Before/After windows have `Baseline=false` and null Continuation/Position/ResumeAfter, even for a sealed Run. Render them as history only: they cannot replace a live baseline, advance coverage, heal gaps or seal a live consumer. Only the default no-boundary read includes the whole mutable tail plus normalization continuation and can establish coverage. A recent sealed baseline can still omit earlier ordinals.

## Deadlines and cleanup

Create a context with a deadline *before* calling `Start` or `Send`. The same observation window includes submission/resume, queue wait, exact Run reads and stream attachment/body; without a caller deadline the SDK defaults to 300 seconds. For example:

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
defer cancel()
interaction, err := client.Agent(agentID).Start(ctx, "Explain the changes.")
if err != nil { return err }
return streamAndRead(ctx, interaction, os.Stdout)
```

This snippet also imports `context`, `os` and `time`. `streamAndRead` closes its interaction on every path; if you do not use that helper, `defer interaction.Close()` immediately after successful submission. Close releases local reads without approving work or interrupting the remote Run. To request a *remote* interruption, call `result.Run.Interrupt(ctx)` deliberately. A shorter context passed to `Result` stops only that one caller's wait while the interaction remains open; it does not extend the invocation's original deadline.

## Recover a display after a stream gap

If you have persisted an exact Run ID, `client.Run(runID).Items(ctx)` rereads its committed display. This works after reconnection without fabricating missing deltas:

```go
items, err := client.Run(savedRunID).Items(ctx)
if err != nil { return err }
fmt.Printf("display sealed=%t, baseline=%t\n", items.Value.Complete, items.Value.Baseline)
if err := json.NewEncoder(os.Stdout).Encode(items.Value.Items); err != nil { return err }
```

## Rebuild an advanced reader from committed coverage

The generated persistent reader accepts `Run` and `Position` **together**. `RunItems.Position` is the `{attempt}-{sequence}` coverage of the display; `ResumeAfter` is an optional Redis ID hint, not a second coverage cursor. Only a `Baseline=true` response can seed live state. Preserve its native `Continuation`, `Complete` and item truncation/omission metadata; continuation is normalization state, not an execution checkpoint. Historical Before/After windows have null continuation/position/hint and cannot heal a gap or end an existing live consumer, even when Complete is true. Missing or null hints are valid; a retained, expired or incompatible hint does not change the position claim. Import `context`, `fmt`, `net/http`, `a13n` and `generated`:

```go
func rebuildReader(ctx context.Context, client *a13n.Client, runID string) (
    a13n.Result[generated.RunItems], *http.Response, error,
) {
    snapshot, err := client.Run(runID).Items(ctx)
    if err != nil { return snapshot, nil, err }
    if snapshot.Value.Run.Id != runID {
        return snapshot, nil, fmt.Errorf("items do not belong to run %s", runID)
    }
    if !snapshot.Value.Baseline {
        return snapshot, nil, fmt.Errorf("historical window is not a live baseline")
    }
    if snapshot.Value.Complete { return snapshot, nil, nil }
    position := snapshot.Value.Position.GetOrEmpty()
    if position == "" {
        return snapshot, nil, fmt.Errorf("no committed display yet; wait for a checkpoint")
    }
    params := generated.ThreadStreamApiV1ThreadsThreadIdStreamGetParams{
        Run: &runID, Position: &position,
    }
    if after := snapshot.Value.ResumeAfter.GetOrEmpty(); after != "" {
        params.LastEventID = &after
    }
    api, err := client.API()
    if err != nil { return snapshot, nil, err }
    response, err := api.ThreadStreamApiV1ThreadsThreadIdStreamGet(ctx,
        snapshot.Value.Run.ThreadId, &params)
    if err != nil { return snapshot, nil, err }
    if response.StatusCode != http.StatusOK {
        _, err = a13n.ParseJSON[struct{}](client, response, nil, http.StatusOK)
        return snapshot, nil, err
    }
    return snapshot, response, nil
}
```

Apply the returned baseline, including its normalization continuation, before applying raw SSE output from the returned response. The SDK does not implement this display reducer. The caller owns the persistent response body and must close it (and use a context deadline); a nil response means the Run is already sealed. This advanced path does not become another finite Agent invocation or follow a successor Run. Session callers must also set `XWorkspaceID` explicitly on generated params.

`a13n.GapFrame.Position` is a `*string`: nil means transport loss with no known missing range, while a value identifies the position the replacement snapshot must cover. A gap or a boundary beyond contiguous applied output is not proof that the hole healed. Do not advance coverage or the hint past it. Refresh Items when a newer boundary or terminal state can cover that target, rather than repeatedly reading the same stale snapshot. The explicit coverage reader freezes its position and hint after a gap, reset, uncovered sequence hole or newer attempt. Later contiguous deltas cannot unfreeze it: apply an authoritative snapshot, then explicitly rebuild the reader with that snapshot's position and hint. A stale snapshot must not be treated as covering the recovery target. A `ResetFrame` means superseded provisional output must be discarded; it does not prove an empty display baseline for the new attempt. Terminal reads replace provisional output with the saved display. The SDK returns these signals without silently folding events or doing readback.

Within the default finite interaction, asking for the next frame acknowledges the preceding one. Reconnect uses only its acknowledged Redis ID, never a merely received ID. It does not automatically add Run/position query parameters or claim a display baseline. Exact-Run filtering and the finite lifecycle remain unchanged. Close does not acknowledge the last returned frame. Advanced readers that maintain their own display must opt into coverage explicitly and follow the applied-only rule. Position components are canonical nonnegative decimal integers, at most 20 digits each; a hint alone uses the older `Last-Event-ID` replay behavior and cannot establish complete delivery.

A Run outcome's `Output` is an optional structured value, not the last text delta; `Items` is one window of persisted display messages and tool activity, not an automatically loaded full transcript. Handle `waiting` outcomes through [waiting and tools](waiting-and-tools.md), and consult [errors and recovery](errors-and-recovery.md) if `Next` fails before a result is available. For long-lived subscriptions to all activity on a Thread, the low-level generated `ThreadStreamApiV1ThreadsThreadIdStreamGet` remains available through `client.API()` with a caller-owned response body; it is not a second high-level Agent interaction.

## Explicitly expand saved display content

`Item.ContentRefs` maps field names to immutable saved-value references. Inline text can be only a preview; it does not replace `RunOutcome.Output`. Service keeps up to 32 KiB inline, externalizes larger fields, and caps saved values at 16 MiB. Above the cap it saves a UTF-8 text prefix, including for oversized JSON. Each reference contains media type, saved byte count, a preview of at most 4 KiB and optional `Truncated`. `RunContent.Truncated` must stay visible to the application after expansion.

This explicit read imports `context`, `a13n` and `generated`. Session callers pass their workspace ID; API-key callers pass nil:

```go
func expandSavedContent(ctx context.Context, client *a13n.Client, runID, contentID string, workspaceID *string) (
    a13n.Result[generated.RunContent], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[generated.RunContent]{}, err }
    response, err := api.RunContentApiV1RunsRunIdContentsContentIdGet(ctx, runID, contentID,
        &generated.RunContentApiV1RunsRunIdContentsContentIdGetParams{XWorkspaceID: workspaceID})
    return a13n.ParseJSON[generated.RunContent](client, response, err, 200)
}
```

`ParseJSON` retains native JSON numbers with `json.Number` for `RunContent.Value` and original bytes in `RawJSON`. Inspect `MediaType` before interpreting Value: JSON may be an object, array, scalar or null; text/plain is a string. Only committed references reachable under that exact Run are readable. Expansion is caller-selected; it neither repairs provisional gaps nor adds content to `Interaction.Text()`. The JSON envelope and string escaping can exceed `ParseJSON`'s default 16 MiB response bound even when saved content fits its own cap; configure `WithResponseLimit` for the application's accepted response budget, or consume the caller-owned raw generated response explicitly.

`DeltaFrame.Item.MemorySources` preserves absent/null/array, including an explicit empty array. END metadata and saved `Item.Content["memory_sources"]` identify automatic memory delivery evidence. A provisional END does not prove commit, and a source digest does not prove that a particular answer depended on it. Record write provenance uses the native nullable `MemoryRecordView.Provenance` independently.

## Native AG-UI 1.0 content and child attribution

The Service's `delta` envelope identifies the authoritative Service Run with `run_id`, attempt and sequence. Inside it, `DeltaFrame.Event` is a raw JSON map of native AG-UI 1.0 fields: `messageId`, `toolCallId`, `threadId`, `runId`, `subagentRunId` and `parentSubagentRunId` use their canonical camelCase names. The SDK does not rename fields, convert media parts to text, invent an event alias or build a UI reducer. New native fields and required `CUSTOM.value: null` are preserved. Treat `RUN_STARTED.protocolVersion` as protocol observation, not an instruction to select another Service Run.

Inline children may reuse a parent's message/tool IDs. Keep `subagentRunId` attribution when displaying their text, reasoning, tool results and custom observations; `SUBAGENT_STARTED`/`SUBAGENT_FINISHED` may also name `parentSubagentRunId` and `parentToolCallId`. Do not concatenate child output into root text. Native `TOOL_CALL_RESULT.content` may be a string or ordered content parts, including images and repeated parts; preserve their structure and ordering. Neither a child's terminal event nor any payload `runId` completes the finite interaction. Only the exact Service Run's authoritative sealed state ends `Next()` and supplies `Result()`.

Authored input appears as correlated `CUSTOM` observations such as `a13n.input.user`, `a13n.input.steering` and `a13n.input.media`, not assistant `TEXT_MESSAGE_*`. Media descriptors can carry a native kind, URL or uploaded-file identity; binary data is represented by size/media type and `payload_omitted`, not raw bytes. Generated input remains system-origin content. Preserve event `metadata`, including `display`, `media` and application references. A normal UI should omit `display: false` content, without using metadata as an access grant.

Readback Items can contain `messageId`, `subagentRunId`, structured `result_parts`, custom observation `name`/`value`, and truncation or omission markers. `Content map[string]generated.JsonValue` keeps them intact; it is not a plain last-delta text field. Persist or return these values as structured JSON when your application needs media or child attribution. Window selection, content limits and transport gaps still prevent a promise of an unabridged transcript. `DeltaFrame.Item` is required nullable on the wire; when present, its optional nullable `Ordinal`, `ResponseGroup` and JSON-map `Failure` retain native metadata independently of the raw event.
