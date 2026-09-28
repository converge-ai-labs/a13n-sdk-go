# Errors and recovery

A local error is not necessarily a failed Service operation. In particular, a timeout or broken connection **after a POST** cannot tell you whether the Service accepted it. Choose and save a request key *before* submission. If `Start`/`Send` returned an interaction, also save its `Thread.ID` and `Entry.ID` for readback. No SDK method silently retries a mutation or interrupts work just because local observation stopped.

## Classify what happened locally

Use `errors.Is` / `errors.As`, not error-string matching. This function imports `context`, `errors`, `fmt` and `a13n`:

```go
func explain(err error) string {
    var entry *a13n.EntryDispositionError
    var api *a13n.ApiError
    var protocol *a13n.ProtocolError
    var transport *a13n.TransportError
    switch {
    case err == nil:
        return "no local error"
    case errors.As(err, &entry):
        return fmt.Sprintf("entry %s ended %s", entry.Entry.ID, entry.Snapshot.Value.Status)
    case errors.As(err, &api):
        return fmt.Sprintf("Service %d: %s (request %s)", api.Status, api.Code, api.RequestID)
    case errors.As(err, &protocol):
        return fmt.Sprintf("invalid Service response: %s", protocol.Kind)
    case errors.As(err, &transport):
        return fmt.Sprintf("transport: %s", transport.Kind)
    case errors.Is(err, context.DeadlineExceeded):
        return "local deadline expired; remote outcome may still be unknown"
    case errors.Is(err, a13n.ErrClosed):
        return "client was closed"
    default:
        return err.Error()
    }
}
```

A failed/withdrawn submission produces `*EntryDispositionError` with the Entry snapshot; that is different from a Run whose terminal status is `failed` or `cancelled`. `Result` returns those Run statuses as observations—check `outcome.Status()` and `outcome.Failure()` rather than treating a nil Go error as completed success. `ApiError` also carries bounded details, headers and a retry-after hint. Error messages avoid raw response bodies and transport credentials; do not log secrets separately.

## Read back a receipt before trying anything again

If you have a Thread and Entry ID, read that exact Entry. A `consumed` Entry records the Run it entered; a merely `pending` or `assigned` Entry has not yet reached that point. This function imports `context`, `fmt`, `a13n` and `generated`:

```go
func readSubmitted(ctx context.Context, client *a13n.Client, threadID, entryID string) (a13n.RunOutcome, error) {
    entry, err := client.Entry(threadID, entryID).Get(ctx)
    if err != nil { return a13n.RunOutcome{}, err }
    if entry.Value.Status != generated.EntryStatusConsumed {
        return a13n.RunOutcome{}, fmt.Errorf("entry %s is %s; check again later or inspect its failure", entryID, entry.Value.Status)
    }
    runID, err := entry.Value.AssignedRunId.Get()
    if err != nil { return a13n.RunOutcome{}, err }
    return client.Run(runID).Wait(ctx)
}
```

This reads the exact consuming Run, not a mutable "latest" pointer; its status may be `waiting`, `failed` or `cancelled`, so inspect the returned outcome before displaying a result. If `Start` or `Send` returned an interaction, prefer `interaction.Result(ctx)` while it is still open. A shorter `Result` call context only stops that call; the interaction's original deadline and `Close` still control its observation.

If the POST failed *before you got a receipt*, reconciliation needs the saved request key and original payload. After deciding to replay, call the same `Start` or `Send` with the **same** key, Agent, Thread (for Send), options and payload, not `rand.Text()` or an altered body. Service idempotency can return the original receipt; an unrelated mutation under a fresh key is not a retry. Keep the saved key outside process memory when recovery must survive a restart. See the [Service's idempotency rules](../contract/semantics/api-conventions.md) for retention/conflict details.

## Conditional writes and streams

For a `412` from a generated `IfMatch` write, reread that resource and decide whether to reapply the change against its new ETag. Do not turn a precondition failure into an unconditional overwrite. [Generated API](generated-api.md#read-and-conditionally-update-a-resource) shows the ETag flow.

If `Next()` fails because a stream is malformed or disconnected, the Run can still finish. While the interaction context is valid, you may call `Result(ctx)` and then `outcome.Run.Items(ctx)` before `Close` to recover committed display. `io.EOF` from a finite interaction is the normal end of its frame iteration, **not** a substitute for checking `Result`. A gap/reset frame likewise calls for readback, not replaying an arbitrary mutation. Closing observation never remotely interrupts the Run; `Run.Interrupt(ctx)` is a separate explicit request.
