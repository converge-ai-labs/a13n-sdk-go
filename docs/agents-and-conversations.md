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
