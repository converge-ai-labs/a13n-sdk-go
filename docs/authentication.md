# Authentication and workspace scope

For a server-side application, use a workspace API key. Console issues a personal key under **Workspace settings → My API keys**; an administrator can create a **service account** and issue its key under **Workspace settings → Service accounts**. The secret appears only once. The principal still needs permission to read/run the Agent. See the Service [identity guide](https://github.com/converge-ai-labs/agent-foundation/blob/main/docs/a13n-service/identity.md#api-keys) for grants and key issuance.

The [quick-start program](../README.md) reads the key from `A13N_API_TOKEN` rather than source code. A key already identifies its workspace; ordinary Agent and generated business calls must **not** send a different `X-Workspace-ID`. With `a13n` imported:

```go
client, err := a13n.NewClient(serviceURL, a13n.NewSecret(apiKey), nil)
if err != nil { return err }
defer client.Close()
```

Use the Service origin, not a URL ending in `/api/v1`. The default transport verifies TLS; a private CA can be configured on an `http.Transport` you pass to `NewClient`. Do not disable verification or log the secret. The client owns its configured transport and closes idle connections with `Close()`; closing it does not interrupt a remote Run.

## Use a login session you already own

A user session is a different credential type. Your application supplies its own authenticated cookie jar and a callback returning the current CSRF token; the callback may be called concurrently. It must not be combined with an API key. This function imports `net/http` and `a13n`:

```go
func sessionClient(baseURL, workspaceID string, jar http.CookieJar, csrf func() string) (*a13n.Client, error) {
    return a13n.NewClient(baseURL, a13n.Secret{}, nil,
        a13n.WithSession(jar, csrf),
        a13n.WithSessionWorkspace(workspaceID))
}
```

High-level calls such as `client.Agent(agentID).Start(...)` now use the chosen session workspace on scoped operations. Public, organization and deployment-wide operations do not receive it. `WithSessionWorkspace` is *not* a substitute for obtaining a valid session cookie or CSRF token. For most backend integrations, a service-account API key is simpler.

The generated low-level API deliberately keeps scope **explicit**, even on a session client. This function imports `context`, `a13n` and `generated`:

```go
func sessionAgent(ctx context.Context, client *a13n.Client, workspaceID, agentID string) (generated.Agent, error) {
    api, err := client.API()
    if err != nil { return generated.Agent{}, err }
    response, err := api.GetAgentApiV1AgentsAgentIdGet(ctx, agentID,
        &generated.GetAgentApiV1AgentsAgentIdGetParams{XWorkspaceID: &workspaceID})
    agent, err := a13n.ParseJSON[generated.Agent](client, response, err, 200)
    if err != nil { return generated.Agent{}, err }
    return agent.Value, nil
}
```

The result is the exact Agent view returned by Service. Generated methods never silently inherit `WithSessionWorkspace`; leaving their `XWorkspaceID` empty on a session-scoped route is an authentication/scope error. Conversely, raw public or administrator operations should not be given a workspace selector they do not define. A login session's mutating requests carry its CSRF token; a nil CSRF callback is rejected at client creation. See [generated API](generated-api.md) for conditional updates on either credential type.
