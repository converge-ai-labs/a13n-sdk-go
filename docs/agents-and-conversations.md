# Agents and conversations

The [quick start](../README.md) runs an Agent created in Console. If your application has `builder` permission, it can create one through the generated API instead. Choose a configured **model key**; the returned Agent **ID** is what execution calls use. The function below needs `context`, the root SDK import (`a13n`) and its `generated` package:

```go
func createAgent(ctx context.Context, client *a13n.Client, modelKey string) (string, error) {
    api, err := client.API()
    if err != nil { return "", err }
    instructions := "Answer clearly and cite uncertainty."
    response, err := api.CreateAgentApiV1AgentsPost(ctx,
        &generated.CreateAgentApiV1AgentsPostParams{}, generated.AgentCreate{
            Name: "Research assistant",
            Config: generated.AgentConfigInput{Model: modelKey, Instructions: &instructions},
        })
    created, err := a13n.ParseJSON[generated.Agent](client, response, err, 201)
    if err != nil { return "", err }
    return created.Value.Id, nil
}
```

Keep the Agent ID returned here (or copied from Console). Binding it with `client.Agent(agentID)` is local; the Service still checks permissions on requests.

## Start and inspect the first turn

A context bounds submission, queueing and execution together. You can skip streaming entirely and read the Run's committed items after `Result`. This example imports `context`, `crypto/rand`, `fmt`, `time`, `a13n` and `generated`:

```go
func firstTurn(client *a13n.Client, agentID string) (string, generated.RunItems, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()
    interaction, err := client.Agent(agentID).Start(ctx, "Explain the main trade-off.",
        a13n.StartOptions{RequestKey: rand.Text()})
    if err != nil { return "", generated.RunItems{}, err }
    defer interaction.Close()

    result, err := interaction.Result(ctx)
    if err != nil { return "", generated.RunItems{}, err }
    if result.Status() != generated.RunStatusCompleted {
        return interaction.Thread.ID, generated.RunItems{}, fmt.Errorf("run %s ended %s", result.Run.ID, result.Status())
    }
    items, err := result.Run.Items(ctx)
    if err != nil { return interaction.Thread.ID, generated.RunItems{}, err }
    return interaction.Thread.ID, items.Value, nil
}
```

Display `items.Items` according to the item's kind; `items.Run` identifies the Run they belong to. Save the returned Thread ID for follow-up. `result.Output()` is optional—even a completed conversation can have committed messages without it. If the result is `waiting`, use [waiting and tools](waiting-and-tools.md) instead of presenting it as finished.

## Start from an imported conversation

If another Pydantic AI application already completed a conversation, pass its public `ModelMessage` JSON objects on **new Thread creation**. This is not a prompt string or Harness checkpoint. The Service accepts completed user/model text and closed tool-call/JSON-result exchanges; it validates the actual format and limits (at most 256 messages and 256 KiB normalized JSON). The SDK intentionally forwards JSON objects without inventing another Pydantic AI type hierarchy or filtering content locally. This example imports `context`, `crypto/rand`, `fmt`, `a13n` and `generated`:

```go
func importConversation(ctx context.Context, client *a13n.Client, agentID string) (string, a13n.RunOutcome, error) {
    history := generated.MessageHistory{
        {"kind": "request", "parts": []map[string]any{{"part_kind": "user-prompt", "content": "What changed?"}}},
        {"kind": "response", "parts": []map[string]any{{"part_kind": "tool-call", "tool_name": "lookup", "tool_call_id": "call-1", "args": map[string]any{"key": "x"}}}},
        {"kind": "request", "parts": []map[string]any{{"part_kind": "tool-return", "tool_name": "lookup", "tool_call_id": "call-1", "content": map[string]any{"value": "ok"}}}},
        {"kind": "response", "parts": []map[string]any{{"part_kind": "text", "content": "The API changed."}}},
    }
    interaction, err := client.Agent(agentID).Start(ctx, "Continue from this history.",
        a13n.StartOptions{RequestKey: rand.Text(), MessageHistory: &history})
    if err != nil { return "", a13n.RunOutcome{}, err }
    defer interaction.Close()
    view, err := interaction.Thread.Get(ctx)
    if err != nil { return "", a13n.RunOutcome{}, err }
    if len(view.Value.MessageHistory) != len(history) {
        return "", a13n.RunOutcome{}, fmt.Errorf("imported history length changed")
    }
    result, err := interaction.Result(ctx)
    return interaction.Thread.ID, result, err
}
```

Inspect `result.Status()` and `result.Run.Items(ctx)` before presenting an answer. `Thread.Get(ctx).Value.MessageHistory` reads back the immutable seed; it is not an unbounded transcript of later turns. Omit `MessageHistory` to start empty, or pass a pointer to an empty slice to send explicit `[]`. Import is only available on `Start`/`StartPayload`, not on `Send`, `Resume` or fork; later turns reuse the Thread's context without reseeding it. Use the **same** history and request key to replay an uncertain create. Invalid/oversized history is rejected by Service; don't truncate it silently on the client.

