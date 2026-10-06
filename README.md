# a13n SDK for Go

Run an existing a13n Service Agent from a Go application, read its result, and continue the conversation. Go 1.25+; execution and authorization stay in the Service.

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
    "crypto/rand"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "time"

    a13n "github.com/converge-ai-labs/a13n-sdk-go"
    "github.com/converge-ai-labs/a13n-sdk-go/generated"
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
    interaction, err := client.Agent(agentID).Start(ctx, "Explain this project in two sentences.",
        a13n.StartOptions{RequestKey: rand.Text()})
    if err != nil {
        return err
    }
    defer interaction.Close()

    outcome, err := interaction.Result(ctx) // no stream loop is required
    if err != nil {
        return err
    }
    if outcome.Status() != generated.RunStatusCompleted {
        return fmt.Errorf("run %s ended with status %s", outcome.Run.ID, outcome.Status())
    }
    messages, err := outcome.Run.Items(ctx)
    if err != nil {
        return err
    }
    encoded, err := json.MarshalIndent(messages.Value, "", "  ")
    if err != nil {
        return err
    }
    fmt.Printf("Thread: %s\nOutput: %s\n", interaction.Thread.ID, encoded)
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

This example prints saved messages and tool activity as JSON. `outcome.Output()` is an optional Run output value; a completed conversation can have messages without that value.

This example needs a reachable Service and Agent; `go run` does not start one. `rand.Text()` gives each new submission its own request key. For a *retry of the same logical submission*, reuse its original key and reconcile the response rather than generating a new one.

## Continue or stream

Save the printed Thread ID. A later request can continue it with an explicitly chosen Agent:

```go
followUp, err := client.Agent(agentID).Send(ctx, threadID, "What are the trade-offs?",
    a13n.SendOptions{RequestKey: rand.Text()})
if err != nil { return err }
defer followUp.Close()
followUpOutcome, err := followUp.Result(ctx)
if err != nil { return err }
if followUpOutcome.Status() != generated.RunStatusCompleted {
    return fmt.Errorf("run %s ended %s", followUpOutcome.Run.ID, followUpOutcome.Status())
}
messages, err := followUpOutcome.Run.Items(ctx)
if err != nil { return err }
fmt.Println(messages.Value.Items)
```

To show **provisional** frames as they arrive, call `Next()` *before* `Result(ctx)` on the same interaction:

```go
for {
    frame, err := interaction.Next()
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    if delta, ok := frame.(a13n.DeltaFrame); ok { fmt.Println(delta.Event) }
}
outcome, err := interaction.Result(ctx) // use the final result even if no frames arrived
if err != nil { return err }
if outcome.Status() != generated.RunStatusCompleted {
    return fmt.Errorf("run %s ended %s", outcome.Run.ID, outcome.Status())
}
items, err := outcome.Run.Items(ctx)
if err != nil { return err }
fmt.Println(items.Value.Items)
```

Add `errors` and `io` to your imports for that loop. A Run can finish before the stream attaches, so zero frames is valid. Always `Close()` the interaction, even when you stop reading early; that does not interrupt the remote Run. Reuse a fresh context for a later `Send` if the first request's deadline has passed.

## Go further

- [Application guide](docs/README.md): task-based chapters for [conversations](docs/agents-and-conversations.md), [streaming/readback](docs/streaming-and-readback.md), [waiting and tools](docs/waiting-and-tools.md), [files and Memory](docs/files-and-memory.md), [authentication](docs/authentication.md), [generated API](docs/generated-api.md), and [recovery](docs/errors-and-recovery.md).
- [Compiled examples](example_test.go) and [SDK contract](spec/README.md): exact types and lifecycle guarantees.
- [Pinned Service API](contract/openapi.json) and [provenance](contract/README.md): what this checkout was generated from. `client.API()` exposes its complete generated low-level API; [contribution guide](CONTRIBUTING.md) covers generation and validation.

For result-only applications, `Start`/`Send` plus `Result` is enough. `StartOptions.MessageHistory` can import completed Pydantic AI conversation JSON when creating a new Thread; later `Send` calls do not reseed it. A queued submission might wait before it runs; a waiting Run requires a complete, explicit batch of approval decisions and call results. `Run.Resume(ctx, generated.Resume{Approvals: ..., Calls: ..., Input: ...}, key)` may include an *additional* typed message payload atomically in that same successor Run. It does not answer a missing call or approve one implicitly. A timeout or broken connection does **not** prove the remote submission failed, and the SDK does not automatically retry it.

`Run.Items(ctx)` reads a recent committed display **window**, not automatically all history. Its default `Baseline=true` response includes the whole mutable tail even beyond the Service's limit (default 200, maximum 500), native normalization `Continuation`, nullable `Position` and optional covered `ResumeAfter`. Use one optional `a13n.RunItemsOptions{Before: &ordinal, Limit: &limit}` or After for explicit ordinal history windows; these have no live baseline or coverage. `Complete` means sealed, not all history loaded. See [ordinal windows and recovery](docs/streaming-and-readback.md#read-one-ordinal-history-window).

A Thread's `LastRunId` is its latest sealed Run of any outcome. Continue completed, failed or cancelled history with normal explicit `Send`; failed/cancelled outcomes stop automatic advancement but do not discard history. Resume is only for the exact idle waiting last Run with full approvals/calls, not a generic retry.

## License

Apache License 2.0.
