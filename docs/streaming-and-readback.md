# Streaming and readback

Use `Result(ctx)` alone when you only need the final answer; it does not open an SSE connection. When a UI wants progress, call `Next()` on the **same** interaction, then read its result and committed items. This function accepts a writer for observable progress and final display; import `context`, `encoding/json`, `errors`, `fmt`, `io`, `a13n` and `generated`:

```go
func streamAndRead(ctx context.Context, interaction *a13n.Interaction, out io.Writer) error {
    defer interaction.Close()
    for {
        frame, err := interaction.Next()
        if errors.Is(err, io.EOF) { break }
        if err != nil { return err }
        switch frame.(type) {
        case a13n.GapFrame, a13n.ResetFrame:
            fmt.Fprintln(out, "Stream changed; refreshing from committed items below.")
        default:
            fmt.Fprintf(out, "Progress: %s\n", frame.EventType())
        }
    }

    result, err := interaction.Result(ctx)
    if err != nil { return err }
    if result.Status() != generated.RunStatusCompleted {
        return fmt.Errorf("run %s ended %s", result.Run.ID, result.Status())
    }
    committed, err := result.Run.Items(ctx)
    if err != nil { return err }
    fmt.Fprintf(out, "Run %s display (complete=%t, dropped=%d):\n",
        result.Run.ID, committed.Value.Complete, committed.Value.Dropped)
    return json.NewEncoder(out).Encode(committed.Value.Items)
}
```

The progress lines can be empty even when a Run completed: it may have sealed before stream attachment. Frames can be replayed within retention, may omit old history, and do not replace `Run.Items` for committed display. Check `RunItems.Complete` and `Dropped`: the display is bounded, so old items can be dropped and retained items' content can be replaced by `{"omitted": true}`. Even a sealed Run's display is not a promise of an unabridged transcript. `Next` filters frames belonging to other Runs; it also excludes Thread-wide `changed` frames. It has one active caller and returns `io.EOF` when this interaction ends, including when the SSE body goes idle after Run completion. Other errors—including a timeout or malformed stream—are returned, not treated as natural EOF.

## Deadlines and cleanup

Create a context with a deadline *before* calling `Start` or `Send`. The same observation window includes the submission, queue wait, exact Run wait and stream attachment; without a caller deadline the SDK defaults to 300 seconds. For example:

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
defer cancel()
interaction, err := client.Agent(agentID).Start(ctx, "Explain the changes.",
    a13n.StartOptions{RequestKey: rand.Text()})
