package a13n

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
)

// UnknownOutcomeError means dispatch was attempted without a reliable receipt.
// The key and known identities are explicit reconciliation evidence, not retry
// permission. Error strings omit arbitrary IDs, keys, bodies and causes.
type UnknownOutcomeError struct {
	IdempotencyKey string
	Operation      string
	ThreadID       string
	RunID          string
	StatusCode     int
	Header         http.Header
	Cause          error
}

func (e *UnknownOutcomeError) Error() string {
	return fmt.Sprintf("%s outcome is unknown; reconcile using the original idempotency key", e.Operation)
}
func (e *UnknownOutcomeError) GoString() string { return e.Error() }
func (e *UnknownOutcomeError) Unwrap() error    { return e.Cause }

type dispatchKey struct{}
type dispatchEvidence struct {
	attempted bool
	status    int
	header    http.Header
}

func mutationContext(ctx context.Context, c *Client, key string) (context.Context, *dispatchEvidence, string, error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, key, err
	}
	if c.lifetime.Err() != nil {
		return ctx, nil, key, ErrClosed
	}
	if key == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return ctx, nil, key, err
		}
		key = hex.EncodeToString(random[:])
	}
	evidence := &dispatchEvidence{}
	return context.WithValue(ctx, dispatchKey{}, evidence), evidence, key, nil
}
func mutationError(err error, evidence *dispatchEvidence, key, operation, threadID, runID string) error {
	if err == nil {
		return nil
	}
	var rejection *ApiError
	if evidence == nil || !evidence.attempted || errors.As(err, &rejection) {
		return err
	}
	return &UnknownOutcomeError{IdempotencyKey: key, Operation: operation, ThreadID: threadID, RunID: runID, StatusCode: evidence.status, Header: evidence.header, Cause: err}
}
