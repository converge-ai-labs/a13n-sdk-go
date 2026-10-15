package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated/sessionstream"
)

func sessionStreamOffline() {
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		connections++
		check(req.URL.Path == "/api/v1/sessions/session/stream" && req.URL.RawQuery == "" && req.Header.Get("Last-Event-ID") == "", "installed Session stream has no progress claim")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-ID", "session-native")
		_, _ = io.WriteString(w, "event: future\ndata: not JSON\n\nevent: thread\ndata: {\"thread\":{\"id\":\"thr\",\"workspace_id\":\"value\",\"session_id\":\"ses\",\"origin\":\"new\",\"origin_thread_id\":null,\"origin_run_id\":null,\"origin_tool_call_id\":null,\"subagent\":null,\"current_run_id\":null,\"last_run_id\":null,\"mcp_headers\":{},\"labels\":{},\"archived_at\":null,\"version\":0,\"created_at\":\"2026-10-15T00:00:00Z\",\"updated_at\":\"2026-10-15T00:00:00Z\"},\"current_run\":null,\"last_run\":null,\"entries\":[]}\n\nevent: items\ndata: {\"thread_id\":\"thread\",\"run_id\":\"run\",\"first\":1,\"items\":[],\"continuation\":{\"run_id\":\"run\",\"position\":{\"attempt\":1,\"sequence\":8},\"next_ordinal\":1},\"complete\":false}\n\nevent: delta\ndata: {\"thread_id\":\"thread\",\"run_id\":\"run\",\"attempt\":1,\"sequence\":9,\"event\":{\"type\":\"CUSTOM\",\"value\":null},\"item\":null}\n\nevent: gap\ndata: {\"thread_id\":\"thread\",\"run_id\":\"run\"}\n\n")
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
	defer client.Close()
	for connection := 1; connection <= 2; connection++ {
		stream := must(client.SessionEvents(context.Background(), "session", a13n.SessionStreamOptions{}))
		status, headers := stream.Response()
		check(status == 200 && headers.Get("X-Request-ID") == "session-native", "installed Session HTTP metadata")
		for _, name := range []string{"thread", "items", "delta", "gap"} {
			frame := must(stream.Next())
			check(frame.EventType() == name && len(frame.RawJSON()) > 0, "installed typed Session frame")
			if snapshot, ok := frame.(*a13n.SessionItemsFrame); ok {
				var continuation sessionstream.DisplayContinuation = must(snapshot.Continuation.Get())
				check(continuation.Position.Sequence == 8, "installed generated Session graph included in ZIP")
			}
		}
		_, err := stream.Next()
		check(errors.Is(err, io.EOF) && connections == connection, "installed Session EOF never auto-reconnects")
		_ = stream.Close()
	}
	fmt.Println("Installed module: four native Session frames, ignored unknown event, continuation package, EOF and fresh explicit reopen passed (local mock)")
}

func sessionStreamLive(ctx context.Context, client *a13n.Client, sessionID, runID string) {
	for range 2 {
		readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		stream := must(client.SessionEvents(readCtx, sessionID, a13n.SessionStreamOptions{}))
		status, _ := stream.Response()
		check(status == 200, "live Session SSE HTTP status")
		seenRun := false
		for !seenRun {
			frame := must(stream.Next())
			if value, ok := frame.(*a13n.SessionThreadFrame); ok {
				if last, err := value.LastRun.Get(); err == nil && last.Id == runID {
					check(last.Status == sessionstream.RunStatusCompleted, "live Session last sealed exact Run")
					seenRun = true
				}
			}
		}
		_ = stream.Close()
		cancel()
	}
	fmt.Println("Verified HTTPS: native Session thread/last-Run snapshot and explicit fresh-snapshot reopen passed; sealed Runs do not replay items, live delta/gap not claimed")
}