if err != nil { return err }
return streamAndRead(ctx, interaction, os.Stdout)
```

This snippet also imports `context`, `crypto/rand`, `os` and `time`. `streamAndRead` closes its interaction on every path; if you do not use that helper, `defer interaction.Close()` immediately after successful submission. Close releases local reads without approving work or interrupting the remote Run. To request a *remote* interruption, call `result.Run.Interrupt(ctx)` deliberately. A shorter context passed to `Result` stops only that one caller's wait while the interaction remains open; it does not extend the invocation's original deadline.

## Recover a display after a stream gap

If you have persisted an exact Run ID, `client.Run(runID).Items(ctx)` rereads its committed display. This works after reconnection without fabricating missing deltas:

```go
items, err := client.Run(savedRunID).Items(ctx)
if err != nil { return err }
fmt.Printf("display complete=%t, dropped=%d\n", items.Value.Complete, items.Value.Dropped)
if err := json.NewEncoder(os.Stdout).Encode(items.Value.Items); err != nil { return err }
```

## Rebuild an advanced reader from committed coverage

The generated persistent reader accepts `Run` and `Position` **together**. `RunItems.Position` is the `{attempt}-{sequence}` coverage of the display; `ResumeAfter` is an optional Redis ID hint, not a second coverage cursor. Use the full snapshot, including `Dropped`, `Complete` and item truncation/omission metadata. Missing or null hints are valid; a retained, expired or incompatible hint does not change the position claim. Import `context`, `fmt`, `net/http`, `a13n` and `generated`:

```go
func rebuildReader(ctx context.Context, client *a13n.Client, runID string) (
    a13n.Result[generated.RunItems], *http.Response, error,
) {
    snapshot, err := client.Run(runID).Items(ctx)
    if err != nil { return snapshot, nil, err }
    if snapshot.Value.Run.Id != runID {
        return snapshot, nil, fmt.Errorf("items do not belong to run %s", runID)
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

Render the returned snapshot as the saved baseline before applying SSE output from the returned response. The caller owns the persistent response body and must close it (and use a context deadline); a nil response means the Run is already sealed. This advanced path does not become another finite Agent invocation or follow a successor Run. Session callers must also set `XWorkspaceID` explicitly on generated params.

`a13n.GapFrame.Position` is a `*string`: nil means transport loss with no known missing range, while a value identifies the position the replacement snapshot must cover. A gap or a boundary beyond contiguous applied output is not proof that the hole healed. Do not advance coverage or the hint past it. Refresh Items when a newer boundary or terminal state can cover that target, rather than repeatedly reading the same stale snapshot. The explicit coverage reader freezes its position and hint after a gap, reset, uncovered sequence hole or newer attempt. Later contiguous deltas cannot unfreeze it: apply an authoritative snapshot, then explicitly rebuild the reader with that snapshot's position and hint. A stale snapshot must not be treated as covering the recovery target. A `ResetFrame` means superseded provisional output must be discarded; it does not prove an empty display baseline for the new attempt. Terminal reads replace provisional output with the saved display. The SDK returns these signals without silently folding events or doing readback.

Within the default finite interaction, asking for the next frame acknowledges the preceding one. Reconnect uses only its acknowledged Redis ID, never a merely received ID. It does not automatically add Run/position query parameters or claim a display baseline. Exact-Run filtering and the finite lifecycle remain unchanged. Close does not acknowledge the last returned frame. Advanced readers that maintain their own display must opt into coverage explicitly and follow the applied-only rule. Position components are canonical nonnegative decimal integers, at most 20 digits each; a hint alone uses the older `Last-Event-ID` replay behavior and cannot establish complete delivery.

A Run's `Output()` is an optional structured value, not the last text delta; `Items` is the bounded persisted display of messages and tool activity, not a full transcript. Handle `waiting` outcomes through [waiting and tools](waiting-and-tools.md), and consult [errors and recovery](errors-and-recovery.md) if `Next` fails before a result is available. For long-lived subscriptions to all activity on a Thread, the low-level generated `ThreadStreamApiV1ThreadsThreadIdStreamGet` remains available through `client.API()` with a caller-owned response body; it is not a second high-level Agent interaction.

## Native AG-UI 1.0 content and child attribution

The Service's `delta` envelope identifies the authoritative Service Run with `run_id`, attempt and sequence. Inside it, `DeltaFrame.Event` is a raw JSON map of native AG-UI 1.0 fields: `messageId`, `toolCallId`, `threadId`, `runId`, `subagentRunId` and `parentSubagentRunId` use their canonical camelCase names. The SDK does not rename fields, convert media parts to text, invent an event alias or build a UI reducer. New native fields and required `CUSTOM.value: null` are preserved. Treat `RUN_STARTED.protocolVersion` as protocol observation, not an instruction to select another Service Run.

Inline children may reuse a parent's message/tool IDs. Keep `subagentRunId` attribution when displaying their text, reasoning, tool results and custom observations; `SUBAGENT_STARTED`/`SUBAGENT_FINISHED` may also name `parentSubagentRunId` and `parentToolCallId`. Do not concatenate child output into root text. Native `TOOL_CALL_RESULT.content` may be a string or ordered content parts, including images and repeated parts; preserve their structure and ordering. Neither a child's terminal event nor any payload `runId` completes the finite interaction. Only the exact Service Run's authoritative sealed state ends `Next()` and supplies `Result()`.

Authored input appears as correlated `CUSTOM` observations such as `a13n.input.user`, `a13n.input.steering` and `a13n.input.media`, not assistant `TEXT_MESSAGE_*`. Media descriptors can carry a native kind, URL or uploaded-file identity; binary data is represented by size/media type and `payload_omitted`, not raw bytes. Generated input remains system-origin content. Preserve event `metadata`, including `display`, `media` and application references. A normal UI should omit `display: false` content, without using metadata as an access grant.

Readback Items can contain `messageId`, `subagentRunId`, structured `result_parts`, custom observation `name`/`value`, and truncation or omission markers. `Content map[string]generated.JsonValue` keeps them intact; it is not a plain last-delta text field. Persist or return these values as structured JSON when your application needs media or child attribution. Display limits and transport gaps still prevent a promise of complete history.
