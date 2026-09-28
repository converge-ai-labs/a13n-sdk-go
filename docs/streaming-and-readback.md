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

A Run's `Output()` is an optional structured value, not the last text delta; `Items` is the bounded persisted display of messages and tool activity, not a full transcript. Handle `waiting` outcomes through [waiting and tools](waiting-and-tools.md), and consult [errors and recovery](errors-and-recovery.md) if `Next` fails before a result is available. For long-lived subscriptions to all activity on a Thread, the low-level generated `ThreadStreamApiV1ThreadsThreadIdStreamGet` remains available through `client.API()` with a caller-owned response body; it is not a second high-level Agent interaction.
