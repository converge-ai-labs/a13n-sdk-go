# a13n for Go

Go SDK module for a13n Service.

## Status

This SDK implements Web Provider management for Native `/api/v1`: the type catalog, Workspace/Organization account create/list/get/update, saved-account tests, and authorized references. Responses preserve ETags, and mutations are never automatically replayed after an uncertain outcome.

The generated low-level API covers every ordinary Native `/api/v1` HTTP operation in the shared Service OpenAPI contract. The Web facade provides typed `AgentConfig` and `AgentRunOverride` wrappers, while complete request/resource models live in `generated`. Generated HTTP bindings do not implement Run SSE or notification WebSocket recovery.

## Installation

```bash
go get github.com/converge-ai-labs/a13n-sdk-go@latest
```

```go
import "github.com/converge-ai-labs/a13n-sdk-go"
```

Go module releases use canonical module tags in the form `v<version>`, created by the `release/a13n/go/<version>` release workflow. `<version>` is stable `X.Y.Z` or RC `X.Y.Z-rc.N`.

## Web Provider accounts

Bind API Key operations with `client.Workspace(ctx)`. This reads `/api/v1/auth/context` once and returns Web Provider operations without a Workspace argument. The binding uses the immutable Workspace ID and shares the parent transport and shutdown. The parent client retains explicit `WebProviderScope` operations; Service always enforces the credential boundary.

Create a bearer client with `NewClient(baseURL, NewSecret(token), nil)` and call `Close` when finished. All operations accept `context.Context`. `WebProviders` returns a `Representation[Page[WebProvider]]`; pass `WebProviderListOptions{Cursor: ...}` to continue pagination. `UpdateWebProvider` requires the current account ETag. `TestWebProvider` sends one quota-consuming probe only when called.

Create a `WebProviderCredential` with `NewWebProviderCredential(map[string]any{"api_key": value})`, checking its error. It accepts the selected catalog type's arbitrary nested JSON credential object. JSON and formatting diagnostics redact the complete object; the client reveals it only for the authorized request. `AgentRunOverride.Toolsets` uses its zero value to inherit; a supplied `web` entry replaces that complete Toolset, and `Enabled: &false` disables it. `ToolPermissionInherit` preserves the authored `inherit` setting. Configuration JSON round-trips preserve other fields in `Fields`.

## Generated HTTP operations

Call `api, err := client.API()` once, check the error, then use `api.GetAuthContextWithResponse(ctx)` or any other generated operation. The generated view shares the parent HTTP pool, authentication, context cancellation, and `Close`; it does not create a second transport.

Import full request/resource types from `github.com/converge-ai-labs/a13n-sdk-go/generated`. Nullable fields use `nullable.Nullable[T]`, preserving omitted/null/value. Union helpers expose typed `As...` and `From...` branches. `WithResponse` methods expose typed status-specific bodies and HTTP headers; callers handle those results rather than the Web facade's `ApiError` mapping. These low-level methods do not impose the Web facade's 1 MiB response bound.

For binary transfer, use generated raw methods (without `WithResponse`) with `io.Reader` request bodies and the returned `*http.Response`; always close the response body. `WithResponse` helpers buffer the response. Go 1.25 or newer is required.

## Development

This repository is independently buildable with Go 1.25+. Generator development also needs Python 3.13, uv and Make, not Service or another SDK checkout.

```bash
make install
make generate         # local pinned input only
make generated-check  # non-mutating drift check
make check-all        # generation, tooling, vet, race tests and build
```

The module now lives at this repository's root; consumers must update old `agent-foundation/sdk/go` imports. `contract/source.json` records the upstream full commit SHA, original paths and input hashes. See [contract provenance](contract/README.md), [SDK contract](spec/README.md) and [Contributing](CONTRIBUTING.md).

`codegen/generate.py` invokes oapi-codegen 2.8.0 with the local configuration. Nullable presence and typed unions remain generated; a narrow diagnostic adapter redacts write-only request fields without changing wire serialization. Commit updated inputs and generated output together.

## License

Licensed under the Apache License 2.0.
