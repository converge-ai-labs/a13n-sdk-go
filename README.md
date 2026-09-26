# a13n for Go

A typed Go SDK for a13n Service: a complete generated resource API, full HTTP access, and thin submission, waiting and Thread SSE helpers. Go 1.25 or newer is required.

## Start a Thread

```go
import (
    "context"
    "time"

    a13n "github.com/converge-ai-labs/a13n-sdk-go"
    "github.com/converge-ai-labs/a13n-sdk-go/generated"
)

client, err := a13n.NewClient(baseURL, a13n.NewSecret(token), nil)
if err != nil { return err }
defer client.Close()

ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
defer cancel()
workspace := client.Resources().Workspaces().Ref("my-workspace")
submitted, err := workspace.Threads().Create(ctx, generated.NewThread{
    AgentId: "agent_example",
    Payload: a13n.TextPayload("Explain this project"),
}, a13n.ThreadsCreateOptions{IdempotencyKey: requestKey})
if err != nil { return err }
if submitted.Run != nil {
    run, err := submitted.Run.Wait(ctx, 0) // default polling interval
    if err != nil { return err }
    _ = run.Value.Status // waiting/failed/cancelled are not successful completion
}
// A queued receipt has no Run. Observe submitted.Entry explicitly instead.
```

Binding a reference is local. The server authorizes requests and resolves IDs or keys. Full generated request models, including rich payloads, mounts and run options, are accepted directly. Submission references use the canonical workspace ID returned by Service. The receipt preserves status, headers and the nullable Run. `Run.Resume` instead returns the successor Run: bind `workspace.Runs().Ref(resumed.Value.Id)` before waiting. Waiting on the original reference never follows that successor.

[Compiled external-package examples](example_test.go) cover submission/readback, queued entries, resume, CAS/null updates, typed events and binary ownership. They are type-checked by `go test`; Service examples require your own explicit resources to execute.

## Read, update and page

```go
agent := workspace.Agents().Ref("agent_example")
current, err := agent.Get(ctx)
if err != nil { return err }
// import "github.com/oapi-codegen/nullable"
updated, err := agent.Update(ctx, generated.AgentUpdate{
    Name: nullable.NewNullableWithValue("Support"),
    Description: nullable.NewNullNullable[string](), // explicit null clears it
}, a13n.AgentUpdateOptions{
    IfMatch: current.ETag(),
})
if err != nil { return err }
_ = updated.RequestID()

for page, err := range workspace.Threads().Pages(ctx, a13n.ThreadsListOptions{}) {
    if err != nil { return err }
    for _, thread := range page.Value.Items { _ = thread.Id }
}
```

`List` makes one request; `Pages` is lazy and retains each response's metadata. Breaking iteration stops further requests. Only cursor-bearing collections have `Pages`. Run Items uses `run.Items().Get(ctx)`.

Ordinary selectors and filters use `a13n.ProviderKind`, `a13n.MemberKind` and `a13n.SkillSource` with readable constants such as `ProviderKindMemory` and `MemberKindServiceAccount`. These are generated aliases, not additional wire types. Other models live in `generated`. Nullable fields use `nullable.Nullable[T]` from `github.com/oapi-codegen/nullable`: the zero value omits a field, `NewNullNullable[T]()` clears it, and `NewNullableWithValue(value)` supplies it. Union helpers expose typed `As...` and `From...` branches. These are wire models, not a promise of complete local JSON Schema validation.

## Observe a Thread

```go
stream, err := submitted.Thread.Events(ctx, a13n.StreamOptions{MaxReconnects: 3})
if err != nil { return err }
defer stream.Close()
for {
    frame, err := stream.Next()
    if err == io.EOF { break } // import "io"
    if err != nil { return err }
    switch event := frame.(type) {
    case a13n.DeltaFrame:
        _ = event.Event // apply provisional content before calling Next again
    case a13n.GapFrame:
        _, err = workspace.Runs().Ref(event.RunID).Items().Get(ctx)
    case a13n.ResetFrame:
        _, err = workspace.Runs().Ref(event.RunID).Items().Get(ctx)
    case a13n.ChangedFrame:
        _, err = submitted.Thread.Get(ctx)
    }
    if err != nil { return err }
}
```

