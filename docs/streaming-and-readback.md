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

If the current window's first ordinal exceeds 1, pass that ordinal as Before to request earlier display. After requests items just after an ordinal; both boundaries are exclusive. The SDK neither uses the generic cursor `Pages` iterator here nor automatically loads unbounded history. A default read includes the whole mutable tail, even beyond Limit. Every Items response contains only Run, Items and Complete: it carries no stream position, normalization continuation or retained replay hint. Complete means sealed, not all history loaded. Neither a recent nor a historical read establishes stream coverage.

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
fmt.Printf("display sealed=%t\n", items.Value.Complete)
if err := json.NewEncoder(os.Stdout).Encode(items.Value.Items); err != nil { return err }
```

## Observe a Session's native snapshots

`Client.SessionEvents` opens one advanced native connection for all Session threads, including children. It shares the Client transport, authentication, semantic workspace and cancellation lifetime. Its seven distinct `SessionFrame` variants embed the generated `sessionstream` schema models: `SessionThreadFrame`, `SessionItemsFrame`, `SessionDeltaFrame`, `SessionGapFrame`, `SessionScheduleFrame`, `SessionOccurrenceFrame` and `SessionAppReadyFrame`. App readiness follows committed Items and can repeat after reconnect or repair; track `(run_id, app_id)` without advancing delta sequence. Provisional `a13n.mcp_apps` output alone does not establish readiness. `RawJSON()` returns original bytes independently of typed decoding. Session frames have no SSE ID or cursor; reopening always starts from a fresh snapshot. An already-sealed Run appears in the thread's last-run state, without replaying its items; use Run.Items for that saved display.

This helper imports `context`, `fmt`, `io` and `a13n`. It consumes one connection and leaves reconnection to its caller:

```go
func readSession(ctx context.Context, client *a13n.Client, sessionID string, out io.Writer) error {
    stream, err := client.SessionEvents(ctx, sessionID, a13n.SessionStreamOptions{})
    if err != nil { return err }
    defer stream.Close()
    for {
        frame, err := stream.Next()
        if err != nil { return err } // io.EOF ends this connection, not a Run
        switch value := frame.(type) {
        case *a13n.SessionThreadFrame:
            fmt.Fprintln(out, "thread", value.Thread.Id)
        case *a13n.SessionItemsFrame:
            fmt.Fprintln(out, "items", value.RunId, value.First, value.Complete)
        case *a13n.SessionDeltaFrame:
            fmt.Fprintln(out, "delta", value.RunId, value.Attempt, value.Sequence)
        case *a13n.SessionGapFrame:
            fmt.Fprintln(out, "gap", value.RunId)
        }
    }
}
```

Each current Run starts with items; an items frame replaces the suffix beginning at First, while earlier ordinals are final. Its optional native Continuation belongs to that snapshot's normalization cut, not HTTP reconnect progress. A gap holds that Run's deltas until the server supplies replacement items; a completed items frame finishes that Run's display, not Session observation. This SDK exposes these native facts without assembling fragments or maintaining a display reducer. Use the finite Interaction for one Agent invocation and exact outcome.

Unknown SSE event names are ignored before decoding their payload. Known payloads are validated recursively against the bundled pinned JSON Schema before typed decoding; invalid frames terminate Next with ErrProtocol. The shared parser enforces UTF-8 and complete framing. `MaxFrameBytes` defaults to zero, imposing no SDK limit on snapshots; set a positive value to enforce your application's frame bound. Keepalive comments are consumed internally. Close or context cancellation releases local observation; it neither cancels a Run nor sends a mutation. Response retains HTTP status and cloned headers after Close.

There is no automatic reconnect or mutation retry. A persistent application's reconnect loop starts a new SessionEvents call after connection end, except a non-429 4xx refusal or invalid frame. Use full jitter below `min(30s, 0.5s * 2^n)`, honor Retry-After for 429/503, and reset n after 30 seconds open, as specified by Service. Preserve one application cancellation deadline across that loop. Never send Last-Event-ID or manufacture Session progress from a continuation.

## Keep explicit Thread progress separate

The generated persistent `ThreadStream` still accepts paired Run and Position, plus optional LastEventID. Supply only progress your application actually applied and saved; Run.Items supplies none. A position uses canonical nonnegative decimal attempt/sequence components, each at most 20 digits. A retained Redis ID optimizes replay and does not establish coverage by itself. Session normalization state is a separate protocol concept.

A Thread gap, reset, uncovered sequence hole or newer attempt freezes the internal explicitly scoped reader's position and hint. Later deltas cannot heal that claim. Read committed display for inspection, or reopen the native Session stream for server-owned snapshot repair; an Items read alone cannot authorize a new Thread coverage claim. The SDK neither infers positions from Redis IDs nor creates an empty baseline after reset. Advanced generated raw responses remain caller-owned and require explicit body closure; session-authenticated generated calls set XWorkspaceID explicitly.

Within the default finite interaction, asking for the next frame acknowledges the preceding one. Reconnect uses only its acknowledged Redis ID, never a merely received ID. It does not automatically add Run/position query parameters or claim a display baseline. Exact-Run filtering and the finite lifecycle remain unchanged. Close does not acknowledge the last returned frame. Advanced readers that maintain their own display must opt into coverage explicitly and follow the applied-only rule. Position components are canonical nonnegative decimal integers, at most 20 digits each; a hint alone uses the older `Last-Event-ID` replay behavior and cannot establish complete delivery.

A Run outcome's `Output` is an optional structured value, not the last text delta; `Items` is one window of persisted display messages and tool activity, not an automatically loaded full transcript. Handle `waiting` outcomes through [waiting and tools](waiting-and-tools.md), and consult [errors and recovery](errors-and-recovery.md) if `Next` fails before a result is available. For long-lived subscriptions to all activity on a Thread, the low-level generated `ThreadStream` remains available through `client.API()` with a caller-owned response body; it is not a second high-level Agent interaction.

## Explicitly expand saved display content

`Item.ContentRefs` maps field names to immutable saved-value references. Inline text can be only a preview; it does not replace `RunOutcome.Output`. Service keeps up to 32 KiB inline, externalizes larger fields, and caps saved values at 16 MiB. Above the cap it saves a UTF-8 text prefix, including for oversized JSON. Each reference contains media type, saved byte count, a preview of at most 4 KiB and optional `Truncated`. `RunContent.Truncated` must stay visible to the application after expansion.

This explicit read imports `context`, `a13n` and `generated`. Session callers pass their workspace ID; API-key callers pass nil:

```go
func expandSavedContent(ctx context.Context, client *a13n.Client, runID, contentID string, workspaceID *string) (
    a13n.Result[generated.RunContent], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[generated.RunContent]{}, err }
    response, err := api.GetRunContent(ctx, runID, contentID,
        &generated.GetRunContentParams{XWorkspaceID: workspaceID})
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

