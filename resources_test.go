package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
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

var pathParameter = regexp.MustCompile(`\{([^}]+)\}`)

func checkCoverageRequest(t *testing.T, req *http.Request, verb, path string) {
	t.Helper()
	expected := pathParameter.ReplaceAllStringFunc(path, func(param string) string {
		if param == "{seq}" {
			return "7"
		}
		if param == "{kind}" {
			return "memory"
		}
		return url.PathEscape("part /雪%")
	})
	if req.Method != verb || req.URL.EscapedPath() != "/proxy"+expected {
		t.Errorf("route: %s %s, expected %s %s", req.Method, req.URL.EscapedPath(), verb, expected)
	}
	if req.Header.Get("Authorization") != "Bearer test" {
		t.Error("missing shared auth")
	}
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
}
func TestResourceCoverageMatchesContract(t *testing.T) {
	data, err := os.ReadFile("contract/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	count := 0
	for path, operations := range doc.Paths {
		for verb := range operations {
			if verb != "get" && verb != "post" && verb != "put" && verb != "patch" && verb != "delete" {
				continue
			}
			count++
			if resourceOperations[strings.ToUpper(verb)+" "+path] == "" {
				t.Errorf("missing %s %s", verb, path)
			}
		}
	}
	if count != 230 || len(resourceOperations) != count {
		t.Fatalf("coverage %d/%d", len(resourceOperations), count)
	}
}
func TestBoundSubmissionUsesCanonicalWorkspace(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		if calls == 1 {
			if req.Header.Get("Idempotency-Key") != "key" {
				t.Error("missing request key")
			}
			var input generated.NewThread
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input.AgentId != "agent" {
				t.Errorf("body: %#v %v", input, err)
			}
			return coverageResponse(201, `{"thread":{"workspace_id":"ws_canonical","id":"thr"},"entry":{"id":"ent"},"run":null}`)
		}
		if req.URL.Path != "/proxy/api/v1/workspaces/ws_canonical/threads/thr/inbox/ent" {
			t.Error(req.URL.Path)
		}
		return coverageResponse(200, `{"id":"ent","status":"pending"}`)
	})
	collection := client.Resources().Workspaces().Ref("friendly-key").Threads()
	if calls != 0 {
		t.Fatal("binding performed IO")
	}
	submitted, err := collection.Create(context.Background(), generated.NewThread{AgentId: "agent", Payload: TextPayload("hello")}, ThreadsCreateOptions{IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Run != nil || submitted.Receipt.StatusCode != 201 {
		t.Fatal("queued receipt lost")
	}
	if _, err := submitted.Entry.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestWaitDeadlineCoversStalledResponse(t *testing.T) {
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
	_, err = client.Resources().Workspaces().Ref("ws").Runs().Ref("run").Wait(ctx, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	<-started
}
func TestClientCloseCancelsWaitSleep(t *testing.T) {
	called := make(chan struct{})
	client := coverageClient(t, func(req *http.Request) *http.Response {
		close(called)
		return coverageResponse(200, `{"status":"running"}`)
	})
	done := make(chan error, 1)
	go func() {
		_, err := client.Resources().Workspaces().Ref("ws").Runs().Ref("run").Wait(context.Background(), time.Hour)
		done <- err
	}()
	<-called
	_ = client.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait leaked")
	}
}
func TestResponseErrorsAndConditionalWrites(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		if req.Header.Get("If-Match") != `"v1"` {
			t.Error("CAS absent")
		}
		return coverageResponse(412, `{"error":{"code":"precondition_failed","message":"changed","details":{"current_etag":"v2"}}}`)
	})
	agent := client.Resources().Workspaces().Ref("ws").Agents().Ref("agent")
	_, err := agent.Update(context.Background(), generated.AgentUpdate{}, AgentUpdateOptions{})
	if err == nil || calls != 0 {
		t.Fatal("missing CAS dispatched")
	}
	_, err = agent.Update(context.Background(), generated.AgentUpdate{}, AgentUpdateOptions{IfMatch: `"v1"`})
	var api *ApiError
	if !errors.As(err, &api) || api.Status != 412 || api.RequestID != "req-test" || len(api.Details) != 1 {
		t.Fatalf("error evidence: %v", err)
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
	_, err = client.Resources().Workspaces().Ref("ws").Threads().Create(context.Background(), generated.NewThread{}, ThreadsCreateOptions{IdempotencyKey: "same-key"})
	if !errors.Is(err, ErrTransport) || calls != 1 || strings.Contains(err.Error(), "sensitive") {
		t.Fatal(err, calls)
	}
}
func TestSessionUsesSharedJarAndCSRF(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://service.test")
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "cookie"}})
	client, err := NewClient(origin.String(), Secret{}, roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" || req.Header.Get("X-CSRF-Token") != "csrf" {
			t.Error("wrong authentication")
		}
		if cookie, err := req.Cookie("session"); err != nil || cookie.Value != "cookie" {
			t.Error("missing session cookie")
		}
		return coverageResponse(204, ""), nil
	}), WithSession(jar, func() string { return "csrf" }))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Resources().Auth().Logout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}
func TestPaginationRetainsQueryAndStopsEarly(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		if req.URL.Query().Get("label") != "team=blue" {
			t.Error(req.URL.RawQuery)
		}
		if calls == 2 && req.URL.Query().Get("cursor") != "next" {
			t.Error("cursor absent")
		}
		return coverageResponse(200, `{"items":[],"next_cursor":"next"}`)
	})
	labels := []string{"team=blue"}
	pages := client.Resources().Workspaces().Ref("ws").Threads().Pages(context.Background(), ThreadsListOptions{Label: &labels})
	labels[0] = "changed-after-binding"
	for result, err := range pages {
		if err != nil || result.ETag() != `"v1"` {
			t.Fatal(err)
		}
		break
	}
	if calls != 1 {
		t.Fatal("pagination prefetched")
	}
	calls = 0
	var final error
	for _, err := range pages {
		final = err
	}
	if !errors.Is(final, ErrProtocol) || calls != 2 {
		t.Fatalf("loop guard: %v %d", final, calls)
	}
}
