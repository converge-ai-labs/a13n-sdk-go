# Waiting, approvals and external calls

A Run with `Status == a13n.RunWaiting` is sealed but needs explicit answers. Its direct `Pending` snapshot has **two arrays**: `Approvals` authorizes or denies server-side work; `Calls` requires a result from a client tool, human-operated tool or built-in question. Items expose `ToolCallId`, `ToolName`, JSON arguments and optional presentation. Display them to an authorized person or dispatch only to a known tool. The SDK never approves or executes tools automatically.

## Submit a reviewer's decision

The reviewer supplies `approved` and `reason`; this function does not decide for them. It accepts exactly one approval and no calls. Import `context`, `fmt` and the root SDK (`a13n`):

```go
func resumeSingleApproval(ctx context.Context, waiting a13n.RunOutcome, approved bool, reason string) (a13n.RunOutcome, error) {
    if waiting.Status != a13n.RunWaiting || waiting.Pending == nil {
        return a13n.RunOutcome{}, fmt.Errorf("run %s is not waiting for an answer", waiting.RunID)
    }
    pending := waiting.Pending
    if len(pending.Approvals) != 1 || len(pending.Calls) != 0 {
        return a13n.RunOutcome{}, fmt.Errorf("expected one approval and no external calls")
    }
    answer := a13n.Deny(a13n.Value(reason))
    if approved { answer = a13n.Approve() }
    successor, err := waiting.Run.Resume(ctx, a13n.Answers{
        pending.Approvals[0].ToolCallId: answer,
    })
    if err != nil { return a13n.RunOutcome{}, err }
    defer successor.Close()
    return successor.Result(ctx)
}
```

`Resume` returns one Interaction bound to a **distinct exact successor Run**, not the predecessor and not a fabricated Entry. It supports `Result`, `Next` and `Text` with the same finite observer. `Receipt().Origin` is `resumed_run`, `Receipt().RunID` identifies the successor and `ReceiptEvidence().Resume` retains the original HTTP response. The predecessor handle does not follow it automatically.

One public `Answers` map translates to **both** wire maps in **one** POST, including explicit empty maps. An actual mixed/multi-item wait needs an answer for every ID. Service alone checks complete atomic coverage, authority and that the waiting Run is the exact idle last Run. Omitted answers receive no defaults.

- `Approve()` authorizes one pending approval.
- `Deny()` omits the optional reason; `Deny(Null[string]())` sends null; `Deny(Value(""))` preserves an empty reason.
- `Returned(value)` supplies a call's actual JSON-compatible result. `Returned(nil)` is an explicit null result, not a missing answer.
- `Failed(message)` records a call failure. It is not a returned business object with an `error` field.

## Complete a known client tool and add user input atomically

Validate the call name and arguments against your own allowed implementation before executing it. This example accepts one `echo_text` call and no approvals, then includes optional ordinary user input in the **same** successor mutation. Imports: `context`, `fmt`, `a13n`.

```go
func resumeEcho(ctx context.Context, waiting a13n.RunOutcome, additionalText string) (a13n.RunOutcome, error) {
    if waiting.Status != a13n.RunWaiting || waiting.Pending == nil {
        return a13n.RunOutcome{}, fmt.Errorf("run %s is not waiting", waiting.RunID)
    }
    pending := waiting.Pending
    if len(pending.Approvals) != 0 || len(pending.Calls) != 1 || pending.Calls[0].ToolName != "echo_text" {
        return a13n.RunOutcome{}, fmt.Errorf("expected one echo_text call and no approvals")
    }
    text, ok := pending.Calls[0].Arguments["text"].(string)
    if !ok || text == "" { return a13n.RunOutcome{}, fmt.Errorf("echo_text requires nonempty text") }
    options := a13n.ResumeOptions{}
    if additionalText != "" {
        options.Input = a13n.Value(a13n.Input{Content: []a13n.Part{a13n.TextPart(additionalText)}})
    }
    successor, err := waiting.Run.Resume(ctx, a13n.Answers{
        pending.Calls[0].ToolCallId: a13n.Returned(map[string]string{"text": text}),
    }, options)
    if err != nil { return a13n.RunOutcome{}, err }
    defer successor.Close()
    return successor.Result(ctx)
}
```

`ResumeOptions.Input` preserves absent/null/content and accepts the same text/JSON/Asset/URL constructors. It is additional user content, not an approval or replacement for missing tool answers; no separate Send is needed. A caller deadline spans the mutation and successor observation. Local timeout, cancellation or Close never interrupts remote execution.

For durable reconciliation, save your `ResumeOptions.IdempotencyKey` and logical body before sending. Omitting the key chooses one random key once and exposes it on the returned receipt or an after-dispatch unknown-outcome error. There is no automatic mutation replay. An expired decision conflicts rather than approving a different wait.

## Reply to a built-in question

A built-in `ask_user_question` appears in `Pending.Calls`. Render its original arguments to the person and submit their answer as `Returned(value)` on the exact waiting Run. The native answer value has an `answers` map keyed by question text and optionally a general `response`. Service checks it against the original question arguments. Intentional skipping uses `Failed` with a meaningful message. Ordinary Agent Send stays queued while the Run waits and cannot answer it.

After completed, failed or cancelled Runs, use explicit ordinary `Agent.Send` to continue checkpoint history instead. Resume is not a generic retry or implicit fork. See the [pinned Run contract](../contract/semantics/runs.md#waiting-interrupt-and-fork).
