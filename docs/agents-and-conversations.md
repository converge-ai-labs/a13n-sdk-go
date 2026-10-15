# Agents and conversations

The [quick start](../README.md) runs an existing Agent. Bind the Agent ID copied from Console with `client.Agent(agentID)`; binding is local, and Service still checks authorization on each request. Common workflow examples need only `context`, `fmt`, and the root SDK import (`a13n`).

## Start and read the first reply

```go
func firstTurn(ctx context.Context, client *a13n.Client, agentID string) (a13n.RunOutcome, error) {
    interaction, err := client.Agent(agentID).Start(ctx, "Explain the main trade-off.")
    if err != nil { return a13n.RunOutcome{}, err }
    defer interaction.Close()
    return interaction.Result(ctx)
}
```

Supply a bounded context; without a caller deadline the SDK uses 300 seconds total. `Result` does not require streaming or Items reads. Inspect direct `Status`, `Output`, `Pending`, `Failure`, `ThreadID`, `RunID` and the exact `Run` handle. `Text()` returns `(string, bool)` only for string output, preserving an empty string. False, zero, null and empty containers remain output values. Waiting, failed and cancelled are explicit outcomes, not completed replies.

JSON numbers in high-level output are `json.Number`. Decode the original JSON into an application type without a float64 roundtrip:

```go
answer, err := a13n.DecodeOutput[struct {
    Count int64 `json:"count"`
    Summary string `json:"summary"`
}](outcome)
if err != nil { return err }
fmt.Println(answer.Count, answer.Summary)
```

`outcome.Evidence()` exposes the original typed Run response, HTTP status, headers and request ID. Advanced `Run.Items` reads committed display windows, not the final reply or an automatically complete transcript.

## Continue a saved Thread

A Thread has no permanent Agent. Choose one explicitly and use a fresh bounded context:

```go
func continueTurn(ctx context.Context, client *a13n.Client, agentID, threadID string) (a13n.RunOutcome, error) {
    next, err := client.Agent(agentID).Send(ctx, threadID, "What would you do next?")
    if err != nil { return a13n.RunOutcome{}, err }
    defer next.Close()
    return next.Result(ctx)
}
```

Use ordinary `Send` after completed, failed or cancelled Runs to continue checkpoint history. Failure/cancellation pauses automatic progress, not history; the SDK does not retry execution, choose an earlier successful Run or fork implicitly. A waiting last Run instead requires explicit [resume](waiting-and-tools.md).

## Submit structured or multipart input

Constructors own wire discriminators and preserve ordering and duplicate parts:

```go
input := a13n.Input{Content: []a13n.Part{
    a13n.TextPart("Compare these inputs."),
    a13n.JSONPart(map[string]any{"enabled": false, "threshold": 0, "note": nil}),
    a13n.URLPart("https://example.com/report.pdf"),
    a13n.AssetPart(assetID),
}}
interaction, err := client.Agent(agentID).StartInput(ctx, input)
if err != nil { return err }
defer interaction.Close()
result, err := interaction.Result(ctx)
if err != nil { return err }
fmt.Println(result.Status, result.Output)
```

`SendInput(ctx, threadID, input, ...SendOptions)` provides the same content choices. Service owns media eligibility, materialization, content bounds and validation. `Input.Wire()` / `InputFromWire()` are explicit advanced conversions; common applications do not need them.

## Supply one native Run configuration snapshot

`StartOptions.Options` and `SendOptions.Options` share public `RunOptions`. `Configuration` selects Run network rules and namespaced JSON extensions; `Overrides` changes selectable Agent fields. They are distinct and are not locally merged. All existing selectable fields remain available through selected canonical aliases, including uncommon client tools, connections, plugins, skills, subagents, reviewer, retry and model settings.

