package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/converge-ai-labs/a13n-sdk-go/generated/sessionstream"
)

func TestToolSearchProxyConfiguration(t *testing.T) {
	valid := []string{
		`{}`, `{"groups":{}}`,
		`{"config":{"mode":"proxy","max_results":0,"max_search_bytes":0,"search_name":"","call_name":""}}`,
		`{"config":{}}`,
		`{"config":{"mode":"proxy"},"groups":{"files":{"description":"Files","platform":false,"connections":[],"plugins":["selected"]}}}`,
	}
	for _, raw := range valid {
		var value ToolSearch
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(raw, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal([]byte(raw), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wire changed: %s -> %s", raw, encoded)
		}
		var native sessionstream.ToolSearch
		if err := json.Unmarshal([]byte(raw), &native); err != nil {
			t.Fatal(err)
		}
		nativeJSON, err := json.Marshal(native)
		if err != nil || string(nativeJSON) != string(encoded) {
			t.Fatal(string(nativeJSON), err)
		}
	}
	for _, raw := range []string{
		`null`, `{"config":null}`, `{"groups":null}`, `{"groups":{"x":{}}}`, `{"unexpected":true}`,
		`{"config":{"mode":"native"}}`, `{"config":{"mode":null}}`, `{"config":{"mode":"unknown"}}`,
		`{"config":{"mode":"native","max_results":10}}`, `{"config":{"mode":"proxy","strategy":"bm25"}}`,
		`{"config":{"mode":"native","strategy":"unknown"}}`, `{"config":{"mode":"native","strategy":false}}`,
		`{"config":{"mode":"proxy","max_results":null}}`, `{"config":{"mode":"proxy","max_results":1.5}}`,
	} {
		var value ToolSearch
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatal("invalid HTTP ToolSearch accepted", raw)
		}
		var native sessionstream.ToolSearch
		if err := json.Unmarshal([]byte(raw), &native); err == nil {
			t.Fatal("invalid Session ToolSearch accepted", raw)
		}
	}
	for _, raw := range []string{`{"mode":"native"}`, `null`, `{"mode":"native","search_name":"proxy"}`, `{"mode":"native","strategy":"bad"}`} {
		var value ToolSearchConfig
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatal("invalid union accepted", raw)
		}
	}
	invalidMode := generated.ToolSearchConfigMode("native")
	if _, err := json.Marshal(ToolSearchConfig{Mode: &invalidMode}); err == nil {
		t.Fatal("invalid discovery mode serialized")
	}
	for _, raw := range []string{`{}`, `{"tool_search":null}`, `{"tool_search":{}}`, `{"tool_search":{"groups":{}}}`} {
		var value Overrides
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != raw {
			t.Fatal(raw, string(encoded), err)
		}
	}
}

func TestDesktopWaitingIsFiniteAndResumeStaysExplicit(t *testing.T) {
	gets, resumes := 0, 0
	waiting := `{"id":"run","thread_id":"thr","status":"waiting","wait_reason":"desktop_control","pending":{"approvals":[],"calls":[],"desktop_control_id":"control"},"output":null}`
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/api/v1/runs/run") {
			gets++
			return coverageResponse(200, waiting)
		}
		if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/api/v1/runs/run/resume") {
			resumes++
			data, _ := io.ReadAll(req.Body)
			if string(data) != `{"approvals":{},"calls":{}}` {
				t.Errorf("desktop-only resume changed: %s", data)
			}
			return coverageResponse(201, runView("successor", "completed", "thr"))
		}
		t.Errorf("unrequested desktop mutation or successor follow: %s %s", req.Method, req.URL)
		return coverageResponse(500, `{}`)
	})
	interaction, err := client.Run("run").Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := interaction.Result(context.Background())
	if err != nil || result.Status != RunWaiting || result.Pending == nil || len(result.Pending.Calls) != 0 || len(result.Pending.Approvals) != 0 {
		t.Fatal(result, err)
	}
	id, err := result.Pending.DesktopControlId.Get()
	if err != nil || id != "control" || !strings.Contains(string(result.Evidence().RawJSON()), `"wait_reason":"desktop_control"`) {
		t.Fatal(id, err)
	}
	if _, err := interaction.Next(); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	_ = interaction.Close()
	if gets != 1 || resumes != 0 {
		t.Fatal(gets, resumes)
	}
	// The application completes the separate control record first. The SDK
	// sends an empty full batch explicitly, leaving readiness checks to Service.
	next, err := client.Run("run").Resume(context.Background(), Answers{}, ResumeOptions{IdempotencyKey: "explicit-after-handback"})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	final, err := next.Result(context.Background())
	if err != nil || final.RunID != "successor" || gets != 1 || resumes != 1 {
		t.Fatal(final, err, gets, resumes)
	}
}

