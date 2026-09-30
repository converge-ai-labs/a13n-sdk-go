package a13n

import (
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

func TestInteractionImmediateResultNeedsNoStream(t *testing.T) {
	var streams, posts atomic.Int32
	client := coverageClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == "POST":
			posts.Add(1)
			return coverageResponse(201, queuedReceipt)
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			return coverageResponse(200, entryView("consumed", `"run"`))
		case strings.HasSuffix(req.URL.Path, "/runs/run"):
			return coverageResponse(200, runView("run", "completed", "thr"))
		default:
			streams.Add(1)
			return coverageResponse(500, `{"error":{"code":"unexpected","message":"stream"}}`)
		}
	})
	i, err := client.Agent("agent").Start(context.Background(), "hello", StartOptions{RequestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	outcome, err := i.Result(context.Background())
	if err != nil || outcome.Run.ID != "run" {
		t.Fatal(outcome, err)
	}
	frame, err := i.Next()
	if frame != nil || !errors.Is(err, io.EOF) || streams.Load() != 0 || posts.Load() != 1 {
		t.Fatalf("completed before attach: %v %v streams=%d", frame, err, streams.Load())
	}
}

func TestInteractionOwnFramesThenIdleSSETerminates(t *testing.T) {
	var sealed atomic.Bool
	var posts, streams atomic.Int32
	attached := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "POST":
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			_, _ = w.Write([]byte(queuedReceipt))
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(entryView("consumed", `"run"`)))
		case strings.HasSuffix(req.URL.Path, "/runs/run"):
			w.Header().Set("Content-Type", "application/json")
			if sealed.Load() {
				_, _ = w.Write([]byte(runView("run", "waiting", "thr")))
			} else {
				_, _ = w.Write([]byte(runView("run", "running", "thr")))
			}
		case strings.HasSuffix(req.URL.Path, "/stream"):
			if req.URL.Query().Has("run") || req.URL.Query().Has("position") {
				t.Error("finite stream added an implicit coverage baseline")
			}
			streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("id: 1-1\nevent: boundary\ndata: {\"run_id\":\"other\",\"attempt\":1,\"sequence\":1}\n\n" + boundaryEvent + "event: changed\ndata: {\"version\":3}\n\n"))
			w.(http.Flusher).Flush()
			close(attached)
			<-req.Context().Done()
		default:
			t.Errorf("unexpected request: %s", req.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	i, err := client.Agent("agent").Start(context.Background(), "hello", StartOptions{RequestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	frame, err := i.Next()
	if err != nil {
		t.Fatal(err)
	}
	if boundary, ok := frame.(BoundaryFrame); !ok || boundary.RunID != "run" {
		t.Fatalf("other Run leaked: %v", frame)
	}
	<-attached
	sealed.Store(true)
	next := make(chan error, 1)
	go func() { _, err := i.Next(); next <- err }()
	select {
	case err := <-next:
		if !errors.Is(err, io.EOF) {
			t.Fatal("idle stream didn't terminate:", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle stream hung after Run sealed")
	}
	outcome, err := i.Result(context.Background())
	if err != nil || outcome.Run.ID != "run" || outcome.Status() != "waiting" || posts.Load() != 1 || streams.Load() != 1 {
		t.Fatal(outcome, err)
	}
}

func TestInteractionTerminalCancelsStalledStreamHeaders(t *testing.T) {
	var sealed atomic.Bool
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "POST":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			_, _ = w.Write([]byte(queuedReceipt))
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(entryView("consumed", `"run"`)))
		case strings.HasSuffix(req.URL.Path, "/runs/run"):
			w.Header().Set("Content-Type", "application/json")
			if sealed.Load() {
				_, _ = w.Write([]byte(runView("run", "completed", "thr")))
			} else {
				_, _ = w.Write([]byte(runView("run", "running", "thr")))
			}
		case strings.HasSuffix(req.URL.Path, "/stream"):
			close(entered)
			<-req.Context().Done() // no headers sent
		default:
			t.Errorf("unexpected request: %s", req.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	i, err := client.Agent("agent").Start(context.Background(), "hello", StartOptions{RequestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	next := make(chan error, 1)
	go func() { _, err := i.Next(); next <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stream did not attach")
	}
	sealed.Store(true)
	select {
	case err := <-next:
		if !errors.Is(err, io.EOF) {
			t.Fatal("terminal during header attach:", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE header attach hung after Run sealed")
	}
	if outcome, err := i.Result(context.Background()); err != nil || outcome.Run.ID != "run" {
		t.Fatal(outcome, err)
	}
}

func TestInteractionCloseStopsQueuedNextWithoutRemoteMutation(t *testing.T) {
	var posts atomic.Int32
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method == "POST" {
			posts.Add(1)
			return coverageResponse(201, queuedReceipt)
		}
		return coverageResponse(200, entryView("queued", "null"))
	})
	i, err := client.Agent("agent").Start(context.Background(), "hello", StartOptions{RequestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() { _, err := i.Next(); waiting <- err }()
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Next remained blocked after Close")
	}
	if _, err := i.Result(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if posts.Load() != 1 {
		t.Fatal("Close issued remote mutation")
	}
}
