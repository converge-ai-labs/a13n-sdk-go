# Go SDK application guide

Use this module to call an existing a13n Service. It does not run an agent in your Go process. The [README](../README.md) is the quick start; this Markdown guide covers application integration and recovery. [External-package examples](../example_test.go) are compiled by `go test` and show the exact public API.

## Setup and reading map

Go 1.25+ is required. The module path is `github.com/converge-ai-labs/a13n-sdk-go`. Add an available release with `go get github.com/converge-ai-labs/a13n-sdk-go@<version>`, replacing the placeholder with the chosen tag, or select a reviewed commit. For local development against a checkout, an application can use a Go workspace or an explicit local `replace`; neither proves the module has been published.

You need a Service base URL, an authorized credential, a workspace ID or key, and an existing Agent ID for submission. Get these from Console or your deployment administrator. References bind locally and do not discover authority. SDK/module versions are independent from the pinned Service commit in [contract/source.json](../contract/source.json).

| Task                                         | Read                                                                    |
| -------------------------------------------- | ----------------------------------------------------------------------- |
| Submit and read the receipt                  | [Start a Thread](../README.md#start-a-thread)                           |
| Update with CAS or paginate                  | [Read, update and page](../README.md#read-update-and-page)              |
| Observe provisional output                   | [Observe a Thread](../README.md#observe-a-thread)                       |
| Own uploads/downloads and inspect errors     | [Authentication and transfer](../README.md#authentication-and-transfer) |
| Use full wire-level methods                  | [Advanced protocol access](../README.md#advanced-protocol-access)       |
| Understand accepted guarantees               | [SDK contract](../spec/README.md)                                       |
| Develop or run disposable-Service acceptance | [Contributing](../CONTRIBUTING.md)                                      |

## One Client, explicit scope and lifetime

`NewClient(baseURL, NewSecret(token), httpClient)` uses Bearer authentication. Pass `Secret{}` for public access; use `WithSession(jar, csrfCallback)` without a Bearer credential for session operations. Your application owns current cookie/CSRF state. API-key access is not a substitute for session-only account administration.

Pass a configured `*http.Client` when your deployment needs custom trusted roots or transport settings. Do not disable TLS verification. Bound the entire operation with a `context.Context`; a request timeout is not the same as a deadline covering an entire queue/Run workflow or stream. `defer client.Close()` releases SDK work, but is not a remote Run interrupt.

Upload readers stay caller-owned. Every returned `BinaryResult` must be closed, including early exits. Raw generated `*http.Response` bodies are also caller-owned. A Thread event stream needs `defer stream.Close()` even when iteration stops early.

## Find an operation

Start at `client.Resources()`. Scope methods and `.Ref(selector)` construct local references; methods on them dispatch I/O.

| Resource                  | Example path in the Go API                                |
| ------------------------- | --------------------------------------------------------- |
| Workspace                 | `client.Resources().Workspaces().Ref(workspaceID)`        |
| Organization              | `client.Resources().Organizations().Ref(organizationID)`  |
| Agent                     | `workspace.Agents().Ref(agentID)`                         |
| Thread                    | `workspace.Threads().Ref(threadID)`                       |
| Run and committed display | `workspace.Runs().Ref(runID).Items().Get(ctx)`            |
| Memory file               | `workspace.Memories().Ref(memoryID).Files().Ref(path)`    |
| Memory history            | `workspace.Memories().Ref(memoryID).Revisions().Ref(seq)` |

Use IDE completion, `go doc`, the generated [resource methods and options](../resources.gen.go), and [wire models](../generated/client.gen.go). Provider/member/skill selectors have readable `ProviderKind`, `MemberKind` and `SkillSource` aliases. The [pinned OpenAPI](../contract/openapi.json) gives wire field definitions. Do not create manual URLs merely because the language names differ from HTTP paths.

`Result[T]` retains `Value`, `StatusCode`, `Header`, `ETag()` and `RequestID()`. `List` fetches one page; `Pages` lazily yields complete results, not just items. Stop ranging to stop further requests. Bounded catalogues and Run Items do not invent pagination.

Advanced `client.API()` shares transport and shutdown, but generated raw/`WithResponse` methods retain their own parsing and body-ownership rules. Prefer ordinary resources unless you deliberately need that lower-level boundary.

## Omission, null and concurrency

Generated nullable fields use `nullable.Nullable[T]`: the zero value omits a field, `nullable.NewNullNullable[T]()` sends null, and `nullable.NewNullableWithValue(value)` sends a value. Service determines what null means for each field; it does not universally clear it. For Agent description updates, null keeps the existing value while an empty string is explicit content.

Read the target resource and pass its ETag through the method's `IfMatch` option. The relevant owner matters: Memory content takes a file ETag, while Thread inbox/mount edits take the Thread ETag. Reconcile a `412` with the current representation before deciding to retry; never blindly acquire a fresh ETag and overwrite.

## Submission and recovery

A `Submitted` result includes a Thread, an Entry and an optional Run, plus the response receipt. References use the receipt's canonical workspace identity. A nil Run means retained input, not failure.

- When queued, wait on the exact Entry and inspect its status. Consumption differs from assignment; failed/withdrawn entries do not imply a successful Run.
- Use the Entry's assigned Run ID when you explicitly decide to observe it. `Run.Wait(ctx, interval)` ends at completed, waiting, failed or cancelled; inspect the status rather than treating nil error as success.
- Keep one deadline context across queue observation and Run waiting when they share a total budget.
- Resume returns a successor Run view. Bind that returned ID before waiting; the original reference remains the original Run. Fork returns another submission receipt.
- Reuse a saved idempotency key for the same logical request after reconciliation. A new key submits a new operation. Mutations are never automatically replayed.

`context.Canceled`, `context.DeadlineExceeded` and client closure only describe local work. They neither prove rollback nor interrupt remote execution. Read back saved identities after an uncertain outcome. Interrupt is an explicit remote command.

Typed `Events` is separate from `thread.Stream().Get(...)`, which returns raw SSE bytes. `Next()` acknowledges the previously returned cursor-bearing frame, so apply it before calling `Next()` again. `delta`/`boundary` carry cursors; `changed` requires Thread readback and `gap`/`reset` require Run Items readback. Apply those snapshots yourself. EOF and reconnect are not durable completion signals, and application checkpoint storage is not owned by the SDK.

## Memory files, records and mounts

`workspace.Memories().Ref(memoryID)` owns separate APIs:

| Surface             | Behavior                                                                                              |
| ------------------- | ----------------------------------------------------------------------------------------------------- |
| `Files()`           | JSON text by logical path; list/create/get/replace/delete/move. Pass paths unescaped to `.Ref(path)`. |
| `Revisions()`       | History selected by integer sequence; get/restore and path-scoped history purge.                      |
| `Records()`         | Provider-backed list/create/replace/delete/search; no item GET or file ETag.                          |
| `thread.Memories()` | Named mounts edited with the Thread ETag.                                                             |

Restore undoes the selected revision's change. Undoing file creation can return a `MemoryFileState` with no file; do not dereference it unconditionally. Supply the current file ETag if it exists, and omit it only for an absent restore target. Removing history and deleting a current file are different operations.

Submission/fork bodies can carry memory mounts. `RunView.MemoryMounts` captures accepted mounts and is not changed by later Thread edits. Provider configuration/testing lives under the organization's `MemoryProviders()`, separate from file content. Uncertain provider record writes need reconciliation, not automatic replay.

## Error handling

Use `errors.As` for `*ApiError` and inspect HTTP status, Service code and request ID. Authentication/permission (`401`/`403`), conflict (`409`), stale ETag (`412`) and missing precondition (`428`) call for different application decisions.

Use `errors.Is` for context cancellation/deadlines, `ErrClosed`, `ErrTransport` and `ErrProtocol`. `errors.As` further exposes `*TransportError` and `*ProtocolError` with safe stage/category or response-boundary evidence. Raw transport causes are not retained, and diagnostic strings do not substitute for structured fields. Avoid printing request bodies, tokens or cookies when investigating failures.

A transport or protocol error after sending a mutation does not establish whether Service committed it. Save request keys and returned identities, then read back before retrying.

## Compatibility and test boundaries

Every pinned HTTP operation has a generated ordinary resource method, but that is structural coverage, not a claim that all deployments/providers were tested. Read documentation at your consumed commit/tag and compare [contract provenance](../contract/README.md) with the deployed Service.

Local race tests and isolated module-ZIP acceptance do not contact a real Service. The [optional HTTPS acceptance](../CONTRIBUTING.md#disposable-service-acceptance) creates resources in caller-provisioned disposable state; it does not verify external cloud providers or publish a release.
