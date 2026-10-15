# Errors and recovery

A local error is not necessarily a failed Service mutation. There are no automatic POST retries. For durable or cancellation-safe reconciliation, choose and persist an `IdempotencyKey` and identical logical request body **before** dispatch. Omitted keys are generated once and exposed on a returned Interaction or unknown-outcome error; this does not make a key recoverable if the process exits before either returns.

## Persist the returned identity receipt

The receipt is acceptance evidence, not completion. It is JSON-serializable and independent of raw response envelopes. Import `context`, `encoding/json`, `fmt`, `os` and the root SDK (`a13n`):

```go
interaction, err := client.Agent(agentID).Start(ctx, "Review the project.",
    a13n.StartOptions{IdempotencyKey: savedKey})
if err != nil { return err }
defer interaction.Close()
encoded, err := json.Marshal(interaction.Receipt())
if err != nil { return err }
if err := os.WriteFile(receiptPath, encoded, 0600); err != nil { return err }
outcome, err := interaction.Result(ctx)
if err != nil { return err }
fmt.Println(outcome.Status, outcome.Output)
```

A submitted receipt records Thread/Entry IDs, the chosen key and an optional clearly labeled `ProvisionalRunID`. Only the exact **consumed** Entry makes execution binding authoritative; assignment may revert. `interaction.Run()` does not promote a provisional receipt Run into authority.

## Recover saved Thread/Entry IDs

Recovery constructs the same lazy Interaction without I/O, a POST or a fabricated original receipt. It awaits durable consumption, then observes only that exact Run. Import `context` and `a13n`:

```go
func readSubmitted(ctx context.Context, client *a13n.Client, threadID, entryID string) (a13n.RunOutcome, error) {
    interaction, err := client.Entry(threadID, entryID).Observe(ctx)
    if err != nil { return a13n.RunOutcome{}, err }
    defer interaction.Close()
    return interaction.Result(ctx)
}
```

For a saved exact Run ID, use `client.Run(runID).Result(ctx)` or `Observe(ctx)` if raw/text iteration is wanted. Neither follows latest pointers or successors. Resume receipts identify the exact successor Run and have no invented Entry. Fresh ID recovery begins a new bounded local observation; repeated Result calls on an existing Interaction do not reset its original deadline.

`ReceiptEvidence()` optionally exposes original submitted/resumed HTTP responses. Its fields are nil for recovered identities. A shorter Result caller context only stops that caller's wait, not the Interaction; the original deadline and Close still govern observation.

## Classify local failures

Use `errors.Is` / `errors.As`, not string matching. Import `context`, `errors`, `fmt`, `a13n`:

```go
func explain(err error) string {
    var unknown *a13n.UnknownOutcomeError
    var entry *a13n.EntryDispositionError
    var api *a13n.ApiError
    var preview *a13n.PreviewInterruptedError
    switch {
    case err == nil:
        return "no local error"
    case errors.As(err, &unknown):
        return "dispatch outcome unknown; reconcile the original key and body"
    case errors.As(err, &entry):
        return fmt.Sprintf("entry ended %s", entry.Snapshot.Value.Status)
    case errors.As(err, &api):
        return fmt.Sprintf("Service %d: %s", api.Status, api.Code)
    case errors.As(err, &preview):
        return "preview interrupted; read authoritative Result"
    case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
        return "local observation stopped; remote work is not interrupted"
    case errors.Is(err, a13n.ErrClosed):
        return "client was closed"
    default:
        return err.Error()
    }
}
```

`UnknownOutcomeError` retains the chosen key, safe operation, known identities, optional HTTP status/headers and native chained cause. It covers attempted dispatch without reliable acknowledgement: transport/cancellation, lost response bodies and malformed accepted receipts. Its diagnostics omit arbitrary keys/IDs/bodies/causes. Pre-dispatch validation, serialization, cancelled context or closed client is not an unknown outcome. Known API rejection remains `ApiError` only for the complete canonical Service error envelope: string code/message, object details and a present string/null request_id. A code-only gateway JSON object remains a protocol failure, wrapped as an unknown mutation outcome after dispatch. Safe `TransportError`, `ProtocolError`, `ErrTransport`, `ErrProtocol`, `ErrClosed` and context identities remain accessible through native chaining.

If a POST lacks a reliable receipt, the application decides whether to replay with the **same** key, Agent, Thread, options, answers and content. Service may return the original receipt or an idempotency conflict. A fresh key or changed body is not reconciliation. See [pinned idempotency rules](../contract/semantics/api-conventions.md).

A failed/withdrawn Entry produces `EntryDispositionError`, distinct from an incorporated Run with `Status` failed/cancelled. Those Run states are outcomes: inspect direct `Status` and `Failure` even when Go error is nil.

## Conditional writes and optional streams

A generated `IfMatch` write returning `412` requires rereading and an application decision, not an unconditional overwrite. [Generated API](generated-api.md#read-and-conditionally-update-a-resource) shows ETag handling.

`Text` interruption is sticky for preview only. While the Interaction remains open, `Result` and deliberate sequential `Next` remain usable. Raw stream failures likewise do not establish Run failure; authoritative output is still available through Result. Advanced Items readback concerns committed display windows; it returns no stream coverage. Native Session observation uses fresh server snapshots independently of finite outcomes. `io.EOF` is normal finite frame completion, not a substitute for checking the outcome. Close cancels and joins local observation only; `Run.Interrupt(ctx)` is a separate explicit remote mutation.
