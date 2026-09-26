package a13n

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type ownedInput struct {
	io.Reader
	closed bool
}

func (r *ownedInput) Close() error { r.closed = true; return nil }
func TestUploadAndImagePreserveCallerOwnership(t *testing.T) {
	bytesIn := strings.Repeat("x", 300000)
	input := &ownedInput{Reader: strings.NewReader(bytesIn)}
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if strings.HasSuffix(req.URL.Path, "/uploads") {
			kind, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
			if err != nil || kind != "multipart/form-data" {
				t.Fatal(kind, err)
			}
			reader := multipart.NewReader(req.Body, params["boundary"])
			part, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(part)
			if err != nil || string(data) != bytesIn || part.FileName() != "notes.txt" || part.Header.Get("Content-Type") != "text/plain" {
				t.Fatal("multipart data mismatch", err)
			}
			if _, err := reader.NextPart(); err != io.EOF {
				t.Fatal(err)
			}
			_ = req.Body.Close()
			return coverageResponse(200, `{"id":"upl","size":300000}`)
		}
		data, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil || string(data) != "image" || req.Header.Get("Content-Type") != "image/png" {
			t.Fatal("image media mismatch", err)
		}
		return coverageResponse(200, `{}`)
	})
	workspace := client.Resources().Workspaces().Ref("ws")
	_, err := workspace.Uploads().Create(context.Background(), UploadFile{Name: "notes.txt", ContentType: "text/plain", Reader: input}, UploadsCreateOptions{IdempotencyKey: "upload"})
	if err != nil || input.closed {
		t.Fatal("input closed", err)
	}
	image := &ownedInput{Reader: strings.NewReader("image")}
	_, err = workspace.Icon().Replace(context.Background(), image, IconReplaceOptions{IfMatch: `"v1"`, ContentType: "image/png"})
	if err != nil || image.closed {
		t.Fatal("image input closed", err)
	}
}
func TestBinaryDeliveryRemainsUnbufferedAndCancellable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("test"), nil, WithResponseLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := client.Resources().Workspaces().Ref("ws").Assets().Ref("asset").Content().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(result.Body, buf); err != nil || !bytes.Equal(buf, []byte("first")) {
		t.Fatal(err, string(buf))
	}
	cancel()
	if _, err := result.Body.Read(buf); !errors.Is(err, context.Canceled) {
		t.Fatalf("body cancellation: %v", err)
	}
	_ = result.Close()
}
func TestJSONBoundAndCallbackRedirect(t *testing.T) {
	client, err := NewClient("https://service.test", NewSecret("test"), roundTrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/callback") {
			response := coverageResponse(303, "")
			response.Header.Set("Location", "https://app.test/return")
			return response, nil
		}
		return coverageResponse(200, `{"name":"too much"}`), nil
	}), WithResponseLimit(8))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Resources().Workspaces().Ref("ws").Get(context.Background())
	if !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	redirect, err := client.Resources().Connections().Callback().Get(context.Background(), ConnectionsCallbackGetOptions{State: "state"})
	if err != nil || redirect.StatusCode != 303 || redirect.Header.Get("Location") != "https://app.test/return" {
		t.Fatal(redirect, err)
	}
}
