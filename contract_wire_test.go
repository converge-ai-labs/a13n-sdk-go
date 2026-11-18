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

func TestCurrentSubagentsOverrideWire(t *testing.T) {
	mode := generated.SubagentsInputMode("async")
	if !mode.Valid() || generated.SubagentsInputMode("invalid").Valid() {
		t.Fatal("subagent mode enum validation")
	}
	named := map[string]any{"worker": SubagentSelection{AgentId: "agt_01234567890123456789", RevisionId: Null[string]()}}
	input := Overrides{Subagents: Value(Subagents{Mode: &mode, Named: &named, Dynamic: &DynamicSubagents{Enabled: Ptr(true), DefaultModel: Null[string]()}})}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected any
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"subagents":{"mode":"async","named":{"worker":{"agent_id":"agt_01234567890123456789","revision_id":null}},"dynamic":{"enabled":true,"default_model":null}}}`), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unexpected subagents: %s", data)
	}
	var decoded Overrides
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	roundtrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(roundtrip, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("lost subagents on readback: %s", roundtrip)
	}
	omitted, _ := json.Marshal(Overrides{})
	explicit, _ := json.Marshal(Overrides{Subagents: Null[Subagents]()})
	if string(omitted) != `{}` || string(explicit) != `{"subagents":null}` {
		t.Fatalf("omitted %s; explicit %s", omitted, explicit)
	}
}

func TestRunAgentThreadSnapshotHasNullRevision(t *testing.T) {
	var agent generated.RunAgent
	if err := json.Unmarshal([]byte(`{"definition_id":"agt_01234567890123456789","source":"thread_snapshot","revision":null,"config":{"model":"model"}}`), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Source != generated.ThreadSnapshot || !agent.Revision.IsNull() {
		t.Fatalf("snapshot: %#v", agent)
	}
	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["revision"]) != "null" {
		t.Fatalf("revision: %s", wire["revision"])
	}
}

func TestRunEnvironmentChoiceAndSessionBindingWire(t *testing.T) {
	var choice RunEnvironment
	existing := ExistingRunEnvironment{Mode: "existing", EnvironmentId: "env_01234567890123456789", SessionBinding: Ptr("default"), SessionConfiguration: Null[SessionConfiguration]()}
	if err := choice.FromExistingRunEnvironmentInput(existing); err != nil {
		t.Fatal(err)
	}
	options := RunOptions{Environment: Value(choice)}
	data, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected any
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"environment":{"mode":"existing","environment_id":"env_01234567890123456789","session_binding":"default","session_configuration":null}}`), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("environment wire: %s", data)
	}
	mount := MountCreate{Name: "work", EnvironmentId: "env_01234567890123456789", SessionBinding: Ptr("default"), SessionConfiguration: Null[SessionConfiguration]()}
	encoded, err := json.Marshal(mount)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["session_configuration"]) != "null" || string(got["session_binding"]) != `"default"` {
		t.Fatalf("mount: %s", encoded)
	}
}

