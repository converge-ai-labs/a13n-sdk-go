package a13n

import (
	"context"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"io"
	"net/http"
	"strings"
	"sync"
)

// API exposes generated protocol operations on the same transport and lifetime.
// Raw calls return caller-owned bodies; WithResponse buffers success and failure
// bodies according to the generated parser, not the resource JSON bound.
func (c *Client) API() (*generated.ClientWithResponses, error) {
	if c.lifetime.Err() != nil {
		return nil, ErrClosed
	}
	return c.api, nil
}

type apiTransport struct{ client *Client }

func (t apiTransport) Do(req *http.Request) (*http.Response, error) {
	c := t.client
	if c.lifetime.Err() != nil {
		return nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(c.lifetime, cancel)
	cleanup := func() { stop(); cancel() }
	req = req.Clone(ctx)
	c.tokenMu.RLock()
	token := c.token.value
	c.tokenMu.RUnlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	write := req.Method != "GET" && req.Method != "HEAD" && req.Method != "OPTIONS"
	if write {
		// Go's transport treats Idempotency-Key as replay permission. An SDK
		// receipt key is not permission to replay an uncertain mutation.
		req.GetBody = nil
		if req.Body == nil || req.Body == http.NoBody {
			req.Body = io.NopCloser(strings.NewReader(""))
			req.ContentLength = -1
		}
		if token == "" && c.csrf != nil {
			if csrf := c.csrf(); csrf != "" {
				req.Header.Set("X-CSRF-Token", csrf)
			}
		}
	}
	response, err := c.http.Do(req)
	if err != nil {
		outcome := transportError(ctx, c, err, "request")
		cleanup()
		return nil, outcome
	}
	response.Body = &apiBody{ReadCloser: response.Body, ctx: ctx, client: c, cleanup: cleanup}
	return response, nil
}
func transportError(ctx context.Context, c *Client, err error, stage string) error {
	if c.lifetime.Err() != nil {
		return ErrClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &TransportError{Stage: stage, Kind: transportKind(err)}
}

type apiBody struct {
	io.ReadCloser
	ctx     context.Context
	client  *Client
	cleanup func()
	once    sync.Once
}

func (b *apiBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		if err != io.EOF {
			err = transportError(b.ctx, b.client, err, "body")
		}
		b.once.Do(b.cleanup)
	}
	return n, err
}
func (b *apiBody) Close() error {
	b.once.Do(b.cleanup)
	return b.ReadCloser.Close()
}
