# a13n SDK for Go

Run an existing a13n Service Agent from a Go application, read its result, and continue the conversation. Go 1.25+; execution and authorization stay in the Service.

The current contract is pinned to Service `647371cdc6f1e85b9c4ccfe954999823f52f08e8`: 270 native operations and 364 HTTP wire schemas, plus both native stream exports. The [generated API guide](docs/generated-api.md) covers advanced resource access, while [saved content readback](docs/streaming-and-readback.md#explicitly-expand-saved-display-content) keeps previews and authoritative values distinct. Imported conversation messages use `[]json.RawMessage` to preserve native nested key order and number tokens.

## Before you start

1. [Set up a Service](https://github.com/converge-ai-labs/agent-foundation/blob/main/docs/a13n-service/get-started.md) and create an Agent in Console under **Agents → Create agent**. Copy its ID (`ap_…`). The Agent needs a configured model.
2. In the Agent's workspace, create a key under **Workspace settings → My API keys**. Copy the secret when it is shown; it is not displayed again. The key must have permission to run that Agent. For an application identity, an administrator can instead create a service account and issue its key under **Workspace settings → Service accounts**.
3. Use the Service origin as `A13N_SERVICE_URL` (for example `http://127.0.0.1:8080`), **without** `/api/v1`. Keep the API key out of source control and logs.

This SDK is in pre-public development: do not assume a Go module release is available. Use a local SDK checkout for the example below, or replace the local `replace` directive with `go get github.com/converge-ai-labs/a13n-sdk-go@<published-version>` **after** a version has been published.

## Run your first Agent

Create a separate application directory and `main.go`:

```bash
mkdir agent-example && cd agent-example
go mod init example.com/agent-example
```

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "time"

    a13n "github.com/converge-ai-labs/a13n-sdk-go"
)

func main() {
    if err := run(); err != nil {
        log.Fatal(err)
    }
}

func run() error {
    baseURL, apiKey, agentID := os.Getenv("A13N_SERVICE_URL"), os.Getenv("A13N_API_TOKEN"), os.Getenv("A13N_AGENT_ID")
    if baseURL == "" || apiKey == "" || agentID == "" {
        return fmt.Errorf("set A13N_SERVICE_URL, A13N_API_TOKEN and A13N_AGENT_ID")
    }

    client, err := a13n.NewClient(baseURL, a13n.NewSecret(apiKey), nil)
    if err != nil {
        return err
    }
    defer client.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()
    interaction, err := client.Agent(agentID).Start(ctx, "Explain this project in two sentences.")
    if err != nil {
        return err
    }
    defer interaction.Close()

    outcome, err := interaction.Result(ctx) // no stream loop is required
    if err != nil {
        return err
    }
    if outcome.Status != a13n.RunCompleted {
        return fmt.Errorf("run %s ended with status %s", outcome.Run.ID, outcome.Status)
    }
    encoded, err := json.MarshalIndent(outcome.Output, "", "  ")
    if err != nil { return err }
    fmt.Printf("Thread: %s\nOutput: %s\n", outcome.ThreadID, encoded)
    if text, ok := outcome.Text(); ok { fmt.Println(text) }
    return nil
}
```

Point Go at your local SDK checkout, fetch its dependencies, and run the program. Enter the API key at the prompt rather than putting it in a command or file:

```bash
SDK_DIR=/absolute/path/to/agent-foundation/sdk/go # change to your checkout
go mod edit -require=github.com/converge-ai-labs/a13n-sdk-go@v0.0.0
go mod edit -replace=github.com/converge-ai-labs/a13n-sdk-go="$SDK_DIR"
go mod tidy
export A13N_SERVICE_URL=http://127.0.0.1:8080 A13N_AGENT_ID=ap_your_agent_id
read -r -s -p 'Service API key: ' A13N_API_TOKEN; printf '\n'; export A13N_API_TOKEN
go run .
```

This prints the authoritative Run output without a display-items request. `outcome.Text()` returns `(string, bool)` only for string output, including an empty string. Structured output remains JSON; numbers are `json.Number`, not rounded `float64`. Use `a13n.DecodeOutput[T](outcome)` to decode the original output into your application type. Check the direct `Status`, `Pending` and `Failure` fields: waiting, failed and cancelled are outcomes, not successful replies.

This example needs a reachable Service and Agent; `go run` does not start one. Omitting `IdempotencyKey` generates a random key once; `interaction.Receipt().IdempotencyKey` exposes it. For durable recovery, persist your own key and identical body before dispatch. A new omitted-key call is a new submission, and the SDK never retries a mutation automatically.

## Continue the conversation

Save the Thread ID. Continue it with an explicitly chosen Agent and a fresh bounded context:

```go
followUp, err := client.Agent(agentID).Send(ctx, threadID, "What are the trade-offs?")
if err != nil { return err }
defer followUp.Close()
result, err := followUp.Result(ctx)
if err != nil { return err }
if result.Status != a13n.RunCompleted {
    return fmt.Errorf("run %s ended %s", result.RunID, result.Status)
}
fmt.Println(result.Output)
```

## Preview text, then read the result

Read the same interaction's optional preview before `Result`; add `errors` to your imports:

```go
for chunk, err := range interaction.Text() {
    if err != nil {
        var interrupted *a13n.PreviewInterruptedError
        if !errors.As(err, &interrupted) { return err }
        fmt.Println("Preview unavailable:", interrupted.Reason)
        break // the outcome observer remains usable
    }
    fmt.Print(chunk)
}
outcome, err := interaction.Result(ctx)
if err != nil { return err }
fmt.Println(outcome.Status, outcome.Output)
```

Preview is not a lossless transcript: a Run may seal before attachment and yield no chunks. Gap/reset, missing message starts, protocol discontinuities and unsupported root CUSTOM fragment framing interrupt it explicitly; it does not reconstruct a baseline or assemble fragments. Only root assistant content is emitted, not child/user or hidden content. Advanced callers use `Next()` for native frames. There is one active reader; after preview interruption, deliberate sequential raw reading is allowed. Always `defer interaction.Close()`, including on early break. Close joins local observation and does not remotely interrupt execution.

## Go further

- [Application guide](docs/README.md): task-based chapters for [conversations](docs/agents-and-conversations.md), [streaming/readback](docs/streaming-and-readback.md), [waiting and tools](docs/waiting-and-tools.md), [files and Memory](docs/files-and-memory.md), [authentication](docs/authentication.md), [generated API](docs/generated-api.md), and [recovery](docs/errors-and-recovery.md).
- [Compiled examples](example_test.go) and [SDK contract](spec/README.md): exact types and lifecycle guarantees.
- [Pinned Service API](contract/openapi.json) and [provenance](contract/README.md): what this checkout was generated from. `client.API()` exposes its complete generated low-level API; [contribution guide](CONTRIBUTING.md) covers generation and validation.

For result-only applications, `Start`/`Send` plus `Result` is enough. Public `Input`/`TextPart`/`JSONPart`/`URLPart`/`AssetPart` and `RunOptions`/`Configuration`/`Overrides` support structured submissions without generated imports. Recover saved IDs with `client.Entry(threadID, entryID).Observe(ctx)`; observe an exact Run with `client.Run(runID).Observe(ctx)` or its result-only `Result(ctx)` convenience. `StartOptions.MessageHistory` can import completed Pydantic AI conversation JSON when creating a new Thread; later `Send` calls do not reseed it. A queued submission might wait before it runs; a waiting Run requires a complete, explicit batch of approval decisions and call results. `Run.Resume(ctx, a13n.Answers{callID: a13n.Returned(value)}, a13n.ResumeOptions{Input: a13n.Value(input)})` returns the distinct successor Interaction and may include *additional* user content atomically in that same Run. It does not answer a missing call or approve one implicitly. A timeout or broken connection does **not** prove the remote submission failed, and the SDK does not automatically retry it.

`Run.Items(ctx)` reads a committed display **window**, not automatically all history. Its default read includes the whole mutable tail even beyond the Service's limit (default 200, maximum 500). Use one optional `a13n.RunItemsOptions{Before: &ordinal, Limit: &limit}` or After for exclusive ordinal windows. Complete means sealed, not all history loaded. Items returns no stream position, continuation or replay hint. For advanced server-owned snapshots of a Session, use `client.SessionEvents(ctx, sessionID, a13n.SessionStreamOptions{})`; its single connection yields typed thread/items/delta/gap frames and reopens from a fresh snapshot. See [streaming and readback](docs/streaming-and-readback.md).

A Thread's `LastRunId` is its latest sealed Run of any outcome. Continue completed, failed or cancelled history with normal explicit `Send`; failed/cancelled outcomes stop automatic advancement but do not discard history. Resume is only for the exact idle waiting last Run with full approvals/calls, not a generic retry.

Desktop waits finish the finite Interaction normally. Use the explicit native control/completion APIs before deciding whether to resume; the SDK does not acquire or renew a human lease. See [desktop control, typed tool discovery and nullable context usage](docs/desktop-and-tool-discovery.md).

## License

Apache License 2.0.
