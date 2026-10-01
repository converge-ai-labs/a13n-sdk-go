# Go SDK application guide

The [README quick start](../README.md) contains a complete Go program, local-source installation instructions and the credentials needed to run an existing Service Agent. Begin there. These chapters show how to extend that program; they do not require you to learn the Service's entire HTTP schema before getting a result.

| Task                                                                                                 | Guide                                                   |
| ---------------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| Create an Agent, start a Thread, continue it or choose typed Run options                             | [Agents and conversations](agents-and-conversations.md) |
| Show provisional progress and read committed output; manage deadlines and Close                      | [Streaming and readback](streaming-and-readback.md)     |
| Inspect pending requests and let an authorized person approve/reject, or complete a client tool      | [Waiting and tools](waiting-and-tools.md)               |
| Upload Assets, send structured parts, work with Memory files and mounts                              | [Files and Memory](files-and-memory.md)                 |
| Choose an API key or a login session and scope requests correctly                                    | [Authentication](authentication.md)                     |
| Use generated endpoints, Model Provider authorization/discovery, media policy and conditional writes | [Generated API](generated-api.md)                       |
| Handle transport failures, queued submissions, stale ETags and uncertain mutation outcomes           | [Errors and recovery](errors-and-recovery.md)           |

**Common setup.** Examples assume the `client`, `agentID` and Service origin from the README. Each new submission supplies a caller-chosen request key. Every `Interaction` should be closed, including when iteration stops early. The code blocks identify additional standard-library imports or provide complete function signatures; they can be copied into a Go 1.25+ program with the SDK module.

| Name used in examples | Go import path                                                        |
| --------------------- | --------------------------------------------------------------------- |
| `a13n`                | `github.com/converge-ai-labs/a13n-sdk-go` (import with `a13n` alias)  |
| `generated`           | `github.com/converge-ai-labs/a13n-sdk-go/generated`                   |
| `nullable`            | `github.com/oapi-codegen/nullable` (third-party nullable wire fields) |

The high-level `client.Agent(agentID).Start` / `.Send` workflow returns one finite interaction. Its `Result` works without a stream loop. The separate `client.API()` is the complete generated low-level API, on the same authenticated transport; use it for administration and capabilities outside the Agent workflow. Service owns execution and authorization. Consult the [SDK contract](../spec/README.md) for precise lifecycle guarantees, the [pinned OpenAPI](../contract/openapi.json) for individual fields, and [contract provenance](../contract/README.md) to compare your Service deployment with this checkout. Local tests and documentation do not imply a module release or verified external-provider compatibility.
