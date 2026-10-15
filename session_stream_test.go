package a13n

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

const sessionThreadJSON = `{"thread":{"id":"thr","workspace_id":"value","session_id":"ses","origin":"new","origin_thread_id":null,"origin_run_id":null,"origin_tool_call_id":null,"subagent":null,"current_run_id":null,"last_run_id":null,"mcp_headers":{},"labels":{},"archived_at":null,"version":0,"created_at":"2026-10-15T00:00:00Z","updated_at":"2026-10-15T00:00:00Z"},"current_run":null,"last_run":null,"entries":[]}`
const sessionRunJSON = `{"id":"run","workspace_id":"value","session_id":"ses","thread_id":"thr","agent_id":"value","agent_revision_id":"value","revision_selection":"pinned","principal_id":"value","status":"accepted","trigger":"input","lineage":"root","parent_run_id":null,"source_entry_id":null,"resume":null,"resumed_by_id":null,"wait_reason":null,"pending":null,"environment_mounts":[],"memory_mounts":[],"options":{},"current_attempt_id":null,"attempts":0,"max_attempts":0,"output":null,"failure":null,"usage_at_seal":null,"labels":{},"cancel_requested_at":null,"version":0,"created_at":"2026-10-15T00:00:00Z","started_at":null,"sealed_at":null,"updated_at":"2026-10-15T00:00:00Z"}`
const sessionItemsJSON = `{"thread_id":"thr","run_id":"run","first":1,"items":[],"continuation":null,"complete":false}`
const sessionDeltaJSON = `{"thread_id":"thr","run_id":"run","attempt":1,"sequence":9,"event":{"type":"CUSTOM","name":"native","value":{"z":9007199254740993,"a":null}},"item":null}`
const sessionGapJSON = `{"thread_id":"thr","run_id":"run"}`

func sessionEvent(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

func TestSessionFramesAndNativeContinuationAreSeparateFromThread(t *testing.T) {
	for name, data := range map[string]string{"thread": sessionThreadJSON, "items": sessionItemsJSON, "delta": sessionDeltaJSON, "gap": sessionGapJSON} {
		frame, err := parseSessionFrame(sseEvent{event: name, data: []byte(data)})
		if err != nil || frame.EventType() != name || string(frame.RawJSON()) != data {
			t.Fatal(frame, err)
		}
		copy := frame.RawJSON()
		copy[0] = '!'
		if string(frame.RawJSON()) != data {
			t.Fatal("RawJSON aliases frame data")
		}
		if _, ok := any(frame).(ThreadFrame); ok {
			t.Fatal("Session frame acquired Thread cursor semantics")
		}
		if delta, ok := frame.(*SessionDeltaFrame); ok {
			if string(delta.Event["value"]) != `{"z":9007199254740993,"a":null}` || !delta.Item.IsNull() {
				t.Fatal(delta)
			}
		}
	}
	data := strings.Replace(sessionItemsJSON, `"continuation":null`, `"continuation":`+displayContinuationJSON, 1)
	frame, err := parseSessionFrame(sseEvent{event: "items", data: []byte(data)})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := frame.(*SessionItemsFrame).Continuation.Get()
	if err != nil || continuation.Position.Sequence != 8 || continuation.NextOrdinal != 11 {
		t.Fatal(continuation, err)
	}
	encoded, err := json.Marshal(continuation)
	var got, want any
	_ = json.Unmarshal(encoded, &got)
	_ = json.Unmarshal([]byte(displayContinuationJSON), &want)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("native recursive continuation changed: %s %v", encoded, err)
	}
	for _, pair := range []struct {
		value any
		field string
	}{{generated.ThreadView{}, "MessageHistory"}, {generated.RunView{}, "DisplayPosition"}} {
		if _, exists := reflect.TypeOf(pair.value).FieldByName(pair.field); exists {
			t.Fatal("retired readback field", pair.field)
		}
	}
}

