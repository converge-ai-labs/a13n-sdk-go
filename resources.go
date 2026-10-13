package a13n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strings"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// Result is an observed representation, never a live resource mirror.
type Result[T any] struct {
	Value      T
	StatusCode int
	Header     http.Header
	raw        []byte
}

// RawJSON returns independent original response bytes for exact application decoding.
func (r Result[T]) RawJSON() []byte { return slices.Clone(r.raw) }

func (r Result[T]) ETag() string      { return r.Header.Get("ETag") }
func (r Result[T]) RequestID() string { return r.Header.Get("X-Request-ID") }

// BinaryResult is unbuffered. The caller must close Body or the result itself.
type BinaryResult struct {
	Body       io.ReadCloser
	StatusCode int
	Header     http.Header
}

func (r *BinaryResult) Close() error { return r.Body.Close() }

func readJSON(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, protocolError(response, "size_limit")
	}
	return data, nil
}
func apiFailure(response *http.Response, limit int64) error {
	data, err := readJSON(response, limit)
	if err != nil {
		return err
	}
	var envelope struct {
		Error struct {
			Code      *string                    `json:"code"`
			Message   *string                    `json:"message"`
			Details   map[string]json.RawMessage `json:"details"`
			RequestID json.RawMessage            `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return protocolError(response, "invalid_json")
	}
	// Only a complete canonical Service ErrorBody proves rejection. Gateway
	// JSON with a code alone is not reliable acknowledgement of a mutation.
	if envelope.Error.Code == nil || *envelope.Error.Code == "" || envelope.Error.Message == nil || envelope.Error.Details == nil || len(envelope.Error.RequestID) == 0 {
		return protocolError(response, "error_envelope")
	}
	var requestID string
	if json.Unmarshal(envelope.Error.RequestID, &requestID) != nil {
		return protocolError(response, "error_envelope")
	}
	if requestID == "" {
		requestID = response.Header.Get("X-Request-ID")
	}
	return &ApiError{Status: response.StatusCode, Code: *envelope.Error.Code, Message: *envelope.Error.Message,
		Details: envelope.Error.Details, RequestID: requestID, RetryAfter: response.Header.Get("Retry-After"), Header: response.Header.Clone()}
}

// ParseJSON decodes one raw generated HTTP response with bounded buffering,
// structured Service errors, and status/header evidence. It owns and closes
// the response body. Generated WithResponse methods have separate parsers.
func ParseJSON[T any](client *Client, response *http.Response, err error, success ...int) (Result[T], error) {
	return jsonResult[T](client, response, err, success...)
}

func jsonResult[T any](client *Client, response *http.Response, err error, success ...int) (Result[T], error) {
	var result Result[T]
	if err != nil {
		return result, err
	}
	if !slices.Contains(success, response.StatusCode) {
		return result, apiFailure(response, client.responseLimit)
	}
	result.StatusCode, result.Header = response.StatusCode, response.Header.Clone()
	data, err := readJSON(response, client.responseLimit)
	if err != nil {
		return result, err
	}
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusSeeOther {
		return result, nil
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType != "application/json" {
		return result, protocolError(response, "content_type")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return result, protocolError(response, "empty_body")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return result, protocolError(response, "null_body")
	}
	result.raw = data
	if decodeResourceJSON(data, &result.Value) != nil {
		return result, protocolError(response, "invalid_json")
	}
	return result, nil
}

// The canonical nullable decoder uses float64 for arbitrary JSON. Shadow only
// Run output and saved content at this common response owner so large numbers reach both
// the typed evidence and high-level output without forking generated parsers.
func decodeResourceJSON(data []byte, target any) error {
	switch value := target.(type) {
	case *generated.RunView:
		wire := struct {
			*generated.RunView
			Output json.RawMessage `json:"output"`
		}{RunView: value}
		if err := json.Unmarshal(data, &wire); err != nil {
			return err
		}
		if len(wire.Output) == 0 {
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(wire.Output), []byte("null")) {
			value.Output = Null[any]()
			return nil
		}
		var output any
		decoder := json.NewDecoder(bytes.NewReader(wire.Output))
		decoder.UseNumber()
		if err := decoder.Decode(&output); err != nil {
			return err
		}
		value.Output = Value(output)
		return nil
	case *generated.Submitted:
		wire := struct {
			*generated.Submitted
			Run json.RawMessage `json:"run"`
		}{Submitted: value}
		if err := json.Unmarshal(data, &wire); err != nil {
			return err
		}
		if len(wire.Run) == 0 {
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(wire.Run), []byte("null")) {
			value.Run = Null[generated.RunView]()
			return nil
		}
		var run generated.RunView
		if err := decodeResourceJSON(wire.Run, &run); err != nil {
			return err
		}
		value.Run = Value(run)
		return nil
	case *generated.RunContent:
		if !json.Valid(data) {
			return fmt.Errorf("invalid JSON document")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		return decoder.Decode(value)
	default:
		return json.Unmarshal(data, target)
	}
}

func binaryResult(client *Client, response *http.Response, err error, success ...int) (*BinaryResult, error) {
	if err != nil {
		return nil, err
	}
	if !slices.Contains(success, response.StatusCode) {
		return nil, apiFailure(response, client.responseLimit)
	}
	return &BinaryResult{response.Body, response.StatusCode, response.Header.Clone()}, nil
}

// readerOnly prevents net/http from taking ownership of a caller's ReadCloser.
type readerOnly struct{ io.Reader }

// UploadFile streams caller-owned bytes. Neither success nor failure closes Reader.
type UploadFile struct {
	Name        string
	ContentType string
	Reader      io.Reader
}

func multipartBody(file UploadFile) (string, io.Reader, error) {
	if file.Name == "" || file.Reader == nil {
		return "", nil, fmt.Errorf("upload needs a filename and reader")
	}
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if strings.ContainsAny(contentType, "\r\n") {
		return "", nil, fmt.Errorf("invalid upload content type")
	}
	// Write only framing into memory; stream file bytes without a goroutine or
	// buffering the file, so cancellation cannot strand a producer goroutine.
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": file.Name}))
	header.Set("Content-Type", contentType)
	if _, err := writer.CreatePart(header); err != nil {
		return "", nil, err
	}
	start := slices.Clone(prefix.Bytes())
	prefix.Reset()
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	body := io.MultiReader(bytes.NewReader(start), file.Reader, bytes.NewReader(prefix.Bytes()))
	return writer.FormDataContentType(), body, nil
}
