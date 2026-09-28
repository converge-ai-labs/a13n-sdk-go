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
)

// Result is an observed representation, never a live resource mirror.
type Result[T any] struct {
	Value      T
	StatusCode int
	Header     http.Header
}

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
			Code      string                     `json:"code"`
			Message   string                     `json:"message"`
			Details   map[string]json.RawMessage `json:"details"`
			RequestID string                     `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return protocolError(response, "invalid_json")
	}
	if envelope.Error.Code == "" {
		return protocolError(response, "error_envelope")
	}
	requestID := envelope.Error.RequestID
	if requestID == "" {
		requestID = response.Header.Get("X-Request-ID")
	}
	return &ApiError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message,
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
	if json.Unmarshal(data, &result.Value) != nil {
		return result, protocolError(response, "invalid_json")
	}
	return result, nil
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