`thread.Stream().Get(...)` returns raw SSE bytes; prefer `thread.Events(...)` for typed frames and cursor recovery. Readback results must be applied by the application. `Next` acknowledges the previously returned data frame, not the one it is about to return. Persist application checkpoints explicitly. Reconnection uses only the applied cursor; closing does not acknowledge pending data or stop a server Run. The context passed to `Events` bounds the whole stream; `Close` can cancel a pending read from another goroutine.

## Authentication and transfer

Use a bearer API key for its authorized workspace scope. Public endpoints work with `a13n.Secret{}`. For login-session operations, pass `a13n.WithSession(jar, csrfCallback)` with no bearer token. The cookie jar and concurrency-safe callback supply current session state; the SDK does not infer tenant authority from credentials.

Uploads use `UploadFile{Name, ContentType, Reader}`. Image `Replace` takes an `io.Reader` and an explicit allowed content type in its options. Input readers stay caller-owned. Download `BinaryResult` bodies are unbuffered: always close the result. Resource JSON is bounded to 16 MiB by default (`WithResponseLimit` changes it); binary success bodies have no such buffering limit.

`Result[T]` exposes `Value`, `StatusCode`, `Header`, `ETag()` and `RequestID()`. Use `errors.As` to inspect `*a13n.ApiError`; use `errors.Is` for context deadlines/cancellation, `ErrClosed`, `ErrTransport` and `ErrProtocol`. An uncertain mutation is not automatically retried, even when it carries an idempotency key.

`errors.As` also exposes `*a13n.TransportError` (request/body stage and DNS/TLS/timeout/network category) and `*a13n.ProtocolError` (JSON/content-type/size reason, HTTP status and request ID). `errors.Is` still matches the respective sentinel. Raw transport causes are not retained because they can contain URLs or credentials. Protocol diagnostic strings omit bodies and request IDs; inspect `RequestID` explicitly when reconciling with Service. Neither category nor cancellation proves rollback.

## Advanced protocol access

`api, err := client.API()` exposes every generated HTTP operation on the same transport and lifetime. Raw methods return caller-owned `*http.Response` bodies; `WithResponse` methods buffer and expose status-specific bodies and headers. They retain the generated parser's behavior rather than ordinary resource error mapping and JSON limits.

The ordinary resource API is generated, not a second handwritten route catalogue. `codegen/generate.py` uses pinned oapi-codegen 2.8.0 and `codegen/resources.py`; both consume local contract files. `contract/source.json` identifies the Service revision; module versions are independent of that pin.

## Development and releases

```bash
make install
make generate
make check-all # formatting, vet, tooling types, race tests and build
```

`make check-all` also installs a locally assembled module ZIP through a temporary file-based Go proxy into an isolated consumer, without a `replace` directive. This verifies the package boundary without publishing a version.

For an explicitly provisioned disposable HTTPS Service, set `A13N_SERVICE_URL`, `A13N_API_TOKEN`, `A13N_WORKSPACE`, `A13N_AGENT`, `A13N_CLIENT_TOOL_AGENT`, `A13N_ORGANIZATION`, `A13N_MEMORY_PROVIDER` and `A13N_CA_BUNDLE`, then run `uv run --locked python scripts/accept-installed.py`. It exercises the installed module's submission/replay, streaming recovery, inbox/control, binary transfer, CAS and memory journeys with certificate verification enabled. It creates resources; never point it at production. The caller owns provisioning and fixture cleanup. Scripted model and memory-provider fixtures do not establish external cloud-provider compatibility.

The module is unpublished until a separately authorized release. Release workflows create canonical `v<version>` module tags from `release/a13n/go/<version>` tags. No release is implied by local tests or a contract update. See [SDK contract](spec/README.md), [contract provenance](contract/README.md) and [Contributing](CONTRIBUTING.md).

## License

Apache License 2.0.
