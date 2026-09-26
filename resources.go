package a13n

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
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

type binding struct {
	client *Client
	ids    []string
}

func (r binding) selectID(id string) binding {
	return binding{r.client, append(slices.Clone(r.ids), id)}
}
func (r binding) integerID(index int) int { value, _ := strconv.Atoi(r.ids[index]); return value }
func (r binding) validate() error {
	if r.client == nil {
		return fmt.Errorf("resource is not bound to a client")
	}
	for _, id := range r.ids {
		if id == "" || id == "." || id == ".." {
			return fmt.Errorf("resource selectors must be nonempty and not dot segments")
		}
	}
	return nil
}

func readJSON(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrProtocol
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
	if json.Unmarshal(data, &envelope) != nil || envelope.Error.Code == "" {
		return ErrProtocol
	}
	requestID := envelope.Error.RequestID
	if requestID == "" {
		requestID = response.Header.Get("X-Request-ID")
	}
	return &ApiError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message,
		Details: envelope.Error.Details, RequestID: requestID, RetryAfter: response.Header.Get("Retry-After"), Header: response.Header.Clone()}
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
	if contentType != "application/json" || len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, &result.Value) != nil {
		return result, ErrProtocol
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

// paginate returns one page at a time, retaining each response's headers. A new
// iteration restarts at the supplied cursor; it never buffers all pages.
func paginate[O, T any](ctx context.Context, options O, getCursor func(O) *string, setCursor func(*O, string), fetch func(context.Context, O) (Result[T], error), next func(T) string) iter.Seq2[Result[T], error] {
	return func(yield func(Result[T], error) bool) {
		local := options
		seen := map[string]bool{}
		if initial := getCursor(local); initial != nil {
			seen[*initial] = true
		}
		for {
			page, err := fetch(ctx, local)
			if err != nil {
				yield(page, err)
				return
			}
			cursor := next(page.Value)
			if !yield(page, nil) || cursor == "" {
				return
			}
			if seen[cursor] {
				yield(Result[T]{}, fmt.Errorf("%w: repeated pagination cursor", ErrProtocol))
				return
			}
			seen[cursor] = true
			setCursor(&local, cursor)
		}
	}
}