func TestSessionDesktopWaitAndToolSearchNativeProjection(t *testing.T) {
	run := strings.Replace(sessionRunJSON, `"status":"accepted"`, `"status":"waiting"`, 1)
	run = strings.Replace(run, `"wait_reason":null`, `"wait_reason":"desktop_control"`, 1)
	run = strings.Replace(run, `"pending":null`, `"pending":{"approvals":[],"calls":[],"desktop_control_id":"control"}`, 1)
	run = strings.Replace(run, `"options":{}`, `"options":{"overrides":{"tool_search":{"config":{"mode":"proxy","max_results":7},"groups":{}}}}`, 1)
	data := strings.Replace(sessionThreadJSON, `"last_run":null`, `"last_run":`+run, 1)
	frame, err := parseSessionFrame(sseEvent{event: "thread", data: []byte(data)})
	if err != nil {
		t.Fatal(err)
	}
	last, err := frame.(*SessionThreadFrame).LastRun.Get()
	if err != nil {
		t.Fatal(err)
	}
	reason, _ := last.WaitReason.Get()
	pending, _ := last.Pending.Get()
	id, _ := pending.DesktopControlId.Get()
	if reason != sessionstream.DesktopControl || id != "control" {
		t.Fatal(last)
	}
	overrides, _ := last.Options.Overrides.Get()
	search, _ := overrides.ToolSearch.Get()
	if search.Config == nil || search.Config.MaxResults == nil || *search.Config.MaxResults != 7 {
		t.Fatal(search.Config)
	}
	for _, invalid := range []string{
		strings.Replace(data, `"desktop_control_id":"control"`, `"desktop_control_id":7`, 1),
		strings.Replace(data, `"max_results":7`, `"strategy":"regex"`, 1),
	} {
		if _, err := parseSessionFrame(sseEvent{event: "thread", data: []byte(invalid)}); !errors.Is(err, ErrProtocol) {
			t.Fatal("invalid native frame accepted", err)
		}
	}
}

func TestParseJSONNativeNullableSuccessIsExplicit(t *testing.T) {
	client := coverageClient(t, func(req *http.Request) *http.Response { return coverageResponse(200, `null`) })
	for _, body := range []string{`null`, ` null `, `{"run_id":"run","record_id":"record","input_tokens":null,"output_tokens":0,"context_window_tokens":null}`} {
		response := coverageResponse(200, body)
		response.Header.Set("X-Request-ID", "nullable")
		result, err := ParseJSON[*generated.ContextUsage](client, response, nil, 200)
		if err != nil || result.StatusCode != 200 || result.RequestID() != "nullable" || string(result.RawJSON()) != body {
			t.Fatal(result, err)
		}
		if strings.TrimSpace(body) == "null" {
			if result.Value != nil {
				t.Fatal("successful null became an object")
			}
		} else if result.Value == nil || !result.Value.InputTokens.IsNull() || !result.Value.ContextWindowTokens.IsNull() || result.Value.OutputTokens.GetOrEmpty() != 0 {
			t.Fatal(result.Value)
		}
	}
	if _, err := ParseJSON[generated.ContextUsage](client, coverageResponse(200, `null`), nil, 200); !errors.Is(err, ErrProtocol) {
		t.Fatal("non-nullable resource accepted null", err)
	}
	for _, body := range []string{"", `{`, `null {}`} {
		if _, err := ParseJSON[*generated.ContextUsage](client, coverageResponse(200, body), nil, 200); !errors.Is(err, ErrProtocol) {
			t.Fatal("nullable body bypassed JSON validation", err)
		}
	}
}
