# Waiting, approvals and external calls

A Run that returns `waiting` has not completed. Read its `Pending()` snapshot before deciding what the Service needs. There are **two arrays**, not a generic list: `Approvals` authorizes or denies server-side work; `Calls` needs a result from a client tool, a human-operated tool or a built-in question. Each item has a `ToolCallId`, `ToolName` and arguments. Display them to an authorized person or dispatch them only to a known tool. With `a13n`, `generated`, `encoding/json`, `fmt` and `io` imported:

```go
func showPending(result a13n.RunOutcome, out io.Writer) (generated.Pending, error) {
    if result.Status() != generated.RunStatusWaiting {
        return generated.Pending{}, fmt.Errorf("run %s is %s, not waiting", result.Run.ID, result.Status())
    }
    pending, err := result.Pending().Get()
    if err != nil { return generated.Pending{}, err }
    if err := json.NewEncoder(out).Encode(pending); err != nil { return generated.Pending{}, err }
    return pending, nil
}
```

## Submit a reviewer's decision

The reviewer supplies `approved` and `reason`; this helper does not decide for them. For clarity it accepts *exactly one* approval and no calls. An actual multi-item wait needs a decision or result for **every** ID in the respective arrays. Import `context`, `crypto/rand`, `fmt`, `a13n`, `generated` and `nullable`:

```go
func resumeSingleApproval(ctx context.Context, waiting a13n.RunOutcome, approved bool, reason string) (a13n.RunOutcome, error) {
    if waiting.Status() != generated.RunStatusWaiting {
        return a13n.RunOutcome{}, fmt.Errorf("run %s is not waiting", waiting.Run.ID)
    }
    pending, err := waiting.Pending().Get()
    if err != nil { return a13n.RunOutcome{}, err }
    if len(pending.Approvals) != 1 || len(pending.Calls) != 0 {
        return a13n.RunOutcome{}, fmt.Errorf("expected one approval and no external calls")
    }
    id := pending.Approvals[0].ToolCallId
    var decision generated.ApprovalDecision
    if approved {
        err = decision.FromApprove(generated.Approve{Action: generated.ApproveActionApprove})
    } else {
        err = decision.FromDeny(generated.Deny{
            Action: generated.DenyActionDeny, Reason: nullable.NewNullableWithValue(reason),
        })
    }
    if err != nil { return a13n.RunOutcome{}, err }
    successor, _, err := waiting.Run.Resume(ctx, generated.Resume{
        Approvals: map[string]generated.ApprovalDecision{id: decision},
        Calls: map[string]generated.CallResult{},
    }, rand.Text())
    if err != nil { return a13n.RunOutcome{}, err }
    return successor.Wait(ctx)
}
```

The returned outcome is a **different Run**. After the reviewer decides, display its committed items only if it completed (imports: `context`, `encoding/json`, `fmt`, `io`, `a13n`, `generated`):

```go
func displayReviewedResult(ctx context.Context, waiting a13n.RunOutcome, approved bool, reason string, out io.Writer) error {
    resumed, err := resumeSingleApproval(ctx, waiting, approved, reason)
    if err != nil { return err }
    if resumed.Status() != generated.RunStatusCompleted {
        return fmt.Errorf("successor run %s ended %s", resumed.Run.ID, resumed.Status())
    }
    items, err := resumed.Run.Items(ctx)
    if err != nil { return err }
    return json.NewEncoder(out).Encode(items.Value.Items)
}
```

The SDK passes your complete batch unchanged. Service checks the waiting Run is the exact last sealed Run on an idle Thread and all pending IDs are covered; an expired decision conflicts rather than approving a different wait.

`Resume` is not an all-status retry endpoint. After a completed, failed or cancelled Run, use an explicit ordinary `Agent.Send` to continue its checkpoint history instead. The SDK does not automatically resubmit input or fork.

## Complete a known client tool and add user input atomically

For a `Calls` item, check its name and validate arguments against your *own* allowed tool implementation before executing it. This example accepts exactly one call named `echo_text` and no approvals, then sends its result **together with** an optional ordinary user message. Import `context`, `crypto/rand`, `fmt`, `a13n`, `generated` and `nullable`:

```go
func resumeEcho(ctx context.Context, waiting a13n.RunOutcome, additionalText string) (a13n.RunOutcome, error) {
    if waiting.Status() != generated.RunStatusWaiting {
        return a13n.RunOutcome{}, fmt.Errorf("run %s is not waiting", waiting.Run.ID)
    }
    pending, err := waiting.Pending().Get()
    if err != nil { return a13n.RunOutcome{}, err }
    if len(pending.Approvals) != 0 || len(pending.Calls) != 1 || pending.Calls[0].ToolName != "echo_text" {
        return a13n.RunOutcome{}, fmt.Errorf("expected one echo_text call and no approvals")
    }
    text, ok := pending.Calls[0].Arguments["text"].(string)
    if !ok || text == "" { return a13n.RunOutcome{}, fmt.Errorf("echo_text requires nonempty text") }
    var result generated.CallResult
    err = result.FromReturned(generated.Returned{
        Status: generated.ReturnedStatusReturned,
        Value: map[string]string{"text": text},
    })
    if err != nil { return a13n.RunOutcome{}, err }
    request := generated.Resume{
        Approvals: map[string]generated.ApprovalDecision{},
        Calls: map[string]generated.CallResult{pending.Calls[0].ToolCallId: result},
    }
    if additionalText != "" {
        request.Input = nullable.NewNullableWithValue(a13n.TextPayload(additionalText))
    }
    successor, _, err := waiting.Run.Resume(ctx, request, rand.Text())
    if err != nil { return a13n.RunOutcome{}, err }
    return successor.Wait(ctx)
}
```

For another tool, use its allowlisted name, validate its schema, and return its *actual* JSON-compatible result. A tool failure is `CallResult.FromFailed(generated.Failed{Status: generated.FailedStatusFailed, Message: "…"})`, not a returned business object with an `error` field. `Resume.Input` accepts a typed `generated.MessagePayload` (text, JSON, Asset or URL parts) **in the same resume request** as all required decisions/results; it is *additional* user content, not a substitute for a missing call result or an approval. There is no separate `Send` needed to attach that input to this successor. Save the resume request key and body to reconcile an unknown outcome instead of issuing a different answer.

## Reply to a built-in question

A built-in `ask_user_question` also appears in `Pending.Calls`. Render its arguments to the person, collect their answer, then submit it as a **returned call value on the exact waiting Run**. It is not an ordinary `Agent.Send`, which remains queued while the Run waits. The native answer value has an `answers` map keyed by question text and, optionally, a general `response`. Build that JSON value from the person's selections, put it in a `generated.Returned` and fill `Resume.Calls` by `ToolCallId` as above. The Service checks it against the question's original arguments. Intentional skipping uses `CallResult.FromFailed` with a meaningful message. For mixed or multiple pending items, supply **both complete maps**; omitted items have no default. Read the [Service Run contract](../contract/semantics/runs.md#waiting-interrupt-and-fork) for exact answer semantics.