func TestGeneratedRoutesCASQueryAndNoReplay(t *testing.T) {
	var requests []string
	client := coverageClient(t, func(req *http.Request) *http.Response {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		requests = append(requests, req.Method+" "+req.URL.RequestURI()+" "+req.Header.Get("If-Match")+" "+string(body))
		if strings.HasSuffix(req.URL.Path, "/value") {
			return coverageResponse(412, `{"error":{"code":"conflict","message":"stale","details":{}}}`)
		}
		return coverageResponse(200, `{}`)
	})
	ctx := context.Background()
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	match := `"v:1"`
	response, err := api.RotateEgressCredentialWithBodyWithResponse(ctx, "cred_one", &generated.RotateEgressCredentialParams{IfMatch: &match}, "application/json", strings.NewReader(`{"value":"fixture-only"}`))
	if err != nil || response.StatusCode() != 412 {
		t.Fatalf("rotate response %v %v", response, err)
	}
	_, err = api.ApproveClientPairingWithBodyWithResponse(ctx, "pair_one", nil, "application/json", strings.NewReader(`{"verification_code":"ABC"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.GetKubernetesEnvironmentLogsWithResponse(ctx, "env_one", &generated.GetKubernetesEnvironmentLogsParams{Previous: pointer(true), TailLines: pointer(2)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.UpdateSessionBindingWithBodyWithResponse(ctx, "thr_one", "env_one", "work", &generated.UpdateSessionBindingParams{IfMatch: &match}, "application/json", strings.NewReader(`{"configuration":{"commands":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.SetEnvironmentDebugHoldWithBodyWithResponse(ctx, "env_one", &generated.SetEnvironmentDebugHoldParams{IfMatch: &match}, "application/json", strings.NewReader(`{"seconds":60}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.GetOrganizationCandidateFileWithResponse(ctx, "org_one", &generated.GetOrganizationCandidateFileParams{Path: "notes/a.md", CandidateSeq: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 6 || !strings.Contains(requests[4], `/environments/env_one/debug-hold "v:1" {"seconds":60}`) || !strings.Contains(requests[5], `candidate_seq=3`) || !strings.Contains(requests[5], `path=notes%2Fa.md`) || !strings.Contains(requests[0], `PUT /proxy/api/v1/egress-credentials/cred_one/value "v:1" {"value":"fixture-only"}`) || !strings.Contains(requests[1], `/client-environments/pairings/pair_one/approve`) || !strings.Contains(requests[2], `previous=true`) || !strings.Contains(requests[2], `tail_lines=2`) || !strings.Contains(requests[3], `/threads/thr_one/session-bindings/env_one/work "v:1" {"configuration":{"commands":false}}`) {
		t.Fatalf("wire requests: %#v", requests)
	}
}

func TestService105ObjectReferencesAndSessionToolset(t *testing.T) {
	if !generated.BuiltinToolsetKeyObjectStore.Valid() || !sessionstream.ObjectStore.Valid() || generated.BuiltinToolsetKey("unknown").Valid() || sessionstream.BuiltinToolsetKey("unknown").Valid() {
		t.Fatal("HTTP and Session enum validity")
	}
	rules := map[string]bool{"*": true, "private*": false, "private_read": true}
	cfg := generated.McpConfig{Url: "https://mcp.example.test", ObjectRefTools: &rules}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded generated.McpConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.ObjectRefTools == nil || !reflect.DeepEqual(*decoded.ObjectRefTools, rules) {
		t.Fatalf("MCP mapping roundtrip: %s %v", encoded, err)
	}
	omitted, err := json.Marshal(generated.McpConfig{Url: cfg.Url})
	if err != nil || strings.Contains(string(omitted), "object_ref_tools") {
		t.Fatalf("omission: %s %v", omitted, err)
	}
	empty := map[string]bool{}
	cfg.ObjectRefTools = &empty
	encoded, err = json.Marshal(cfg)
	if err != nil || !strings.Contains(string(encoded), `"object_ref_tools":{}`) {
		t.Fatalf("empty: %s %v", encoded, err)
	}
	options := `"options":{"overrides":{"tool_search":{"automatic":{"capabilities":["object_store"]}}}}`
	run := strings.Replace(sessionRunJSON, `"options":{}`, options, 1)
	thread := strings.Replace(sessionThreadJSON, `"current_run":null`, `"current_run":`+run, 1)
	if _, err := parseSessionFrame(sseEvent{event: "thread", data: []byte(thread)}); err != nil {
		t.Fatalf("Session object_store rejected: %v", err)
	}
	bad := strings.Replace(thread, `"object_store"`, `"not_a_capability"`, 1)
	if _, err := parseSessionFrame(sseEvent{event: "thread", data: []byte(bad)}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unknown Session enum: %v", err)
	}
}
