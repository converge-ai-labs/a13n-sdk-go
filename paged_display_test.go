package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// This is normalization continuation from the pinned public schema, not Harness
// execution state. Include recursive child cursors and incomplete fragments.
const displayContinuationJSON = `{"run_id":"run","position":{"attempt":1,"sequence":8},"next_ordinal":11,"full_content":false,"response_groups":{"call":"response"},"arguments":{"key":"part","event":{"type":"CUSTOM","value":null},"stream":{"nested":[false,0,null]},"sequence":8,"at":"2026-10-06T00:00:00Z","size":4},"fragments":{"max_bytes":1024,"max_pending":2,"gap":false,"pending":{"fragment":{"count":2,"parts":["first"],"size":5}}},"observer":{"run_id":null,"thread_id":"thr","state":{"request_index":0,"parts":{"p":{"kind":"text","part_id":"p","emitted_content":false,"emitted_signature":false,"tool_name":null}},"threads":{"child":"child-thread"},"children":{"child":{"children":{"grandchild":{"request_index":0}}}}}}}`

func TestRunItemsOrdinalWindowsAndWholeMutableTail(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reads++
		if req.Method != "GET" || req.URL.Path != "/api/v1/runs/run/items" || req.Header.Get("X-Workspace-ID") != "" {
			t.Errorf("unexpected request: %s %s %v", req.Method, req.URL, req.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "ordinal-window")
		query := req.URL.Query()
		first, last, sealed := 1, 10, false
		baseline := query.Get("before") == "" && query.Get("after") == ""
		switch query.Encode() {
		case "limit=2": // All ten mutable items, not a client-truncated pair.
		case "before=5&limit=2", "after=2&limit=2":
			first, last, sealed = 3, 4, true
		case "": // A sealed recent baseline starts later than ordinal one.
			first, last, sealed = 9, 10, true
		case "after=0&limit=2":
			first, last, sealed = 1, 2, true
		default:
			t.Errorf("unexpected query: %s", query.Encode())
		}
		status, state := "running", "in_progress"
		if sealed {
			status, state = "completed", "interrupted"
		}
		items := make([]map[string]any, 0, last-first+1)
		for ordinal := first; ordinal <= last; ordinal++ {
			items = append(items, map[string]any{"id": fmt.Sprint(ordinal), "ordinal": ordinal, "kind": "text_message", "state": state, "content": map[string]any{"text": "active"}})
		}
		body := map[string]any{"run": map[string]any{"id": "run", "thread_id": "thr", "status": status, "display_position": nil}, "items": items, "baseline": baseline, "complete": sealed, "continuation": nil, "position": nil, "resume_after": nil}
		if baseline {
			body["continuation"] = json.RawMessage(displayContinuationJSON)
			body["position"], body["resume_after"] = "1-8", "99-1"
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	two, five, zero := 2, 5, 0
	baseline, err := client.Run("run").Items(ctx, RunItemsOptions{Limit: &two})
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Value.Baseline || baseline.Value.Complete || len(baseline.Value.Items) != 10 || baseline.RequestID() != "ordinal-window" || !baseline.Value.Run.DisplayPosition.IsNull() {
		t.Fatal(baseline)
	}
	continuation, err := baseline.Value.Continuation.Get()
	if err != nil || continuation.NextOrdinal != 11 || continuation.Position.Sequence != 8 {
		t.Fatal(continuation, err)
	}
	encoded, err := json.Marshal(continuation)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(encoded, &got)
	_ = json.Unmarshal([]byte(displayContinuationJSON), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recursive normalization state lost: %s", encoded)
	}

	for _, options := range []RunItemsOptions{{Before: &five, Limit: &two}, {After: &two, Limit: &two}, {After: &zero, Limit: &two}} {
		window, err := client.Run("run").Items(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		if window.Value.Baseline || !window.Value.Complete || !window.Value.Continuation.IsNull() || !window.Value.Position.IsNull() || !window.Value.ResumeAfter.IsNull() || len(window.Value.Items) != 2 {
			t.Fatal(window)
		}
		if options.After != &zero && window.Value.Items[0].Ordinal != 3 {
			t.Fatal(window)
		}
	}
	recent, err := client.Run("run").Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !recent.Value.Baseline || !recent.Value.Complete || recent.Value.Items[0].Ordinal != 9 || len(recent.Value.Items) != 2 || reads != 5 {
		t.Fatal("sealed recent window must not trigger loading earlier history", recent, reads)
	}
}

func TestRunItemsQueryErrorsRemainServiceOwned(t *testing.T) {
	for _, query := range []string{"before=0", "after=-1", "limit=0", "limit=501", "after=0&before=1"} {
		t.Run(query, func(t *testing.T) {
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if req.URL.Query().Encode() != query {
					t.Fatal(req.URL)
				}
				response := coverageResponse(400, `{"error":{"code":"invalid_argument","message":"invalid ordinal window","details":{"field":"before"}}}`)
				response.Header.Set("X-Request-ID", "invalid-window")
				return response
			})
			zero, one, negative, large := 0, 1, -1, 501
			options := map[string]RunItemsOptions{"before=0": {Before: &zero}, "after=-1": {After: &negative}, "limit=0": {Limit: &zero}, "limit=501": {Limit: &large}, "after=0&before=1": {After: &zero, Before: &one}}[query]
			_, err := client.Run("run").Items(context.Background(), options)
			var api *ApiError
			if !errors.As(err, &api) || api.Code != "invalid_argument" || api.Status != 400 || api.RequestID != "invalid-window" || string(api.Details["field"]) != `"before"` {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamItemRefMetadataAndRequiredNullableItem(t *testing.T) {
	event := `{"type":"CUSTOM","name":"future.native","value":{"parts":[false,0,null]},"subagentRunId":"child","parentSubagentRunId":"parent"}`
	for _, item := range []string{`null`, `{"id":"item","kind":"observation","state":"failed"}`, `{"id":"item","kind":"tool_call","state":"failed","ordinal":null,"response_group":null,"failure":null}`, `{"id":"item","kind":"tool_call","state":"failed","ordinal":3,"response_group":"response","failure":{"code":"native","details":{"zero":0,"enabled":false,"parts":[null,"x"]}}}`} {
		client := coverageClient(t, func(req *http.Request) *http.Response {
			response := coverageResponse(200, fmt.Sprintf("id: 99-1\nevent: delta\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":1,\"event\":%s,\"item\":%s}\n\n", event, item))
			response.Header.Set("Content-Type", "text/event-stream")
			return response
		})
		stream, err := client.Thread("thr").events(context.Background(), StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		frame, err := stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		delta := frame.(DeltaFrame)
		if string(delta.Event["subagentRunId"]) != `"child"` || string(delta.Event["value"]) != `{"parts":[false,0,null]}` {
			t.Fatal(delta)
		}
		encoded, _ := json.Marshal(delta.Item)
		var got, want any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal([]byte(item), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ItemRef optional/null/value lost: %s", encoded)
		}
		if stream.AppliedCursor() != "" {
			t.Fatal("merely received item was acknowledged")
		}
		_ = stream.Close()
		if stream.AppliedCursor() != "" {
			t.Fatal("Close acknowledged metadata frame")
		}
	}
	_, err := parseThreadFrame("delta", "99-1", true, []byte(`{"run_id":"run","attempt":1,"sequence":1,"event":`+event+`}`))
	if !errors.Is(err, ErrProtocol) {
		t.Fatal("missing required nullable item accepted", err)
	}
}

func TestNormalSendAfterFailedAndCancelledLastRun(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/api/v1/threads/thr":
					_, _ = fmt.Fprintf(w, `{"id":"thr","current_run_id":null,"last_run_id":%q}`, status)
				case "/api/v1/runs/" + status:
					_, _ = fmt.Fprintf(w, `{"id":%q,"thread_id":"thr","status":%q,"failure":{"code":%q,"message":"ended"}}`, status, status, status)
				case "/api/v1/threads/thr/inbox":
					posts++
					if req.Method != "POST" || req.Header.Get("Idempotency-Key") != "explicit-send" {
						t.Error(req)
					}
					var body generated.Message
					if json.NewDecoder(req.Body).Decode(&body) != nil || body.AgentId != "agent" {
						t.Error(body)
					}
					w.WriteHeader(201)
					_, _ = io.WriteString(w, `{"thread":{"id":"thr"},"entry":{"id":"next","thread_id":"thr"},"run":null}`)
				case "/api/v1/threads/thr/inbox/next":
					_, _ = io.WriteString(w, `{"id":"next","thread_id":"thr","status":"consumed","assigned_run_id":"successor"}`)
				case "/api/v1/runs/successor":
					_, _ = fmt.Fprintf(w, `{"id":"successor","thread_id":"thr","status":"completed","parent_run_id":%q}`, status)
				default:
					t.Errorf("unexpected resume/retry/history request: %s", req.URL)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := NewClient(server.URL, NewSecret("test"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx := context.Background()
			thread, err := client.Thread("thr").Get(ctx)
			if err != nil || thread.Value.LastRunId.GetOrEmpty() != status {
				t.Fatal(thread, err)
			}
			predecessor, err := client.Run(status).Wait(ctx)
			if err != nil || string(predecessor.Status()) != status {
				t.Fatal(predecessor, err)
			}
			interaction, err := client.Agent("agent").Send(ctx, "thr", "Continue the sealed history.", SendOptions{RequestKey: "explicit-send"})
			if err != nil {
				t.Fatal(err)
			}
			defer interaction.Close()
			outcome, err := interaction.Result(ctx)
			if err != nil || outcome.Snapshot.Value.ParentRunId.GetOrEmpty() != status || outcome.Run.ID != "successor" || posts != 1 {
				t.Fatal(outcome, err, posts)
			}
			_, err = interaction.Next()
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(thread.Value)
			if strings.Contains(string(encoded), "head_run_id") {
				t.Fatal("obsolete head serialized")
			}
		})
	}
}

func TestHistoricalReadCannotHealOrSealScopedStream(t *testing.T) {
	requests := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if strings.HasSuffix(req.URL.Path, "/items") {
			if req.URL.Query().Get("after") != "0" {
				t.Fatal(req.URL)
			}
			return coverageResponse(200, `{"run":{"id":"run","thread_id":"thr","status":"completed"},"items":[],"baseline":false,"complete":true,"continuation":null,"position":null,"resume_after":null}`)
		}
		requests++
		response := coverageResponse(200, `event: gap
data: {"run_id":"run","position":"1-5"}

`+deltaAt("run", 1, 6, "100-3")+deltaAt("run", 1, 7, "100-4"))
		response.Header.Set("Content-Type", "text/event-stream")
		return response
	})
	stream, err := client.Thread("thr").events(context.Background(), StreamOptions{RunID: "run", Position: "1-2", After: "99-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if frame, err := stream.Next(); err != nil || frame.EventType() != "gap" {
		t.Fatal(frame, err)
	}
	zero := 0
	history, err := client.Run("run").Items(context.Background(), RunItemsOptions{After: &zero})
	if err != nil || history.Value.Baseline || !history.Value.Complete {
		t.Fatal(history, err)
	}
	// A historical response saying sealed describes its Run; it is not a
	// normalized live baseline installation or an Interaction completion signal.
	for range 2 {
		if frame, err := stream.Next(); err != nil || frame.EventType() != "delta" {
			t.Fatal(frame, err)
		}
	}
	if stream.AppliedPosition() != "1-2" || stream.AppliedCursor() != "99-1" || requests != 1 {
		t.Fatal("historical read healed, advanced or reopened frozen live state", stream.AppliedPosition(), stream.AppliedCursor(), requests)
	}
}
