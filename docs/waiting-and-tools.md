# Waiting, approvals and client tools

A Run can stop at `waiting` with pending requests. This is not a completed answer or permission for the SDK to approve anything. Inspect what the Service actually asked before choosing the next action. With `a13n`, `generated`, `encoding/json`, `fmt` and `io` imported:

```go
func showPending(result a13n.RunOutcome, out io.Writer) ([]generated.PendingItem, error) {
    if result.Status() != generated.RunStatusWaiting {
        return nil, fmt.Errorf("run %s is %s, not waiting", result.Run.ID, result.Status())
    }
    pending, err := result.Pending().Get()
    if err != nil { return nil, err }
    if err := json.NewEncoder(out).Encode(pending.Items); err != nil { return nil, err }
    return pending.Items, nil
}
```

The printed items carry a `kind`, `tool_call_id`, `tool_name`, arguments and optional presentation. Show the relevant information to an authorized reviewer or your client-tool implementation; do not assume `Items[0]` exists or that every kind accepts the same answer. `PendingKindApproval` accepts **approve or reject**, `PendingKindClientTool` accepts **complete**, and a question-only `PendingKindUserInput` wait continues with an ordinary message rather than a structured answer.

## Submit a reviewer's approval decision

This helper takes the decision from your application's reviewer workflow; it does **not** decide for them. It deliberately handles only one approval item. If several requests are pending, collect a decision for **each** before constructing one answer batch: omitted approvals are treated as rejections by the Service. Import `context`, `crypto/rand`, `fmt`, `a13n`, `generated` and `nullable`:

```go
func resumeSingleApproval(ctx context.Context, waiting a13n.RunOutcome, approved bool, reason string) (a13n.RunOutcome, error) {
    if waiting.Status() != generated.RunStatusWaiting {
        return a13n.RunOutcome{}, fmt.Errorf("run %s is not waiting", waiting.Run.ID)
    }
    pending, err := waiting.Pending().Get()
    if err != nil { return a13n.RunOutcome{}, err }
    if len(pending.Items) != 1 || pending.Items[0].Kind != generated.PendingKindApproval {
        return a13n.RunOutcome{}, fmt.Errorf("expected exactly one approval request")
    }
    callID := pending.Items[0].ToolCallId
    var answer generated.Answer
    if approved {
        err = answer.FromApprove(generated.Approve{Action: generated.ApproveActionApprove, ToolCallId: callID})
    } else {
        err = answer.FromReject(generated.Reject{
            Action: generated.RejectActionReject, ToolCallId: callID,
            Reason: nullable.NewNullableWithValue(reason),
        })
    }
    if err != nil { return a13n.RunOutcome{}, err }
    answers := []generated.Answer{answer}
    successor, _, err := waiting.Run.Resume(ctx, generated.ResumeRequest{Answers: &answers}, rand.Text())
    if err != nil { return a13n.RunOutcome{}, err }
    return successor.Wait(ctx)
}
```

The returned outcome belongs to a **different** Run. For example, after your reviewer supplies `approved` and `reason`, display that successor's committed messages (imports: `context`, `encoding/json`, `fmt`, `io`, `a13n`, `generated`):

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

Do not label a `waiting`/`failed`/`cancelled` successor as completed. Resume answers must target the exact waiting head; stale decisions can be rejected after another actor changes it. Keep the request key for reconciliation if the resume response is lost.

## Complete a client tool with a real result

The client tool implementation supplies its computed result; do not send a fabricated approval-shaped value. This helper requires exactly one `client_tool` item **with the expected tool name**. Its `invoke` callback must validate the actual arguments before performing work and return a JSON-compatible result. It imports `context`, `crypto/rand`, `fmt`, `a13n` and `generated`:

```go
func resumeSingleClientTool(ctx context.Context, waiting a13n.RunOutcome, expectedToolName string, invoke func(context.Context, map[string]generated.JsonValue) (generated.JsonValue, error)) (a13n.RunOutcome, error) {
    if waiting.Status() != generated.RunStatusWaiting {
        return a13n.RunOutcome{}, fmt.Errorf("run %s is not waiting", waiting.Run.ID)
    }
    pending, err := waiting.Pending().Get()
    if err != nil { return a13n.RunOutcome{}, err }
    if len(pending.Items) != 1 || pending.Items[0].Kind != generated.PendingKindClientTool || pending.Items[0].ToolName != expectedToolName {
        return a13n.RunOutcome{}, fmt.Errorf("expected exactly one %q client tool", expectedToolName)
    }
    toolResult, err := invoke(ctx, pending.Items[0].Arguments)
    if err != nil { return a13n.RunOutcome{}, err }
    var answer generated.Answer
    err = answer.FromComplete(generated.Complete{
        Action: generated.CompleteActionComplete,
        ToolCallId: pending.Items[0].ToolCallId,
        Result: toolResult,
    })
    if err != nil { return a13n.RunOutcome{}, err }
    answers := []generated.Answer{answer}
    successor, _, err := waiting.Run.Resume(ctx, generated.ResumeRequest{Answers: &answers}, rand.Text())
    if err != nil { return a13n.RunOutcome{}, err }
    return successor.Wait(ctx)
}
```

For example, if the Agent explicitly defines a client tool named `echo_text`, this implementation only accepts a string `text` argument and returns that validated value (imports: `context`, `fmt`, `a13n`, `generated`):

```go
func echoText(ctx context.Context, args map[string]generated.JsonValue) (generated.JsonValue, error) {
    text, ok := args["text"].(string)
    if !ok || text == "" { return nil, fmt.Errorf("echo_text requires a nonempty text argument") }
    return map[string]string{"text": text}, nil
}

func resumeEcho(ctx context.Context, waiting a13n.RunOutcome) (a13n.RunOutcome, error) {
    return resumeSingleClientTool(ctx, waiting, "echo_text", echoText)
}
```

Substitute your own allowlisted tool name and schema-checked implementation; do not pass arbitrary `toolResult` or execute based solely on an untrusted `tool_name`. A resumed result may wait again, so inspect its status and `Run.Items` before showing success.

For a **question-only** wait (every pending item is `user_input`), submit the person's text with `client.Agent(agentID).Send(ctx, threadID, text, a13n.SendOptions{RequestKey: rand.Text()})` and read that interaction's `Result(ctx)`. Ordinary text does *not* grant an approval or complete a client tool. Multiple kinds and multiple items need application-specific routing; use the [Service's Run contract](../contract/semantics/runs.md) rather than guessing a default answer.