## Read a retained Computer image explicitly

Computer observations can carry `CUSTOM name:"a13n.tool_images"` with opaque image references and an unavailable flag; child observations retain `subagentRunId`. Saved tool-call Items expose `tool_images` and `tool_image_unavailable` in their native content map. Keep these structured fields separate from the text preview.

Use the owning Run and committed image reference for an explicit binary read. This helper imports `context`, `fmt`, `io`, `a13n` and `generated`:

```go
func copyRetainedImage(ctx context.Context, client *a13n.Client, runID, imageID string, destination io.Writer) (int64, error) {
    api, err := client.API()
    if err != nil { return 0, err }
    response, err := api.RunImage(ctx, runID, imageID,
        &generated.RunImageParams{})
    if err != nil { return 0, err }
    if response.StatusCode != 200 {
        _, err := a13n.ParseJSON[map[string]any](client, response, nil, 200)
        return 0, err
    }
    defer response.Body.Close()
    mediaType := response.Header.Get("Content-Type")
    if mediaType != "image/png" && mediaType != "image/jpeg" {
        return 0, fmt.Errorf("unexpected image media type %q", mediaType)
    }
    return io.Copy(destination, response.Body)
}
```

The raw success response is caller-owned and retains image bytes, content type and `no-store`/`nosniff` headers. Images are readable only through references reachable from the Run's committed tail/pages; a live reference may precede that boundary and receive `not_found`. Wrong-Run, unknown or private references are also unavailable. Fetch explicitly after committed readback; the SDK performs no automatic image expansion, retry or display reduction.
