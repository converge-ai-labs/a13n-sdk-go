# Foundation SDK for Go

Go SDK module for Agent Foundation Service.

## Status

The `v0.0.x` module reserves the stable module and package names while the service API is being designed. It intentionally exposes no client API yet. Generated models and transports will be added only after the service contract is stable enough to support compatibility guarantees.

## Installation

```bash
go get github.com/converge-ai-labs/agent-foundation/sdk/go@latest
```

```go
import foundationsdk "github.com/converge-ai-labs/agent-foundation/sdk/go"
```

Go module releases use canonical module tags in the form `sdk/go/v<version>`, created by the `release/sdk/go/<version>` release workflow. `<version>` is stable `X.Y.Z` or RC `X.Y.Z-rc.N`.

## Development

Run the Go SDK checks from the repository root:

```bash
make sdk-go-check
```

## License

Licensed under the Apache License 2.0.