```go
var skillSync a13n.SkillSync
if err := skillSync.FromSharedSkillSync(a13n.SharedSkillSync{Overwrite: a13n.Ptr(false)}); err != nil { return err }
options := a13n.StartOptions{
    AgentRevisionId: a13n.Value("revision-id"),
    Options: &a13n.RunOptions{
        Configuration: a13n.Value(a13n.Configuration{
            AllowedHosts: a13n.Value([]string{"api.example.com"}),
            Extensions: a13n.Ptr(map[string]any{
                "myapp.review": map[string]any{"enabled": false, "tags": []string{}, "note": nil},
            }),
        }),
        Overrides: a13n.Value(a13n.Overrides{
            ModelSettings: a13n.Value(map[string]any{"extra_body": map[string]any{}}),
            Plugins: a13n.Value([]a13n.PluginSelection{}),
            SkillSync: a13n.Value(skillSync),
            LazyEnvironment: a13n.Value(false),
        }),
        Labels: a13n.Ptr(map[string]string{"application": "review"}),
    },
}
interaction, err := client.Agent(agentID).Start(ctx, "Review the project.", options)
if err != nil { return err }
defer interaction.Close()
```

`Optional[T]` is the canonical nullable value. Zero value omits a field; `Null[T]()` sends explicit null; `Value(v)` sends a value, including empty objects/arrays. Pointer-valued fields use nil for omission and `Ptr` for explicit zero/false/empty. Constructors neither fill defaults nor remove empty values. `SkillSync` has native `FromSharedSkillSync` and `FromIsolatedSkillSync` union helpers. Service defaults to shared mutable publication (`overwrite=true`, `cleanup=false`); an explicit Run policy replaces the revision's whole block. Isolated `CacheCount: Ptr(0)` disables trimming, not caching. `LazyEnvironment: Value(false)` requests eager preparation; absence/null inherit the revision.

- Omitted/null configuration selects the default for a new Run and retains the snapshot when steering against an active Run (`accepted` or `running`).
- `Value(Configuration{})` selects a complete empty snapshot, not a merge.
- `AllowedHosts: Null[[]string]()` removes a restriction for a new Run; `Value([]string{})` denies every destination.
- Empty extension/model-settings maps are meaningful JSON objects. Service owns normalization, namespace validation and whole-object replacement.

Choose destinations required by your model and input routes, not simply the example hostname. A different explicit configuration against an active Run fails with `conflict` reason `run_configuration_immutable`. To choose a later snapshot, use `Delivery: a13n.Ptr(a13n.Delivery("next_run"))`. A sealed waiting Run is not active; explicit resume successors inherit its frozen configuration. See the [pinned contract](../contract/semantics/runs.md#run-configuration).

Start options also expose session, environment/Memory mounts and MCP headers. Send exposes its continuation selectors. At most one options value is accepted; omitted submission keys are generated once. For durable unknown-outcome recovery, persist a caller `IdempotencyKey` and identical logical body before sending; see [errors and recovery](errors-and-recovery.md).

## Start from an imported conversation

Only new Thread creation accepts completed Pydantic AI `ModelMessage` JSON objects. `MessageHistory` is `[]json.RawMessage`; import `encoding/json` to retain nested key order and number tokens from native exports. Go maps sort keys when marshaled, which can change model-visible tool argument/result strings. Service validates the native format and bounds (256 messages, 256 KiB normalized JSON); the SDK forwards the seed without constructing another message hierarchy.

```go
history := a13n.MessageHistory{
    json.RawMessage(`{"kind":"request","parts":[{"part_kind":"user-prompt","content":"What changed?"}]}`),
    json.RawMessage(`{"kind":"response","parts":[{"part_kind":"text","content":"The API changed."}]}`),
}
interaction, err := client.Agent(agentID).Start(ctx, "Continue from this history.",
    a13n.StartOptions{MessageHistory: &history})
if err != nil { return err }
defer interaction.Close()
result, err := interaction.Result(ctx)
if err != nil { return err }
fmt.Println(result.Output)
```

The immutable seed is submit-only: Thread.Get does not return MessageHistory. Retain the original export in your application when it is needed for reconciliation. Omit the pointer to omit history; point to an empty slice for explicit `[]`. Send and resume cannot re-import it. Replay an uncertain create only with the original history/key/body; do not truncate oversized input silently.

## Advanced: create an Agent through the low-level API

An application with `builder` permission may create an Agent instead of using Console. This separate administrative task imports `generated`; execution examples above do not. Choose a configured model **key**, then use the returned Agent **ID**:

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
