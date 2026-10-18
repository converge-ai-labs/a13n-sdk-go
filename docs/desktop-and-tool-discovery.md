# Desktop control, tool discovery and context usage

These are native Service APIs on the existing `client.API()` transport. Service owns desktop permissions, target reservations, provider access and tool discovery execution. The SDK neither launches a desktop nor renews a control lease, answers a deferred call or resumes a Run automatically.

## Configure discovery without changing selected tools

Tool search presents sources already selected by the Agent's toolsets, platform tools, Connections and plugins. It does not authorize new sources. Proxy mode exposes bounded search/call tools; native mode delegates discovery to the model provider. A provider may reject an unsupported native strategy.

The root SDK aliases canonical generated types. Import `a13n` (`github.com/converge-ai-labs/a13n-sdk-go`) for this Run override:

```go
func nativeDiscoveryOptions() (a13n.RunOptions, error) {
    var config a13n.ToolSearchConfig
    if err := config.FromNativeToolSearchConfig(a13n.NativeToolSearchConfig{
        Strategy: a13n.Null[a13n.NativeToolSearchStrategy](),
    }); err != nil { return a13n.RunOptions{}, err }
    search := a13n.ToolSearch{
        Config: &config,
        Groups: a13n.Ptr(map[string]a13n.ToolSearchGroup{}),
    }
    return a13n.RunOptions{Overrides: a13n.Value(a13n.Overrides{
        ToolSearch: a13n.Value(search),
    })}, nil
}
```

`FromNativeToolSearchConfig` supplies the native discriminator. Omit `Strategy` to omit it; `Null` sends null; `Value(a13n.NativeToolSearchStrategy("bm25"))` or `"regex"` selects a strategy. To configure proxy discovery, use `FromProxyToolSearchConfig` with `ProxyToolSearchConfig{SearchName: Ptr("find_tools"), CallName: Ptr("invoke_tool"), MaxResults: Ptr(7)}`. Invalid/missing discriminators, mixed native/proxy fields and invalid strategy values fail at the generated ToolSearch JSON boundary in both HTTP and Session packages.

An omitted/null `Overrides.ToolSearch` retains the Agent revision; an explicit ToolSearch replaces the entire block. Omitted `Groups` uses Service grouping defaults, while `Groups: Ptr(map[string]ToolSearchGroup{})` disables grouping. Nonempty groups require a description and may select platform tools, Connection IDs and plugin keys; false/empty selections remain meaningful. `ToolSearch{}` is an explicit empty block, not null. `config:null` and `groups:null` are invalid, unlike nullable native strategy.

## Observe a desktop wait, then decide explicitly

`Interaction.Result` finishes normally when its exact Run seals `waiting`, including `wait_reason="desktop_control"`. Read `outcome.Evidence().Value.WaitReason`, `outcome.Pending` and the raw evidence. User-origin desktop waiting exposes `Pending.DesktopControlId`; its approvals/calls may both be empty. No pending tool call is fabricated. Local `Close`, context cancellation and observation EOF do not complete or interrupt the durable wait.

Desktop assistance is a separate control record. Native operations cover:

- `GET /runs/{run}/desktop-assistance/{call}`: Agent-requested assistance metadata.
- `GET /runs/{run}/desktop-control`: user-control metadata or successful JSON null.
- `POST /runs/{run}/desktop-control`: open a user-origin context for a frozen mount and reason; viewing does not pause execution.
- `POST /desktop-assistance/{id}/request-control`: request durable suspension; `preparing` is not an input grant.
- `POST /desktop-assistance/{id}/view`, `/control`, `/disconnect`, `/complete`: explicit grant, handback and completion operations.

The `Control.SessionId` / `Completion.SessionId` field is a caller-managed browser-session token (20–72 URL-safe characters), **not** a Service Session resource ID. Epoch is the server's fencing value, including zero. Only access responses contain direct connection credentials; retain them in memory rather than logs. Generated raw bodies remain caller-owned; `ParseJSON` closes them. Session-authenticated generated calls pass `XWorkspaceID` explicitly.

After the person hands back control, the application can explicitly persist completion. Import `context`, `a13n` and `generated`:

```go
func completeDesktop(ctx context.Context, client *a13n.Client, controlID, browserSession string, epoch int) (generated.View, error) {
    api, err := client.API()
    if err != nil { return generated.View{}, err }
    response, err := api.CompleteAssistanceApiV1DesktopAssistanceAssistanceIdCompletePost(ctx, controlID, nil,
        generated.Completion{SessionId: browserSession, Epoch: epoch, Note: a13n.Ptr("Handed back to the agent.")})
    result, err := a13n.ParseJSON[generated.View](client, response, err, 200)
    return result.Value, err
}
```

Completion revokes connections and confirms input cleanup; it does not start a successor. Only after successful handback/reconciliation may the application explicitly call `client.Run(runID).Resume(ctx, a13n.Answers{})` for a desktop-only user pause. Mixed waits still require every native call and approval. For Agent-requested assistance, return the exact persisted assistance outcome for that call along with all other pending answers. Service validates the complete batch and stored completion transactionally; do not fabricate an outcome from the viewer state. The returned Interaction binds the explicit successor, not the original waiting Run.

Disconnect or lease expiry revokes access but leaves the task paused. Reopening a viewer never reacquires control implicitly. There is no SDK lease loop, automatic retry/reclaim, ordinary resume on completion, or automatic successor-following. See the upstream [desktop contract](https://github.com/converge-ai-labs/agent-foundation/blob/647371cdc6f1e85b9c4ccfe954999823f52f08e8/spec/a13n-service/06a-environment-management.md).

## Read native context usage

Context usage reports one latest completed generation contribution and its currently configured context window, not a pending prompt estimate. Service validates **exactly one** of Thread ID, Run ID or model-request record ID. A successful JSON null means no completed contribution; nullable token/window fields mean unavailable values, not zero. Do not fabricate totals, capacity or ratios.

Use an explicit pointer type to retain nullable success. This function returns nil without an error for JSON null:

```go
func readContextUsage(ctx context.Context, client *a13n.Client, runID string) (*generated.ContextUsage, error) {
    api, err := client.API()
    if err != nil { return nil, err }
    response, err := api.ContextUsageApiV1UsageContextGet(ctx,
        &generated.ContextUsageApiV1UsageContextGetParams{RunId: &runID})
    result, err := a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
    return result.Value, err
}
```

For other selectors, set only `ThreadId` or `RecordId` instead. A missing target is a Service error, distinct from successful null. Generated `WithResponse` calls expose the native response bytes/status as well; choose `ParseJSON[*T]` when typed nullable success and bounded decoding are useful. A non-pointer `ParseJSON[T]` continues to reject a null resource response.

Session observation remains one cursor-free connection with no default extra frame limit or automatic reconnect. A fresh connection to an already-sealed Run yields Thread state with `last_run`; read that Run's historical Items explicitly if needed. Desktop waiting and tool-search configuration use the same pinned Session schema; no UI reducer or recovery policy is added.
