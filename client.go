package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// Secret redacts diagnostics; only the transport serializes its credential.
type Secret struct{ value string }

func NewSecret(value string) Secret         { return Secret{value} }
func (Secret) String() string               { return "[REDACTED]" }
func (Secret) GoString() string             { return "a13n.Secret([REDACTED])" }
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }

var ErrTransport = errors.New("service transport failed; mutation outcome may be unknown")
var ErrProtocol = errors.New("invalid or oversized Service response")
var ErrClosed = errors.New("client is closed")

// ApiError retains reconciliation evidence without including response bodies in diagnostics.
type ApiError struct {
	Status     int
	Code       string
	Message    string
	Details    map[string]json.RawMessage
	RequestID  string
	RetryAfter string
	Header     http.Header
}

func (err *ApiError) Error() string { return err.Code + ": " + err.Message }

// Client owns one transport and cancellation lifetime. Contexts bound individual
// operations, including response delivery. Close never interrupts a server Run.
type Client struct {
	baseURL       string
	token         Secret
	http          *http.Client
	api           *generated.ClientWithResponses
	lifetime      context.Context
	cancel        context.CancelFunc
	once          sync.Once
	tokenMu       sync.RWMutex
	csrf          func() string
	responseLimit int64
}

type ClientOption func(*Client) error

// WithSession enables a caller-supplied cookie jar and current CSRF token source.
// The callback may be called concurrently; it is not called in bearer mode.
func WithSession(jar http.CookieJar, csrf func() string) ClientOption {
	return func(c *Client) error {
		if jar == nil || c.token.value != "" {
			return errors.New("session authentication requires a cookie jar and no bearer token")
		}
		c.http.Jar, c.csrf = jar, csrf
		return nil
	}
}

// WithResponseLimit sets the bound for buffered JSON, never binary downloads.
func WithResponseLimit(bytes int64) ClientOption {
	return func(c *Client) error {
		if bytes <= 0 {
			return errors.New("response limit must be positive")
		}
		c.responseLimit = bytes
		return nil
	}
}

// NewClient accepts the Service origin with an optional proxy path (not /api/v1).
// A supplied transport is owned by this client. Requests are never replayed by
// the SDK; caller contexts supply deadlines rather than a whole-stream timeout.
func NewClient(baseURL string, token Secret, transport http.RoundTripper, options ...ClientOption) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("baseURL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	lifetime, cancel := context.WithCancel(context.Background())
	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, lifetime: lifetime, cancel: cancel, responseLimit: 16 << 20,
		http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, option := range options {
		if err := option(c); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	c.api, err = generated.NewClientWithResponses(c.baseURL, generated.WithHTTPClient(apiTransport{c}))
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.tokenMu.Lock()
		c.token = Secret{}
		c.tokenMu.Unlock()
		c.http.CloseIdleConnections()
	})
	return nil
}

// Resources is the complete generated ordinary resource surface. Binding is local.
func (c *Client) Resources() ServiceResources { return ServiceResources{binding{client: c}} }
