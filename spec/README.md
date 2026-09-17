# Go SDK Contract

## Ownership and compatibility

This repository owns the root `github.com/converge-ai-labs/a13n-sdk-go` module, generated bindings, language adapters, tests and independent release lifecycle. Service owns durable state, authorization and endpoint/protocol semantics. `contract/source.json` identifies the exact upstream inputs. Development and generation use local copies without invoking Service or another SDK.

The module version and Service source commit are independent identities. A contract update does not publish a module or grant compatibility with arbitrary Service revisions. Moving from the old `agent-foundation/sdk/go` module requires caller import changes; the new repository publishes root `v<version>` tags.

## HTTP and models

The generated API covers the ordinary Native `/api/v1` operations in the pinned OpenAPI. Nullable fields preserve omitted, null and value states; generated union helpers retain typed `As...` and `From...` branches. Unknown response data is not a promise of complete runtime JSON Schema validation.

`Client.API()` shares the parent HTTP pool, authentication, cancellation and shutdown. Workspace-bound operations share that parent lifetime and never broaden credential authority. The Web facade retains its bounded response/error mapping. Generated `WithResponse` methods return status-specific bodies and HTTP evidence instead; they do not acquire the facade's 1 MiB bound or `ApiError` mapping.

Binary request bodies use readers. Raw generated methods retain a streaming `http.Response`, whose body the caller must close. `WithResponse` methods buffer it. All network operations accept contexts; closing the client cancels owned in-flight work. Generated ordinary HTTP methods do not implement Run SSE or notification WebSocket recovery.

## Failure and diagnostics

Mutations are never automatically replayed after an uncertain outcome. Context cancellation or a transport failure after dispatch does not prove rollback; callers reconcile effects with Service state. Credential diagnostics remain redacted while authorized wire serialization retains credentials.

## Verifiable invariants

- Every pinned Native HTTP operation has a generated binding.
- Generation consumes pinned tools and local files; generated bindings compile and pass behavior tests.
- Wire tests preserve union and nullable semantics.
- Transport tests exercise shared headers, cancellation and response-body lifetime.
- Compile-time negative tests reject incorrectly typed requests; race-enabled tests validate concurrent lifetime behavior.
