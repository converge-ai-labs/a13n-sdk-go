package a13n

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

// TransportError classifies a failure without retaining potentially sensitive
// causes, URLs or credentials. Use errors.Is with ErrTransport and errors.As
// with *TransportError. No category proves rollback or permits automatic retry.
type TransportError struct {
	// Stage is "request" (dispatch/headers) or "body" (response delivery).
	Stage string
	// Kind is "dns", "tls", "timeout", "network", "unexpected_eof" or "other".
	Kind string
}

func (err *TransportError) Error() string {
	return fmt.Sprintf("service transport failed (%s: %s)", err.Stage, err.Kind)
}
func (err *TransportError) Unwrap() error { return ErrTransport }

// ProtocolError describes an ordinary resource JSON response failure.
// Use errors.Is with ErrProtocol and errors.As with *ProtocolError.
// Error and GoString omit response bodies and untrusted request IDs.
type ProtocolError struct {
	// Kind is "size_limit", "content_type", "empty_body", "null_body",
	// "invalid_json" or "error_envelope".
	Kind       string
	StatusCode int
	// RequestID is explicit reconciliation metadata, not safe log text.
	RequestID string
}

func (err *ProtocolError) Error() string {
	return fmt.Sprintf("invalid Service response (%s; HTTP %d)", err.Kind, err.StatusCode)
}
func (err *ProtocolError) GoString() string { return err.Error() }
func (err *ProtocolError) Unwrap() error    { return ErrProtocol }

func protocolError(response *http.Response, kind string) error {
	return &ProtocolError{Kind: kind, StatusCode: response.StatusCode, RequestID: response.Header.Get("X-Request-ID")}
}

func transportKind(err error) string {
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var network net.Error
	var operation *net.OpError
	switch {
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &certificate), errors.As(err, &hostname), errors.As(err, &authority), errors.As(err, &invalid), errors.As(err, &record):
		return "tls"
	case errors.As(err, &network) && network.Timeout():
		return "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.As(err, &operation):
		return "network"
	default:
		return "other"
	}
}