func TestSessionParserUnknownAndMalformedFrames(t *testing.T) {
	unknown, err := parseSessionFrame(sseEvent{event: "future", hasID: true, data: []byte("not JSON")})
	if err != nil || unknown != nil {
		t.Fatal(unknown, err)
	}
	for _, event := range []sseEvent{
		{event: "gap", hasID: true, data: []byte(sessionGapJSON)},
		{event: "thread", data: []byte(`{"thread":{},"entries":[]}`)},
		{event: "thread", data: []byte(`{"thread":{},"entries":null,"current_run":null,"last_run":null}`)},
		{event: "items", data: []byte(strings.Replace(sessionItemsJSON, `"complete":false`, `"complete":null`, 1))},
		{event: "items", data: []byte(strings.Replace(sessionItemsJSON, `"continuation":null,`, "", 1))},
		{event: "delta", data: []byte(strings.Replace(sessionDeltaJSON, `"item":null`, `"item":false`, 1))},
		{event: "delta", data: []byte(strings.Replace(sessionDeltaJSON, `"sequence":9`, `"sequence":1.5`, 1))},
		{event: "delta", data: []byte(strings.Replace(sessionDeltaJSON, `"item":null`, `"item":{}`, 1))},
		{event: "gap", data: []byte(`[]`)},
		{event: "gap", data: []byte(`{"thread_id":false,"run_id":"run"}`)},
	} {
		if _, err := parseSessionFrame(event); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted invalid %s %s: %v", event.event, event.data, err)
		}
	}
	for _, text := range []string{sessionEvent("gap", sessionGapJSON[:10]), "data: \xff\n\n", strings.TrimSuffix(sessionEvent("gap", sessionGapJSON), "\n"), sessionEvent("future", strings.Repeat("x", 300))} {
		p := sseParser{reader: bufio.NewReader(strings.NewReader(text)), limit: 256, first: true}
		event, err := p.nextEvent()
		if err == nil {
			_, err = parseSessionFrame(event)
		}
		if !errors.Is(err, ErrProtocol) {
			t.Fatal("malformed framing accepted", err)
		}
	}
}

func TestSessionEventsSingleConnectionFreshReopenAndMetadata(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		if req.URL.Path != "/proxy/api/v1/sessions/ses/stream" || req.URL.RawQuery != "" || req.Header.Get("Last-Event-ID") != "" || req.Header.Get("X-Workspace-ID") != "" || req.Header.Get("Authorization") != "Bearer test" {
			t.Fatal(req.URL, req.Header)
		}
		response := coverageResponse(200, "\ufeff: keepalive\r\n\r\n"+sessionEvent("future", "not JSON")+sessionEvent("thread", sessionThreadJSON)+sessionEvent("items", sessionItemsJSON)+sessionEvent("delta", sessionDeltaJSON)+sessionEvent("gap", sessionGapJSON))
		response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
		response.Header.Set("X-Request-ID", fmt.Sprint(calls))
		return response
	})
	for connection := 1; connection <= 2; connection++ {
		stream, err := client.SessionEvents(context.Background(), "ses", SessionStreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		status, headers := stream.Response()
		if status != 200 || headers.Get("X-Request-ID") != fmt.Sprint(connection) {
			t.Fatal(status, headers)
		}
		headers.Set("X-Request-ID", "changed")
		for _, want := range []string{"thread", "items", "delta", "gap"} {
			frame, err := stream.Next()
			if err != nil || frame.EventType() != want {
				t.Fatal(frame, err)
			}
		}
		if _, err := stream.Next(); err != io.EOF || calls != connection {
			t.Fatal("EOF reconnected", err, calls)
		}
		_, headers = stream.Response()
		if headers.Get("X-Request-ID") != fmt.Sprint(connection) {
			t.Fatal(headers)
		}
	}
}

