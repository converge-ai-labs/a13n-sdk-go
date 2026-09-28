# Generated API, CAS and pagination

The Agent workflow handles conversation lifecycle. For configuration, administration, Memory, Models, raw SSE and every other pinned route, call `client.API()`. The generated methods share the client's authentication, transport and cancellation lifetime; there is no separate resource-tree API or reason to assemble URLs yourself. The [pinned OpenAPI](../contract/openapi.json) lists fields and statuses, and [provenance](../contract/README.md) identifies the source commit.

## Read and conditionally update a resource

This example reads an Agent, uses its returned ETag, and returns the updated view. It requires a key with `builder` permission; import `context`, `a13n`, `generated` and `nullable`:

```go
func updateDescription(ctx context.Context, client *a13n.Client, agentID, text string) (generated.Agent, error) {
    api, err := client.API()
    if err != nil { return generated.Agent{}, err }
    response, err := api.GetAgentApiV1AgentsAgentIdGet(ctx, agentID,
        &generated.GetAgentApiV1AgentsAgentIdGetParams{})
    current, err := a13n.ParseJSON[generated.Agent](client, response, err, 200)
    if err != nil { return generated.Agent{}, err }

    etag := current.ETag()
    response, err = api.UpdateAgentApiV1AgentsAgentIdPatch(ctx, agentID,
        &generated.UpdateAgentApiV1AgentsAgentIdPatchParams{IfMatch: &etag},
        generated.AgentUpdate{Description: nullable.NewNullableWithValue(text)})
    updated, err := a13n.ParseJSON[generated.Agent](client, response, err, 200)
    if err != nil { return generated.Agent{}, err }
    return updated.Value, nil
}
```

The returned `generated.Agent` is the value saved by Service. On a `412`, someone changed it: inspect `*a13n.ApiError`, fetch the new ETag, reconcile the intended change and only then submit another conditional write. `nullable.Nullable[T]` makes three wire choices: leave a field unset to omit it, `nullable.NewNullNullable[T]()` to send JSON null, and `nullable.NewNullableWithValue(value)` to send a value. Null has field-specific meaning; it does not always clear a value. Model selectors use **keys**, while Agent/Skill selectors use **IDs**.

`ParseJSON[T]` checks the accepted status, closes the raw response body, bounds buffered JSON and retains `StatusCode`, `Header`, `ETag()` and `RequestID()` on the result. A generated raw call without `ParseJSON` returns a caller-owned `*http.Response`; close its body. Generated `WithResponse` methods offer status-specific response structs under their own buffering contract. For binary and SSE, keep the body streaming instead of decoding it as JSON.

## Page through a list

`a13n.Pages` wraps a caller-supplied generated list operation. This example imports `context`, `a13n` and `generated`, and returns the actual Thread views:

```go
func listThreads(ctx context.Context, client *a13n.Client) ([]generated.ThreadView, error) {
    api, err := client.API()
    if err != nil { return nil, err }
    pages := a13n.Pages(ctx, "", func(ctx context.Context, cursor *string) (a13n.Result[generated.ThreadPage], error) {
        response, err := api.ListThreadsApiV1ThreadsGet(ctx,
            &generated.ListThreadsApiV1ThreadsGetParams{Cursor: cursor})
        return a13n.ParseJSON[generated.ThreadPage](client, response, err, 200)
    }, func(page generated.ThreadPage) string { return page.NextCursor.GetOrEmpty() })
    var threads []generated.ThreadView
    for page, err := range pages {
        if err != nil { return nil, err }
        threads = append(threads, page.Value.Items...)
    }
    return threads, nil
}
```

`Pages` requests only the next page when iteration advances. Breaking out stops more requests; a repeated cursor is a protocol error. Each page still carries its HTTP metadata. For filters (label, limit, path), capture a stable value in the fetch closure and change only `Cursor`. Memory revisions use the same pattern and expose numeric `Seq`; see [files and Memory](files-and-memory.md#read-and-edit-files-safely).

## Change model settings for one Run

Run options are typed, but the SDK does not guess provider policy. This example sends an **empty** `extra_body` to clear the inherited object for this Run; import `context`, `crypto/rand`, `a13n`, `generated` and `nullable`:

```go
func runWithClearedExtraBody(ctx context.Context, client *a13n.Client, agentID string) (a13n.RunOutcome, error) {
    settings := map[string]generated.JsonValue{"extra_body": map[string]any{}}
    override := generated.AgentOverrideInput{ModelSettings: nullable.NewNullableWithValue(settings)}
    interaction, err := client.Agent(agentID).Start(ctx, "Test this configuration.", a13n.StartOptions{
        RequestKey: rand.Text(),
        Options: &generated.RunOptionsInput{Overrides: nullable.NewNullableWithValue(override)},
    })
    if err != nil { return a13n.RunOutcome{}, err }
    defer interaction.Close()
    return interaction.Result(ctx)
}
```

`extra_body` and `extra_headers` each replace their inherited object (Run over Agent over Model defaults), not merge nested entries. `{}` clears that object; omission and explicit null remain distinct. Pass only Service-allowed keys/values; do not forward arbitrary user input into provider HTTP headers. Check the outcome's status and `Run.Items(ctx)` before presenting execution as successful.
