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

Run Items is not a cursor collection: use `Run.Items(ctx, a13n.RunItemsOptions{Before: ..., After: ..., Limit: ...})` or the generated operation's native Before/After/Limit params for one ordinal window. Do not feed Items into `Pages` or treat Complete as all-history coverage. See [ordinal display windows](streaming-and-readback.md#read-one-ordinal-history-window).

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

Model, connector and Environment provider catalogues are deployment-discovered through the generated API, not an SDK-owned exhaustive provider whitelist. Service can add model backends (including Cerebras, SambaNova and xAI) and Environment backends (including Vercel) without a new SDK enum or operation. MCP transport, tool discovery and issuer handling remain Service-owned; the SDK forwards native Model/Connection configuration and does not embed an MCP client.

## Model Provider authorization and discovery

`client.API()` includes the five Model Provider authorization/discovery operations alongside every other pinned endpoint. ChatGPT authorization belongs to a workspace-shared Model Provider, not a personal Connection or principal credential. Status needs `read`; authorize, callback and disconnect need `write`; account-specific model discovery needs `run`. The Service decides access. Manual authorization is not categorically session-only; the hosted browser flow specifically requires an unconfined user login. For session calls, use [session authentication](authentication.md#use-a-login-session-you-already-own) and pass `XWorkspaceID` explicitly on generated params.

These helpers import `context`, `fmt`, `a13n` and `generated`. They preserve returned HTTP status and headers rather than extracting a credential or constructing their own callback origin:

```go
func beginModelAuthorization(ctx context.Context, client *a13n.Client, providerID, workspaceID string) (
    a13n.Result[generated.AuthorizationStart], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[generated.AuthorizationStart]{}, err }
    response, err := api.AuthorizeModelApiV1ModelProvidersProviderIdAuthorizePost(ctx, providerID,
        &generated.AuthorizeModelApiV1ModelProvidersProviderIdAuthorizePostParams{XWorkspaceID: &workspaceID},
        generated.ProviderAuthorizationRequest{})
    return a13n.ParseJSON[generated.AuthorizationStart](client, response, err, 200)
}

func modelAuthorizationStatus(ctx context.Context, client *a13n.Client, providerID, workspaceID string) (
    a13n.Result[generated.AuthorizationStatus], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[generated.AuthorizationStatus]{}, err }
    response, err := api.ModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationGet(ctx, providerID,
        &generated.ModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationGetParams{XWorkspaceID: &workspaceID})
    return a13n.ParseJSON[generated.AuthorizationStatus](client, response, err, 200)
}

func completeManualModelAuthorization(ctx context.Context, client *a13n.Client, providerID, workspaceID string,
    start generated.AuthorizationStart, pastedCallback string,
) (a13n.Result[generated.AuthorizationStatus], error) {
    if start.Method != nil && *start.Method != generated.ManualCallback {
        return a13n.Result[generated.AuthorizationStatus]{}, fmt.Errorf("this flow uses a hosted browser callback")
    }
    api, err := client.API()
    if err != nil { return a13n.Result[generated.AuthorizationStatus]{}, err }
    response, err := api.CompleteModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationCallbackPost(ctx, providerID,
        &generated.CompleteModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationCallbackPostParams{XWorkspaceID: &workspaceID},
        generated.AuthorizationCallback{AttemptId: start.AttemptId, CallbackUrl: &pastedCallback})
    return a13n.ParseJSON[generated.AuthorizationStatus](client, response, err, 200)
}

func availableProviderModels(ctx context.Context, client *a13n.Client, providerID, workspaceID string) (
    a13n.Result[[]generated.ChatGPTModel], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[[]generated.ChatGPTModel]{}, err }
    response, err := api.DiscoverModelProviderModelsApiV1ModelProvidersProviderIdModelsGet(ctx, providerID,
        &generated.DiscoverModelProviderModelsApiV1ModelProvidersProviderIdModelsGetParams{XWorkspaceID: &workspaceID})
    return a13n.ParseJSON[[]generated.ChatGPTModel](client, response, err, 200)
}

func disconnectModelAuthorization(ctx context.Context, client *a13n.Client, providerID, workspaceID string) (
    a13n.Result[generated.AuthorizationDisconnect], error,
) {
    api, err := client.API()
    if err != nil { return a13n.Result[generated.AuthorizationDisconnect]{}, err }
    response, err := api.DisconnectModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationDelete(ctx, providerID,
        &generated.DisconnectModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationDeleteParams{XWorkspaceID: &workspaceID})
    return a13n.ParseJSON[generated.AuthorizationDisconnect](client, response, err, 200)
}
```

Show the returned `AuthorizationUrl` only to the initiating authorized person; it may contain flow data. Follow `Method`: `manual_callback` expects a pasted callback URL using the returned attempt, while `browser_callback` completes through the Service's configured browser flow and status polling. Do not log pasted callback codes or manufacture OAuth client IDs, redirect origins, PKCE, browser cookies or a second token store. Hosted callback registration and commercial approval belong to the operator; tokens remain encrypted in the Service. `AuthorizationCallback` is redacted in ordinary diagnostics but its JSON carries the sensitive callback value for submission, so do not log request bodies.

Status may remain pending while an exchange is in progress even if old credentials exist; do not infer success from them. Discovery returns an account-specific array of `ChatGPTModel{Slug, DisplayName}`. Disconnect reports `LocalTokensCleared` separately from nullable `RevocationConfirmed`; a null or false revocation result is not proof of remote revocation. Local mock tests validate serialization only, not external login, account entitlements or paid-model invocation.

## Forward model image and video policy

The generated Model configuration's `Characteristics` is separate from native model `Settings` and from the Run snapshot. This example imports `context`, `a13n`, `generated` and `nullable`:

```go
func createMediaModel(ctx context.Context, client *a13n.Client, providerID, modelAPI, modelName string) (
    a13n.Result[generated.Model], error,
) {
    zero, no, videoBytes := 0, false, 10*1024*1024
    characteristics := generated.HarnessModelCharacteristicsInput{
        ImageInput: nullable.NewNullableWithValue(generated.ImageInputPolicy{
            MaxImageBytes: &zero, SplitLargeImages: &no,
        }),
        UrlInput: &generated.UrlInputSupportInput{Video: &[]generated.VideoUrlType{generated.Youtube}},
        VideoInput: &generated.VideoInputPolicy{MaxVideoBytes: &videoBytes},
    }
    api, err := client.API()
    if err != nil { return a13n.Result[generated.Model]{}, err }
    response, err := api.CreateModelApiV1ModelsPost(ctx,
        &generated.CreateModelApiV1ModelsPostParams{}, generated.ModelCreate{
            Name: "Media model", ProviderId: providerID,
            Config: generated.ModelConfigInput{
                ModelApi: modelAPI, ModelName: modelName, Characteristics: &characteristics,
            },
        })
    return a13n.ParseJSON[generated.Model](client, response, err, 201)
}
```

Pointers preserve explicit zero/false. An omitted `ImageInput` uses native defaults; explicit null disables automatic preparation; an explicit object forwards its chosen limits. A zero `MaxImageBytes` disables that image byte limit, but zero `MaxImages` removes all image input. `VideoInput.MaxVideoBytes` bounds Base64-encoded bytes for one video **and** all inline videos in one model request. `UrlInput.Video` identifies transport-native URL subtypes; an empty array is not omission. Select only characteristics supported by the actual provider. Service/Harness owns preparation, downloads, allowed-host checks, TLS and budgets; sending a URL or Asset part with `StartPayload`/`SendPayload` neither processes media locally nor bypasses those checks. Generated content and authored input retain their different display provenance, as explained in [streaming and readback](streaming-and-readback.md#native-ag-ui-10-content-and-child-attribution).