func TestSessionEventsCloseCancellationAndClientLifetime(t *testing.T) {
	for _, stop := range []string{"close", "context", "client"} {
		t.Run(stop, func(t *testing.T) {
			var requests atomic.Int32
			attached, released := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(attached)
				<-r.Context().Done()
				close(released)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, NewSecret("test"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := client.SessionEvents(ctx, "ses", SessionStreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			<-attached
			done := make(chan error, 1)
			go func() { _, err := stream.Next(); done <- err }()
			switch stop {
			case "close":
				_ = stream.Close()
			case "context":
				cancel()
			case "client":
				_ = client.Close()
			}
			select {
			case err := <-done:
				want := error(io.EOF)
				if stop == "context" {
					want = context.Canceled
				}
				if stop == "client" {
					want = ErrClosed
				}
				if !errors.Is(err, want) {
					t.Fatal(err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked Next not released")
			}
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("body still owned")
			}
			if requests.Load() != 1 {
				t.Fatal("cancellation retried")
			}
		})
	}
}

func TestSessionEventsErrorsAndLocalValidation(t *testing.T) {
	for _, status := range []int{403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client := coverageClient(t, func(req *http.Request) *http.Response {
				calls++
				response := coverageResponse(status, `{"error":{"code":"native","message":"stopped","details":{},"request_id":"session-error"}}`)
				response.Header.Set("Retry-After", "3")
				return response
			})
			_, err := client.SessionEvents(context.Background(), "ses", SessionStreamOptions{})
			var api *ApiError
			if !errors.As(err, &api) || api.Status != status || api.RetryAfter != "3" || api.RequestID != "session-error" || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
	client := coverageClient(t, func(req *http.Request) *http.Response { return coverageResponse(200, "not SSE") })
	if _, err := client.SessionEvents(context.Background(), "ses", SessionStreamOptions{}); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	if _, err := client.SessionEvents(context.Background(), "ses", SessionStreamOptions{MaxFrameBytes: -1}); err == nil {
		t.Fatal("negative bound accepted")
	}
	if _, err := client.SessionEvents(context.Background(), "", SessionStreamOptions{}); err == nil {
		t.Fatal("empty identity accepted")
	}
}

func TestSessionStreamScopeAndProtocolFailureOwnership(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://service.test")
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "cookie"}})
	calls := 0
	client, err := NewClient(origin.String(), Secret{}, roundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		wantScope := "semantic"
		if calls == 2 {
			wantScope = "explicit"
		}
		cookie, cookieErr := req.Cookie("session")
		if req.Header.Get("X-Workspace-ID") != wantScope || req.Header.Get("Authorization") != "" || cookieErr != nil || cookie.Value != "cookie" || req.Header.Get("X-CSRF-Token") != "" {
			t.Fatal(req.Header, cookieErr)
		}
		response := coverageResponse(200, sessionEvent("items", strings.Replace(sessionItemsJSON, `"complete":false`, `"complete":true`, 1))+sessionEvent("gap", sessionGapJSON)+sessionEvent("delta", `{}`))
		response.Header.Set("Content-Type", "text/event-stream")
		return response, nil
	}), WithSession(jar, func() string { return "csrf" }), WithSessionWorkspace("semantic"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream, err := client.SessionEvents(context.Background(), "ses", SessionStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if frame, err := stream.Next(); err != nil || !frame.(*SessionItemsFrame).Complete {
		t.Fatal(frame, err)
	}
	if frame, err := stream.Next(); err != nil || frame.EventType() != "gap" {
		t.Fatal("complete ended Session observation", frame, err)
	}
	_, err = stream.Next()
	var protocol *ProtocolError
	if !errors.As(err, &protocol) || protocol.Kind != "stream_frame" || protocol.StatusCode != 200 || protocol.RequestID != "req-test" {
		t.Fatal(err)
	}
	if _, err = stream.Next(); err != io.EOF || calls != 1 {
		t.Fatal("invalid known frame retried", err, calls)
	}
	api, _ := client.API()
	response, err := api.SessionStreamApiV1SessionsSessionIdStreamGet(context.Background(), "ses", &generated.SessionStreamApiV1SessionsSessionIdStreamGetParams{XWorkspaceID: Ptr("explicit")})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestSessionSchemaIsBundledAndCompiles(t *testing.T) {
	if _, err := sessionSchemas(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionNativeSchemaRejectsNestedInvalidValues(t *testing.T) {
	for _, data := range []string{
		strings.Replace(sessionItemsJSON, `"items":[]`, `"items":[{}]`, 1),
		strings.Replace(sessionItemsJSON, `"continuation":null`, `"continuation":{"run_id":"run","next_ordinal":1,"position":{}}`, 1),
		strings.Replace(sessionThreadJSON, `"current_run":null`, `"current_run":{}`, 1),
		strings.Replace(sessionThreadJSON, `"entries":[]`, `"entries":[{}]`, 1),
		strings.Replace(sessionThreadJSON, `"origin":"new"`, `"origin":"future"`, 1),
	} {
		name := "items"
		if strings.Contains(data, `"thread":`) {
			name = "thread"
		}
		if _, err := parseSessionFrame(sseEvent{event: name, data: []byte(data)}); !errors.Is(err, ErrProtocol) {
			t.Fatal("nested invalid schema accepted", err)
		}
	}
	// The native outer integer fields deliberately have no schema minimum.
	for name, data := range map[string]string{"items": strings.Replace(sessionItemsJSON, `"first":1`, `"first":-1`, 1), "delta": strings.Replace(sessionDeltaJSON, `"attempt":1`, `"attempt":-1`, 1)} {
		if _, err := parseSessionFrame(sseEvent{event: name, data: []byte(data)}); err != nil {
			t.Fatal("invented outer bound", err)
		}
	}
}

func TestSessionSkillSyncAndExactNativeJSONRoundTrip(t *testing.T) {
	for _, policy := range []string{`{"mode":"isolated","cache_count":0}`, `{"mode":"shared","overwrite":false,"cleanup":false}`} {
		run := strings.Replace(sessionRunJSON, `"options":{}`, `"options":{"overrides":{"skill_sync":`+policy+`,"model_settings":{"cache":false,"big":9007199254740993}}}`, 1)
		data := strings.Replace(sessionThreadJSON, `"current_run":null`, `"current_run":`+run, 1)
		frame, err := parseSessionFrame(sseEvent{event: "thread", data: []byte(data)})
		if err != nil {
			t.Fatal(err)
		}
		current, err := frame.(*SessionThreadFrame).CurrentRun.Get()
		if err != nil {
			t.Fatal(err)
		}
		overrides, err := current.Options.Overrides.Get()
		if err != nil {
			t.Fatal(err)
		}
		value, err := overrides.SkillSync.Get()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal([]byte(policy), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("named native policy lost: %s", encoded)
		}
		settings, err := overrides.ModelSettings.Get()
		if err != nil || string(settings["big"]) != "9007199254740993" || string(settings["cache"]) != "false" {
			t.Fatal(settings, err)
		}
	}
}

func TestSessionSnapshotHasNoDefaultSizeLimitAndCallerBoundCloses(t *testing.T) {
	text := strings.Repeat("x", 32<<10)
	items := make([]string, 160)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":"item-%d","ordinal":%d,"kind":"text_message","state":"completed","first_stream_id":"1-0","last_stream_id":"1-1","started_at":"2026-10-15T00:00:00Z","content":{"text":"%s"}}`, i+1, i+1, text)
	}
	payload := strings.Replace(sessionItemsJSON, `"items":[]`, `"items":[`+strings.Join(items, ",")+`]`, 1)
	for _, limit := range []int{0, 1024} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			body := &sessionSizeBody{Reader: strings.NewReader(sessionEvent("items", payload))}
			calls := 0
			client := coverageClient(t, func(req *http.Request) *http.Response {
				calls++
				response := coverageResponse(200, "")
				response.Header.Set("Content-Type", "text/event-stream")
				response.Body = body
				return response
			})
			stream, err := client.SessionEvents(context.Background(), "ses", SessionStreamOptions{MaxFrameBytes: limit})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			frame, err := stream.Next()
			if limit == 0 {
				if err != nil {
					t.Fatal("valid >4 MiB snapshot rejected", err)
				}
				value := frame.(*SessionItemsFrame)
				if len(value.Items) != len(items) || len(value.RawJSON()) <= 4<<20 || string(value.Items[0].Content["text"]) != `"`+text+`"` || value.Items[len(items)-1].Ordinal != len(items) {
					t.Fatal("large native snapshot changed")
				}
			} else if !errors.Is(err, ErrProtocol) || !body.closed {
				t.Fatal("caller bound did not fail and close", err, body.closed)
			}
			if _, err := stream.Next(); err != io.EOF || !body.closed || calls != 1 {
				t.Fatal("terminal read retained body or reconnected", err, body.closed, calls)
			}
		})
	}
}

type sessionSizeBody struct {
	*strings.Reader
	closed bool
}

func (b *sessionSizeBody) Close() error { b.closed = true; return nil }