## Continue a saved Thread

Use an explicitly chosen Agent again; a Thread does not have a permanent Agent. Use a *new* context and request key for a new message:

```go
func continueTurn(ctx context.Context, client *a13n.Client, agentID, threadID string) (generated.RunItems, error) {
    next, err := client.Agent(agentID).Send(ctx, threadID, "What would you do next?",
        a13n.SendOptions{RequestKey: rand.Text()})
    if err != nil { return generated.RunItems{}, err }
    defer next.Close()
    result, err := next.Result(ctx)
    if err != nil { return generated.RunItems{}, err }
    if result.Status() != generated.RunStatusCompleted {
        return generated.RunItems{}, fmt.Errorf("follow-up run %s ended %s", result.Run.ID, result.Status())
    }
    items, err := result.Run.Items(ctx)
    if err != nil { return generated.RunItems{}, err }
    return items.Value, nil
}
```

Import `context`, `crypto/rand`, `fmt`, `a13n` and `generated` for this function. `StartPayload` and `SendPayload` accept a typed `generated.MessagePayload` when text alone is insufficient; [files and Memory](files-and-memory.md) shows an Asset part. `StartOptions` also exposes revision/session choice, delivery, environment/Memory mounts, MCP headers and typed Run options; `SendOptions` exposes the continuation fields. The SDK does not silently select a model, Agent or successor Run for you.

A `RequestKey` belongs to *one logical mutation*. Generating another key retries as a new submission, not an idempotent replay. If an earlier request's outcome is unknown, save its key and reconcile it as described in [errors and recovery](errors-and-recovery.md).

## Supply one native Run configuration snapshot

`StartOptions.Options` and `SendOptions.Options` use the same generated `RunOptionsInput` as a low-level `generated.Message`. Its `Configuration` is distinct from `Overrides`: it selects the accepted Run's network rules and namespaced JSON extensions, not another Agent definition or provider settings object. There is no second configuration argument or local merge. Import `context`, `crypto/rand`, `a13n`, `generated` and `nullable`:

```go
func startConfigured(ctx context.Context, client *a13n.Client, agentID string) (*a13n.Interaction, error) {
    extensions := map[string]generated.JsonValue{
        "myapp.review": map[string]any{"enabled": false, "tags": []string{}, "note": nil},
    }
    configuration := generated.RunConfigurationInput{
        AllowedHosts: nullable.NewNullableWithValue([]string{"api.example.com"}),
        Extensions: &extensions,
    }
    return client.Agent(agentID).StartPayload(ctx, a13n.TextPayload("Review the project."), a13n.StartOptions{
        RequestKey: rand.Text(),
        Options: &generated.RunOptionsInput{
            Configuration: nullable.NewNullableWithValue(configuration),
        },
    })
}
```

Close the returned interaction; `Result(ctx).Snapshot.Value.Options.Configuration` is the accepted readback. Service normalizes hostname rules and freezes the snapshot. Choose destinations needed by the selected model and its owned input/HTTP routes, not merely the example hostname. The SDK does not enforce TLS, DNS, provider eligibility or media budgets.

- Leave `Configuration` zero-valued to omit it, or use `nullable.NewNullNullable[generated.RunConfigurationInput]()` to send null. Both select the default for a new Run and retain its snapshot when steering against an active Run (`accepted` or `running`).
- `nullable.NewNullableWithValue(generated.RunConfigurationInput{})` sends an explicit complete empty object. It does not merge old fields or hidden defaults.
- `AllowedHosts: nullable.NewNullNullable[[]string]()` explicitly removes a restriction for a new Run; `nullable.NewNullableWithValue([]string{})` sends an empty array that denies every destination. An omitted field inside an explicit object follows that object's Service defaults, not the active Run's value.
- A pointer to an empty `Extensions` map sends `{}`. False, zero, empty arrays/objects and nested null values are kept as JSON. Each namespace's consumer owns its meaning and validation.

Use the same `Options` field on `SendPayload`. A `steer` or pending-entry edit against an **active Run** (`accepted` or `running`) may omit configuration or explicitly match its frozen snapshot; a different explicit value fails with `conflict` reason `run_configuration_immutable`, rather than becoming a later Run. A sealed `waiting` Run is not active merely because it needs resume; its status alone does not imply this conflict. Set `delivery := generated.NextRun` and pass `Delivery: &delivery` to select a new snapshot for a later Run. Recovery, handoff and explicit resume successors inherit the frozen configuration; resume input does not change it. See the [pinned configuration contract](../contract/semantics/runs.md#run-configuration) for canonical idempotency and child inheritance.
