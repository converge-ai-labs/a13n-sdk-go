package a13n

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func healthCheck(client *Client) (Result[map[string]any], error) {
	response, err := client.api.HealthHealthzGet(context.Background())
	return jsonResult[map[string]any](client, response, err, 200)
}

func TestTransportDiagnostics(t *testing.T) {
	secret := "private-credential-in-error"
	cases := []struct {
		name  string
		cause error
		kind  string
	}{
		{"dns", &net.DNSError{Name: secret, Err: secret}, "dns"},
		{"hostname", x509.HostnameError{Certificate: &x509.Certificate{}, Host: secret}, "tls"},
		{"certificate", &tls.CertificateVerificationError{Err: errors.New(secret)}, "tls"},
		{"timeout", &net.OpError{Op: secret, Err: context.DeadlineExceeded}, "timeout"},
		{"network", &net.OpError{Op: secret, Err: errors.New(secret)}, "network"},
		{"truncated", io.ErrUnexpectedEOF, "unexpected_eof"},
		{"custom", errors.New(secret), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient("https://service.test", NewSecret(secret), roundTrip(func(*http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: secret, URL: "https://service.test/?token=" + secret, Err: tc.cause}
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = healthCheck(client)
			var diagnostic *TransportError
			if !errors.Is(err, ErrTransport) || !errors.As(err, &diagnostic) || diagnostic.Kind != tc.kind || diagnostic.Stage != "request" {
				t.Fatalf("classification: %v", err)
			}
			if errors.Unwrap(diagnostic) != ErrTransport {
				t.Fatal("raw cause retained")
			}
			for _, format := range []string{"%v", "%+v", "%#v"} {
				if strings.Contains(fmt.Sprintf(format, err), secret) {
					t.Fatal("sensitive transport diagnostics")
				}
			}
		})
	}
}

type failingBody struct{ closed bool }

func (*failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (b *failingBody) Close() error           { b.closed = true; return nil }

func TestBodyDiagnosticsAndErrorIdentity(t *testing.T) {
	body := &failingBody{}
	client := coverageClient(t, func(*http.Request) *http.Response {
		response := coverageResponse(200, "")
		response.Body = body
		return response
	})
	_, err := healthCheck(client)
	var diagnostic *TransportError
	if !errors.As(err, &diagnostic) || diagnostic.Stage != "body" || diagnostic.Kind != "unexpected_eof" || !body.closed {
		t.Fatalf("body error: %v, closed=%v", err, body.closed)
	}
	for _, cancel := range []bool{false, true} {
		ctx, stop := context.WithCancel(context.Background())
		if cancel {
			stop()
		} else {
			_ = client.Close()
		}
		err := transportError(ctx, client, errors.New("private"), "request")
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("closed identity: %v", err)
		}
		stop()
	}
	other := coverageClient(t, func(*http.Request) *http.Response { return coverageResponse(200, "{}") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if transportError(ctx, other, errors.New("private"), "body") != context.Canceled {
		t.Fatal("context identity changed")
	}
}

func TestProtocolDiagnostics(t *testing.T) {
	cases := []struct {
		name, contentType, body, kind string
		status                        int
	}{
		{"size", "application/json", strings.Repeat("x", 100), "size_limit", 200},
		{"content", "text/html", "private", "content_type", 200},
		{"empty", "application/json", " ", "empty_body", 200},
		{"null", "application/json", "null", "null_body", 200},
		{"json", "application/json", "{private", "invalid_json", 200},
		{"error-json", "application/json", "private", "invalid_json", 502},
		{"error-envelope", "application/json", `{}`, "error_envelope", 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := coverageClient(t, func(*http.Request) *http.Response {
				response := coverageResponse(tc.status, tc.body)
				response.Header.Set("Content-Type", tc.contentType)
				response.Header.Set("X-Request-ID", "private-request-id")
				return response
			})
			client.responseLimit = 64
			_, err := healthCheck(client)
			var diagnostic *ProtocolError
			if !errors.Is(err, ErrProtocol) || !errors.As(err, &diagnostic) || diagnostic.Kind != tc.kind || diagnostic.StatusCode != tc.status || diagnostic.RequestID != "private-request-id" {
				t.Fatalf("response evidence: %v", err)
			}
			for _, format := range []string{"%v", "%+v", "%#v"} {
				if strings.Contains(fmt.Sprintf(format, err), "private") {
					t.Fatal("sensitive response diagnostics")
				}
			}
		})
	}
}
