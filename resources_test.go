package a13n

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/oapi-codegen/nullable"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func pointer[T any](value T) *T                                         { return &value }
func coverageResponse(status int, body string) *http.Response {
	if status == 204 {
		body = ""
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "Etag": {`"v1"`}, "X-Request-Id": {"req-test"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func coverageClient(t *testing.T, respond func(*http.Request) *http.Response) *Client {
	t.Helper()
	client, err := NewClient("https://service.test/proxy", NewSecret("test"), roundTrip(func(req *http.Request) (*http.Response, error) { return respond(req), nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

const queuedReceipt = `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr"},"run":null}`

func entryView(status, run string) string {
	return `{"id":"ent","thread_id":"thr","status":"` + status + `","assigned_run_id":` + run + `}`
}
func runView(id, status, thread string) string {
	return `{"id":"` + id + `","status":"` + status + `","thread_id":"` + thread + `"}`
}

func TestStartAndSendOptionsCoverGeneratedFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    any
		options any
	}{{"start", generated.NewThread{}, StartOptions{}}, {"send", generated.Message{}, SendOptions{}}} {
		t.Run(tc.name, func(t *testing.T) {
			body := reflect.TypeOf(tc.body)
			options := reflect.TypeOf(tc.options)
			if options.NumField() != body.NumField()-1 { // body AgentId + Payload; options RequestKey
				t.Fatalf("option schema coverage: %d fields vs %d", options.NumField(), body.NumField())
			}
			for j := 0; j < body.NumField(); j++ {
				field := body.Field(j)
				if field.Name == "AgentId" || field.Name == "Payload" {
					continue
				}
				actual, ok := options.FieldByName(field.Name)
				if !ok || actual.Type != field.Type {
					t.Errorf("missing or mistyped %s: %s", field.Name, field.Type)
				}
			}
		})
	}
}

func TestAgentInvocationUsesFlatRoutesAndTypedOptions(t *testing.T) {
	var post atomic.Int32
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method == "POST" {
			post.Add(1)
			if req.Header.Get("Idempotency-Key") == "" || req.Header.Get("Authorization") != "Bearer test" || req.Header.Get("X-Workspace-ID") != "" {
				t.Error("wrong key or scope")
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if string(body["agent_id"]) != `"agent"` || string(body["agent_revision_id"]) != "null" || string(body["payload"]) == "" {
				t.Errorf("lost typed input: %v", body)
			}
			if req.URL.Path == "/proxy/api/v1/threads" {
				return coverageResponse(201, queuedReceipt)
			}
			if req.URL.Path == "/proxy/api/v1/threads/thr/inbox" {
				return coverageResponse(201, queuedReceipt)
			}
			t.Error("unexpected POST path", req.URL.Path)
		}
		if strings.Contains(req.URL.Path, "/inbox/ent") {
			return coverageResponse(200, entryView("consumed", `"run"`))
		}
		return coverageResponse(200, runView("run", "completed", "thr"))
	})
	for _, send := range []bool{false, true} {
		var interaction *Interaction
		var err error
		if send {
			interaction, err = client.Agent("agent").Send(context.Background(), "thr", "hello", SendOptions{RequestKey: "send", AgentRevisionId: nullable.NewNullNullable[string]()})
		} else {
			interaction, err = client.Agent("agent").Start(context.Background(), "hello", StartOptions{RequestKey: "start", AgentRevisionId: nullable.NewNullNullable[string]()})
		}
		if err != nil {
			t.Fatal(err)
		}
		if interaction.Run != nil || interaction.Receipt.StatusCode != 201 || interaction.Thread.ID != "thr" {
			t.Fatal("queued receipt lost")
		}
		outcome, err := interaction.Result(context.Background())
		if err != nil || outcome.Run.ID != "run" || outcome.Status() != generated.RunStatusCompleted || outcome.Snapshot.RequestID() != "req-test" {
			t.Fatalf("result: %#v %v", outcome, err)
		}
		_ = interaction.Close()
	}
	if post.Load() != 2 {
		t.Fatalf("unexpected submission replay: %d", post.Load())
	}
}

func TestConsumedOnlyAfterRollbackAndExactRun(t *testing.T) {
	var entries atomic.Int32
	var runs atomic.Int32
	client := coverageClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == "POST":
			return coverageResponse(201, queuedReceipt)
		case strings.Contains(req.URL.Path, "/inbox/ent"):
			switch entries.Add(1) {
			case 1:
				return coverageResponse(200, entryView("queued", "null"))
			case 2:
				return coverageResponse(200, entryView("pending", `"old-run"`))
			default:
				return coverageResponse(200, entryView("consumed", `"exact-run"`))
			}
		case strings.Contains(req.URL.Path, "/runs/exact-run"):
			runs.Add(1)
			return coverageResponse(200, runView("exact-run", "waiting", "thr"))
		default:
			t.Errorf("read unrelated Run: %s", req.URL.Path)
			return coverageResponse(500, `{"error":{"code":"unexpected","message":"wrong run"}}`)
		}
	})
	interaction, err := client.Agent("agent").Start(context.Background(), "wait", StartOptions{RequestKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer interaction.Close()
	outcome, err := interaction.Result(context.Background())
	if err != nil || outcome.Status() != generated.RunStatusWaiting || outcome.Run.ID != "exact-run" || entries.Load() != 3 || runs.Load() != 1 {
		t.Fatalf("incorporation: %v %v reads=%d/%d", outcome.Status(), err, entries.Load(), runs.Load())
	}
}

func TestFailedAndWithdrawnEntryReturnTypedError(t *testing.T) {
	for _, status := range []string{"failed", "withdrawn"} {
		t.Run(status, func(t *testing.T) {
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if req.Method == "POST" {
					return coverageResponse(201, queuedReceipt)
				}
				return coverageResponse(200, entryView(status, "null"))
			})
			i, err := client.Agent("agent").Start(context.Background(), "x", StartOptions{RequestKey: "k"})
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close()
			_, err = i.Result(context.Background())
			var disposition *EntryDispositionError
			if !errors.As(err, &disposition) || disposition.Entry.ID != "ent" || strings.Contains(err.Error(), "payload") {
				t.Fatal(err)
			}
			_, err = i.Next()
			if !errors.As(err, &disposition) {
				t.Fatal(err)
			}
		})
	}
}

func TestRunWaitDeadlineCoversStalledResponse(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = client.Run("run").Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	<-started
}
func TestClientCloseCancelsObservation(t *testing.T) {
	called := make(chan struct{})
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method == "POST" {
			return coverageResponse(201, queuedReceipt)
		}
		select {
		case <-called:
		default:
			close(called)
		}
		return coverageResponse(200, entryView("queued", "null"))
	})
	i, err := client.Agent("agent").Start(context.Background(), "x", StartOptions{RequestKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	<-called
	_ = client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = i.Result(ctx)
	if !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	_ = i.Close()
}
func TestGeneratedCASPreservesStructuredFailure(t *testing.T) {
	var calls atomic.Int32
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls.Add(1)
		if req.Header.Get("If-Match") != `"v1"` {
			t.Error("CAS absent")
		}
		return coverageResponse(412, `{"error":{"code":"precondition_failed","message":"changed","details":{"current_etag":"v2"}}}`)
	})
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.UpdateAgentApiV1AgentsAgentIdPatch(context.Background(), "agent", &generated.UpdateAgentApiV1AgentsAgentIdPatchParams{IfMatch: pointer(`"v1"`)}, generated.AgentUpdate{})
	_, err = ParseJSON[generated.Agent](client, response, err, 200)
	var failure *ApiError
	if !errors.As(err, &failure) || failure.Status != 412 || failure.RequestID != "req-test" || len(failure.Details) != 1 || calls.Load() != 1 {
		t.Fatalf("CAS: %v", err)
	}
}
func TestMutationsDoNotReplay(t *testing.T) {
	calls := 0
	client, err := NewClient("https://service.test", NewSecret("test"), roundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.GetBody != nil {
			t.Error("mutation remains replayable")
		}
		return nil, errors.New("sensitive failure")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Agent("agent").Start(context.Background(), "hello", StartOptions{RequestKey: "same-key"})
	if !errors.Is(err, ErrTransport) || calls != 1 || strings.Contains(err.Error(), "sensitive") {
		t.Fatal(err, calls)
	}
}
func TestSessionScopeOnlyExplicitOnGeneratedCalls(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://service.test")
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "cookie"}})
	client, err := NewClient(origin.String(), Secret{}, roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" || (req.Method == "POST" && req.Header.Get("X-CSRF-Token") != "csrf") {
			t.Error("wrong session authentication")
		}
		if cookie, err := req.Cookie("session"); err != nil || cookie.Value != "cookie" {
			t.Error("missing session cookie")
		}
		if req.URL.Path == "/api/v1/auth/logout" {
			if req.Header.Get("X-Workspace-ID") != "" {
				t.Error("workspace leaked to auth")
			}
			return coverageResponse(204, ""), nil
		}
		if req.Header.Get("X-Workspace-ID") != "ws" {
			t.Error("missing semantic session scope")
		}
		if req.Method == "POST" {
			return coverageResponse(201, queuedReceipt), nil
		}
		if strings.Contains(req.URL.Path, "/inbox/ent") {
			return coverageResponse(200, entryView("consumed", `"run"`)), nil
		}
		return coverageResponse(200, runView("run", "completed", "thr")), nil
	}), WithSession(jar, func() string { return "csrf" }), WithSessionWorkspace("ws"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.LogoutApiV1AuthLogoutPost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	i, err := client.Agent("agent").Start(context.Background(), "hi", StartOptions{RequestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if _, err := i.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestPagesLazyMetadataAndLoopGuard(t *testing.T) {
	calls := 0
	ctx := context.Background()
	fetch := func(_ context.Context, cursor *string) (Result[generated.ThreadPage], error) {
		calls++
		if calls == 1 && cursor != nil {
			t.Error("first cursor")
		}
		if calls > 2 && (cursor == nil || *cursor != "next") {
			t.Error("next cursor")
		}
		return Result[generated.ThreadPage]{Value: generated.ThreadPage{NextCursor: nullable.NewNullableWithValue("next")}, Header: http.Header{"Etag": {`"v1"`}}}, nil
	}
	pages := Pages(ctx, "", fetch, func(page generated.ThreadPage) string { return page.NextCursor.GetOrEmpty() })
	for page, err := range pages {
		if err != nil || page.ETag() != `"v1"` {
			t.Fatal(err)
		}
		break
	}
	if calls != 1 {
		t.Fatal("pagination prefetched")
	}
	var last error
	for _, err := range pages {
		last = err
	}
	if !errors.Is(last, ErrProtocol) || calls != 3 {
		t.Fatalf("loop guard: %v calls=%d", last, calls)
	}
}

func TestResponseIdentityAndRequestedThreadAreEnforced(t *testing.T) {
	client := coverageClient(t, func(req *http.Request) *http.Response {
		switch {
		case req.Method == "POST":
			return coverageResponse(201, `{"thread":{"id":"other"},"entry":{"id":"ent","thread_id":"other"},"run":null}`)
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			return coverageResponse(200, `{"id":"wrong","thread_id":"thr","status":"consumed"}`)
		case strings.HasSuffix(req.URL.Path, "/runs/run/items"):
			return coverageResponse(200, `{"run":{"id":"wrong"},"items":[],"complete":true}`)
		case strings.HasSuffix(req.URL.Path, "/runs/run"):
			return coverageResponse(200, runView("wrong", "completed", "thr"))
		default:
			return coverageResponse(200, `{"id":"wrong"}`)
		}
	})
	ctx := context.Background()
	if _, err := client.Agent("agent").Send(ctx, "thr", "x", SendOptions{RequestKey: "key"}); !errors.Is(err, ErrProtocol) {
		t.Fatal("cross-Thread receipt:", err)
	}
	if _, err := client.Entry("thr", "ent").Get(ctx); !errors.Is(err, ErrProtocol) {
		t.Fatal("Entry mismatch:", err)
	}
	if _, err := client.Thread("thr").Get(ctx); !errors.Is(err, ErrProtocol) {
		t.Fatal("Thread mismatch:", err)
	}
	if _, err := client.Run("run").Get(ctx); !errors.Is(err, ErrProtocol) {
		t.Fatal("Run mismatch:", err)
	}
	if _, err := client.Run("run").Items(ctx); !errors.Is(err, ErrProtocol) {
		t.Fatal("Items mismatch:", err)
	}
}
func TestInvocationDeadlineIncludesQueueAndInFlightRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "POST" {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(queuedReceipt))
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	i, err := client.Agent("agent").Start(ctx, "x", StartOptions{RequestKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if _, err := i.Result(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cumulative read deadline: %v", err)
	}
	if _, err := i.Next(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next lost timeout: %v", err)
	}
}
func TestSessionOptionsRequireCookieAndCSRF(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range [][]ClientOption{{WithSession(jar, nil)}, {WithSessionWorkspace("ws")}} {
		if _, err := NewClient("https://service.test", Secret{}, nil, options...); err == nil {
			t.Fatal("accepted session with missing authentication")
		}
	}
	if _, err := NewClient("https://service.test", NewSecret("key"), nil, WithSession(jar, func() string { return "csrf" })); err == nil {
		t.Fatal("combined session and API key")
	}
}
func TestRunResumeReturnsDistinctSuccessor(t *testing.T) {
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.URL.Path == "/proxy/api/v1/runs/run/resume" {
			if req.Header.Get("Idempotency-Key") != "answer" {
				t.Error("missing resume key")
			}
			return coverageResponse(201, runView("successor", "queued", "thr"))
		}
		return coverageResponse(200, runView("run", "cancelled", "thr"))
	})
	successor, receipt, err := client.Run("run").Resume(context.Background(), generated.Resume{Approvals: map[string]generated.ApprovalDecision{}, Calls: map[string]generated.CallResult{}}, "answer")
	if err != nil || successor.ID != "successor" || receipt.StatusCode != 201 {
		t.Fatal(successor, receipt, err)
	}
	if successor.ID == "run" {
		t.Fatal("resume followed predecessor")
	}
}
