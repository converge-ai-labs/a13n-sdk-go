# Go SDK Contract

## Ownership and public surface

This repository owns the root `github.com/converge-ai-labs/a13n-sdk-go` module, generated bindings, adapters, tests and independent release lifecycle. Service owns authorization, durable state and wire semantics. `contract/source.json` identifies exact local inputs; generation does not execute Service or import another SDK.

`Client.Resources()` is the complete, statically generated ordinary API. Collections bind selectors through `Ref`; binding performs no I/O, does not verify existence and never broadens credential authority. Workspace selectors accept the Service's ID or key. Resource methods expose typed full request bodies, query/header options and responses. POST-only action leaves appear as methods on their owning resource. Run Items is a snapshot read (`Items().Get()`), not a collection.

`Client.API()` is the advanced generated protocol escape hatch on the same transport. It does not constitute another handwritten route tree. Resource and protocol bindings derive from the same pinned OpenAPI. Wire models live in `generated`; union helpers preserve typed branches, and `nullable.Nullable[T]` preserves omitted/null/value. Ordinary selectors and filters expose generated `ProviderKind`, `MemberKind` and `SkillSource` aliases with domain-named constants; they retain the protocol types' identity rather than introducing parallel enums. JSON Schema constraints are not all validated locally.

The module version and Service source commit are independent. A new pin neither publishes the module nor grants compatibility with arbitrary Service revisions. The SDK is unpublished; the old Web Provider facade and implicit credential-context lookup are replaced, not retained as competing APIs.

## Authentication, lifetime and responses

The client accepts a bearer credential, or an explicit session cookie jar with a current CSRF-token callback. Bearer and session configuration cannot be combined. An empty bearer credential supports public endpoints. The CSRF callback is caller-owned and must support concurrent calls. Service remains the authority for access, including account operations unavailable to workspace-confined API keys.

The client owns its transport and pool; references share that lifetime. Every network entry accepts a context. A Thread stream retains the context supplied to `Events` for all subsequent reads. There is no fixed whole-response timeout; caller deadlines cover dispatch and delivery, including wait polling. Client close cancels owned local work and closes idle connections; it never interrupts durable server Runs. Upload readers remain caller-owned and must not block indefinitely independent of context cancellation.

`Result[T]` retains the typed value, actual status and response headers, including ETag and request ID. OAuth completion also preserves its documented 303 redirect without following it; that response has a zero value and exposes the Location header. `ApiError` preserves status, headers, code, message and details. Buffered resource JSON is bounded (16 MiB by default, configurable); binary and SSE success bodies remain unbuffered and caller-owned. Callers close `BinaryResult` or its body. Protocol `WithResponse` calls use the upstream generated parser and its buffering, not the resource JSON limit or error mapping.

Transport failures support `errors.Is(err, ErrTransport)` and `errors.As` to `*TransportError`, exposing a request/body stage and a bounded category without retaining raw causes, URLs or credentials. Ordinary JSON failures support `errors.Is(err, ErrProtocol)` and `errors.As` to `*ProtocolError`, with a reason, HTTP status and available request ID. Diagnostic strings omit untrusted response metadata and bodies; request ID remains explicit inspection data. Cancellation, deadlines and parent shutdown keep their existing error identity. Advanced generated response parsers retain their own errors.

Mutations are not automatically replayed after an uncertain outcome. The SDK removes standard transport body-replay permission even when an idempotency header is present. Cancellation, deadline expiry and transport failure do not prove rollback. Request keys are explicit caller input; retries require caller reconciliation. Credential diagnostics remain redacted while authorized wire serialization retains write-only values.

## Resource semantics

Declared conditional mutations require a nonempty `IfMatch` option before dispatch, except Memory revision restore where the target can be absent. Request keys are likewise required on declared idempotent operations. Server validation still owns validity and stale-precondition errors.

Cursor-bearing collections provide lazy `Pages` iteration. Each page retains its own metadata. Options, including pointer-backed query filters, are snapshotted when iteration is constructed; subsequent pages change only the cursor. Early termination stops requests. Repeated cursors fail rather than looping. Bounded catalogues and Items snapshots do not acquire fake pagination.

Image replacement accepts explicitly declared MIME types; uploads stream a named file with an explicit content type through multipart framing. Downloads preserve headers and local cancellation without buffering the complete file.

## Interaction

Canonical full-body submission methods return `Submitted`: the wire receipt and bound Thread, Entry and optional Run references. References use canonical identity from the receipt, not the requested workspace key. A null Run is a queued entry, not a failed submission. Text payload construction is only a convenience over the generated union.

`Run.Wait` observes exactly that Run until completed, waiting, failed or cancelled. `Entry.Wait` observes consumption, failure or withdrawal; assignment alone is not consumption. Waits include request/body delivery and polling sleeps in the caller's context deadline. They do not follow successors automatically or assert business completion. Fork, resume, interrupt and inbox editing use their generated operations, preserving status and HTTP evidence.

## Thread observation

`Thread.Events` opens the Thread SSE endpoint. It exposes five typed variants: delta, boundary, changed, gap and reset. Only delta and boundary have cursor IDs. The adapter validates frame envelopes, required fields, item-reference values, UTF-8 and framing bounds; arbitrary AG-UI event contents remain application-owned.

A stream has one active `Next` reader; `Close` may run concurrently. The next `Next` acknowledges the prior returned data frame. Closing does not acknowledge a pending frame. Received and applied cursors are independently visible. Reconnection is explicit and bounded; only applied IDs are sent as `Last-Event-ID`. New acknowledged cursor progress resets the failure budget; hint-only traffic does not. Authentication/protocol failures are terminal; transient transport and selected HTTP failures may reconnect within that budget. Context expiry or client close stops reads and recovery delays.

Gap/reset require authoritative Items readback; changed requires Thread readback. Reconciliation and durable application checkpoints remain caller-owned. Closing observation does not mutate the Thread or Run.

## Verifiable invariants

- Every pinned HTTP operation has a protocol binding and a callable ordinary resource method.
- Generated resource coverage dispatches every declared successful status through a local transport; independent inventory checks compare the route set with OpenAPI.
- Tests preserve nullable and union semantics, typed invalid-request rejection, binary ownership, response evidence, exact scope and conditional headers.
- Wait, stream close, parent shutdown and recovery are covered by cancellation and race-enabled tests.
- Generation, unit tests and local packaging do not establish live provider compatibility or publication.
