package a13n

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const boundaryEvent = "id: 100-1\nevent: boundary\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":2}\n\n"
const gapEvent = "event: gap\ndata: {\"run_id\":\"run\"}\n\n"

func TestThreadParserVariantsAndLineEndings(t *testing.T) {
	data := "\ufeff: keepalive\n\n" + boundaryEvent +
		"id: 100-2\nevent: delta\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":3,\n" +
		"data: \"event\":{\"type\":\"custom\"},\"item\":null}\n\n" +
		"event: changed\ndata: {\"version\":4}\n\n" + gapEvent + "event: reset\ndata: {\"run_id\":\"run\"}\n\n"
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		parser := sseParser{reader: bufio.NewReader(strings.NewReader(strings.ReplaceAll(data, "\n", newline))), limit: 1 << 20, first: true}
		for i, expected := range []string{"boundary", "delta", "changed", "gap", "reset"} {
			frame, err := parser.next()
			if err != nil || frame.EventType() != expected {
				t.Fatalf("frame %d: %v %v", i, frame, err)
			}
			if i >= 2 && frame.Cursor() != "" {
				t.Fatal("hint acquired cursor")
			}
			if delta, ok := frame.(DeltaFrame); ok && (delta.RunID != "run" || delta.Sequence != 3 || string(delta.Event["type"]) != `"custom"`) {
				t.Fatalf("typed delta: %#v", delta)
			}
		}
		if _, err := parser.next(); err != io.EOF {
			t.Fatal(err)
		}
	}
}
func TestThreadParserRejectsMalformedFrames(t *testing.T) {
	cases := []string{
		strings.TrimSuffix(boundaryEvent, "\n"),
		"event: boundary\ndata: {}\n\n",
		strings.Replace(boundaryEvent, "100-1", "wrong", 1),
		strings.Replace(boundaryEvent, "\"attempt\":1", "\"attempt\":true", 1),
		"event: changed\nid: 100-1\ndata: {\"version\":2}\n\n",
		"event: changed\ndata: {\"version\":null}\n\n",
		"event: delta\nid: 100-2\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":2,\"event\":{}}\n\n",
		"event: delta\nid: 100-2\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":2,\"event\":{},\"item\":{\"id\":\"item\",\"kind\":\"wrong\",\"state\":\"completed\"}}\n\n",
		"data: \xff\n\n",
		"event: unknown\ndata: {}\n\n",
		"data: " + strings.Repeat("x", 2048) + "\n\n",
	}
	for _, input := range cases {
		p := sseParser{reader: bufio.NewReader(strings.NewReader(input)), limit: 1024, first: true}
		if _, err := p.next(); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted %q: %v", input, err)
		}
	}
}
func TestStreamReconnectUsesOnlyAppliedCursor(t *testing.T) {
	var headers []string
	client := coverageClient(t, func(req *http.Request) *http.Response {
		headers = append(headers, req.Header.Get("Last-Event-ID"))
		response := coverageResponse(200, boundaryEvent)
		if len(headers) > 1 {
			response = coverageResponse(200, gapEvent)
		}
		response.Header.Set("Content-Type", "text/event-stream")
		return response
	})
	stream, err := client.Thread("thr").events(context.Background(), StreamOptions{MaxReconnects: 1, ReconnectDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	frame, err := stream.Next()
	if err != nil || frame.Cursor() != "100-1" || stream.AppliedCursor() != "" || stream.LastReceivedCursor() != "100-1" {
		t.Fatal(frame, err)
	}
	frame, err = stream.Next()
	if err != nil || frame.EventType() != "gap" || stream.AppliedCursor() != "100-1" {
		t.Fatal(frame, err)
	}
	if len(headers) != 2 || headers[1] != "100-1" {
		t.Fatal(headers)
	}
	_, err = stream.Next()
	if !errors.Is(err, ErrTransport) || len(headers) != 2 {
		t.Fatalf("gap-only recovery reset budget: %v %v", err, headers)
	}
}
func TestStreamCloseDoesNotAcknowledgeOrInterrupt(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		if req.Method != "GET" {
			t.Fatal("stream issued mutation")
		}
		response := coverageResponse(200, boundaryEvent)
		response.Header.Set("Content-Type", "text/event-stream")
		return response
	})
	stream, err := client.Thread("thr").events(context.Background(), StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if stream.AppliedCursor() != "" || calls != 1 {
		t.Fatal("close acknowledged or interrupted")
	}
	if _, err := stream.Next(); err != io.EOF {
		t.Fatal(err)
	}
}
func TestStreamAuthorizationIsNotRetried(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		return coverageResponse(403, `{"error":{"code":"forbidden","message":"denied"}}`)
	})
	_, err := client.Thread("thr").events(context.Background(), StreamOptions{MaxReconnects: 3, ReconnectDelay: time.Millisecond})
	var api *ApiError
	if !errors.As(err, &api) || api.Status != 403 || calls != 1 {
		t.Fatalf("authorization: %v %d", err, calls)
	}
}
func TestStreamCloseCancelsPendingRead(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("token"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream, err := client.Thread("thr").events(context.Background(), StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := stream.Next(); done <- err }()
	_ = stream.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read leaked")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected reconnect")
	}
}

func TestStreamLifetimeStopsBufferedFrames(t *testing.T) {
	for _, mode := range []string{"context", "client"} {
		t.Run(mode, func(t *testing.T) {
			client := coverageClient(t, func(req *http.Request) *http.Response {
				response := coverageResponse(200, boundaryEvent+strings.ReplaceAll(boundaryEvent, "100-1", "100-2"))
				response.Header.Set("Content-Type", "text/event-stream")
				return response
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := client.Thread("thr").events(ctx, StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err := stream.Next(); err != nil {
				t.Fatal(err)
			}
			expected := context.Canceled
			if mode == "client" {
				_ = client.Close()
				expected = ErrClosed
			} else {
				cancel()
			}
			if frame, err := stream.Next(); frame != nil || !errors.Is(err, expected) {
				t.Fatalf("buffered frame after shutdown: %v %v", frame, err)
			}
			if stream.AppliedCursor() != "" {
				t.Fatal("shutdown acknowledged pending frame")
			}
		})
	}
}
