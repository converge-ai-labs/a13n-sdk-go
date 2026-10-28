package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestMemorySpaceAndComputerNativeValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		value any
	}{
		{"space omitted", `{}`, &generated.MemorySpaceUpdate{}},
		{"space null", `{"always_load":null,"description":null,"enabled":null,"guide":null,"labels":null,"name":null,"organization":null}`, &generated.MemorySpaceUpdate{}},
		{"space empty", `{"always_load":[],"description":"","enabled":false,"guide":"","labels":{},"name":"space","organization":{}}`, &generated.MemorySpaceUpdate{}},
		{"space create", `{"name":"shared","enabled":false,"always_load":[],"labels":{},"guide":null}`, &generated.MemorySpaceCreate{}},
		{"any change", `{"check_interval_seconds":10,"cooldown_seconds":0,"enabled":false,"model":null,"trigger":{"type":"any_change"}}`, &generated.OrganizationPolicy{}},
		{"change count", `{"trigger":{"type":"change_count","count":1}}`, &generated.OrganizationPolicy{}},
		{"session omitted", `{}`, &generated.SessionUpdate{}},
		{"session null", `{"memory":null,"labels":null}`, &generated.SessionUpdate{}},
		{"session enabled only", `{"memory":{"enabled":false}}`, &generated.SessionUpdate{}},
		{"session spaces only", `{"memory":{"spaces":[]}}`, &generated.SessionUpdate{}},
		{"session empty", `{"memory":{"enabled":false,"spaces":[]},"labels":{}}`, &generated.SessionUpdate{}},
		{"empty trigger", `{"trigger":{}}`, &generated.OrganizationPolicy{}},
		{"session bindings", `{"memory":{"enabled":true,"spaces":[{"name":"team","space_id":"spc_0123456789abcdef0123","access":"read"}]}}`, &generated.SessionCreate{}},
		{"empty ceiling", `{"operations":[]}`, &generated.EnvironmentPermissionSetInput{}},
		{"computer ceiling", `{"operations":["environment.computer.observe","environment.computer.click"]}`, &generated.EnvironmentPermissionSetOutput{}},
		{"computer not ready", `{"status":"not_ready","operations":[],"observe_ready":false,"input_ready":false,"display_count":0}`, &generated.ComputerCheck{}},
		{"metrics null", `{"time_to_first_chunk_seconds":null,"request_duration_seconds":null,"output_tokens_per_second":null}`, &generated.ModelRequestMetrics{}},
		{"metrics zero", `{"time_to_first_chunk_seconds":0,"request_duration_seconds":0.25,"output_tokens_per_second":0}`, &generated.ModelRequestMetrics{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(test.input), test.value); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(test.input), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("native value changed: %s -> %s", test.input, encoded)
			}
		})
	}
}

func TestRunFrozenMemoryMountNativeReadback(t *testing.T) {
	const mounts = `[{"name":"legacy","memory_id":"mem_0123456789abcdef0123","access":"read","recall":false},{"name":"team","memory_id":"mem_0123456789abcdef0123","access":"write","recall":false,"space_id":"spc_0123456789abcdef0123","roots":["global/","sessions/ses_0123456789abcdef0123/"]},{"name":"null-space","memory_id":"mem_0123456789abcdef0123","access":"read","space_id":null,"roots":[]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "frozen-mounts")
		_, _ = io.WriteString(w, `{"id":"run","thread_id":"thread","status":"completed","memory_mounts":`+mounts+`}`)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, NewSecret("token"), nil)
	defer client.Close()
	result, err := client.Run("run").Get(context.Background())
	if err != nil || result.RequestID() != "frozen-mounts" || len(result.Value.MemoryMounts) != 3 {
		t.Fatalf("Run readback: %#v, %v", result, err)
	}
	legacy, err := result.Value.MemoryMounts[0].AsMemoryMount()
	if err != nil || legacy.Access != generated.MemoryAccessRead || legacy.Recall == nil || *legacy.Recall {
		t.Fatalf("legacy mount: %#v, %v", legacy, err)
	}
	frozen, err := result.Value.MemoryMounts[1].AsFrozenMemoryMount()
	if err != nil || frozen.SpaceId.GetOrEmpty() != "spc_0123456789abcdef0123" || frozen.Roots == nil || len(*frozen.Roots) != 2 || frozen.Recall == nil || *frozen.Recall {
		t.Fatalf("frozen mount: %#v, %v", frozen, err)
	}
	nullSpace, err := result.Value.MemoryMounts[2].AsFrozenMemoryMount()
	if err != nil || !nullSpace.SpaceId.IsNull() || nullSpace.Roots == nil || len(*nullSpace.Roots) != 0 {
		t.Fatalf("null/empty frozen mount: %#v, %v", nullSpace, err)
	}
	encoded, err := json.Marshal(result.Value.MemoryMounts)
	if err != nil || string(encoded) != mounts {
		t.Fatalf("native union lost host fields: %s, %v", encoded, err)
	}
	request, _ := json.Marshal(legacy)
	var fields map[string]any
	_ = json.Unmarshal(request, &fields)
	if _, ok := fields["roots"]; ok {
		t.Fatal("host-resolved roots entered request MemoryMount")
	}
	if _, ok := fields["space_id"]; ok {
		t.Fatal("host-resolved Space identity entered request MemoryMount")
	}
}

func TestPlatformAndComputerSelectionsReachAuthoredStart(t *testing.T) {
	var submitted map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"thread":{"id":"thread"},"entry":{"id":"entry","thread_id":"thread"},"run":null}`)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, NewSecret("token"), nil)
	defer client.Close()
	permissions := EnvironmentPermissionSet{Operations: Ptr([]EnvironmentAction{})}
	mounts := InitialMounts{{Name: "desktop", EnvironmentId: "environment", PermissionCeiling: &permissions}}
	options := RunOptions{Overrides: Value(Overrides{
		BuiltinSkills: Value([]BuiltinSkill{}),
	})}
	interaction, err := client.Agent("agent").Start(context.Background(), "Inspect", StartOptions{IdempotencyKey: "selection", Environments: &mounts, Options: &options})
	if err != nil {
		t.Fatal(err)
	}
	defer interaction.Close()
	if string(submitted["environments"]) != `[{"environment_id":"environment","name":"desktop","permission_ceiling":{"operations":[]}}]` {
		t.Fatalf("permission ceiling changed: %s", submitted["environments"])
	}
	var got, want any
	_ = json.Unmarshal(submitted["options"], &got)
	_ = json.Unmarshal([]byte(`{"overrides":{"builtin_skills":[]}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("platform selections changed: %s", submitted["options"])
	}
}

func TestMemoryPlatformAndUsageNativeReadback(t *testing.T) {
	config := `{"model":"test","builtin_skills":[],"memory":{"enabled":false,"spaces":[]}}`
	for _, model := range []any{&generated.AgentConfigInput{}, &generated.AgentConfigOutput{}} {
		if err := json.Unmarshal([]byte(config), model); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(model)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal([]byte(config), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("config changed: %s", encoded)
		}
	}
	for _, body := range []string{`{}`, `{"builtin_skills":null}`, `{"builtin_skills":[]}`} {
		for _, model := range []any{&Overrides{}, &generated.AgentOverrideOutput{}} {
			if err := json.Unmarshal([]byte(body), model); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(model)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			_ = json.Unmarshal(encoded, &got)
			_ = json.Unmarshal([]byte(body), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("override changed: %s", encoded)
			}
		}
	}
	var usage generated.UsageSummary
	body := `{"models":[{"model":null,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"requests":1,"cost":"0.00000000001","average_time_to_first_chunk_seconds":null,"output_tokens_per_second":0}]}`
	if err := json.Unmarshal([]byte(body), &usage); err != nil {
		t.Fatal(err)
	}
	if len(usage.Models) != 1 || !usage.Models[0].AverageTimeToFirstChunkSeconds.IsNull() || usage.Models[0].OutputTokensPerSecond.IsNull() || usage.Models[0].Cost.GetOrEmpty() != "0.00000000001" {
		t.Fatalf("usage changed: %#v", usage)
	}
	var session generated.SessionView
	if err := json.Unmarshal([]byte(`{"purpose":"memory_organization","memory":{"enabled":false,"spaces":[]}}`), &session); err != nil {
		t.Fatal(err)
	}
	if session.Purpose == nil || string(*session.Purpose) != "memory_organization" || session.Memory == nil || session.Memory.Enabled == nil || *session.Memory.Enabled || session.Memory.Spaces == nil || len(*session.Memory.Spaces) != 0 {
		t.Fatal("Session memory/purpose lost")
	}
}

func TestCatalogExactFiltersAndProviderSecretOmission(t *testing.T) {
	client := coverageClient(t, func(r *http.Request) *http.Response {
		if r.URL.Query().Get("provider") != "catalog-channel" || r.URL.Query().Get("model") != "Vendor/Case Sensitive+Model" {
			t.Fatal("exact catalog filters changed", r.URL.RawQuery)
		}
		return coverageResponse(200, `{"items":[],"status":"ready"}`)
	})
	api, _ := client.API()
	response, err := api.GetModelCatalog(context.Background(), &generated.GetModelCatalogParams{Provider: Ptr("catalog-channel"), Model: Ptr("Vendor/Case Sensitive+Model")})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	for _, remove := range []bool{false, true} {
		update := generated.ProviderUpdate{Config: Value(map[string]generated.JsonValue{"desktop": false, "execution": "envd_http"}), ExtraHeaders: Ptr(map[string]any{"X-Remove": nil, "X-Keep": "value"})}
		if remove {
			update.Credential = Null[map[string]generated.JsonValue]()
		}
		wire, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		_ = json.Unmarshal(wire, &object)
		credential, present := object["credential"]
		if present != remove || (remove && string(credential) != "null") {
			t.Fatalf("config-only update changed credential disposition: %s", wire)
		}
		if string(object["extra_headers"]) != `{"X-Keep":"value","X-Remove":null}` {
			t.Fatal(string(wire))
		}
	}
}

func TestRetainedImageBinaryAndUnavailableErrors(t *testing.T) {
	for _, media := range []string{"image/png", "image/jpeg"} {
		client := coverageClient(t, func(r *http.Request) *http.Response {
			if r.URL.EscapedPath() != "/proxy/api/v1/runs/run%2Fid/images/image%20%23%25" {
				t.Fatal(r.URL.EscapedPath())
			}
			response := coverageResponse(200, string([]byte{0, 0xff, 0x80, 1}))
			response.Header.Set("Content-Type", media)
			response.Header.Set("Cache-Control", "no-store")
			return response
		})
		api, _ := client.API()
		response, err := api.RunImage(context.Background(), "run/id", "image #%", &generated.RunImageParams{})
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(body) != string([]byte{0, 0xff, 0x80, 1}) || response.Header.Get("Content-Type") != media {
			t.Fatal(body, err)
		}
	}
	client := coverageClient(t, func(r *http.Request) *http.Response {
		return coverageResponse(404, `{"error":{"code":"not_found","message":"Not found","details":{},"request_id":null}}`)
	})
	api, _ := client.API()
	response, err := api.RunImage(context.Background(), "run", "uncommitted", &generated.RunImageParams{})
	_, err = ParseJSON[map[string]any](client, response, err, 200)
	var failure *ApiError
	if !errors.As(err, &failure) || failure.Code != "not_found" {
		t.Fatal(err)
	}
}

func TestComputerImageSupplementStaysStructured(t *testing.T) {
	const event = `{"type":"CUSTOM","name":"a13n.tool_images","subagentRunId":"child","value":{"event":{"tool_call_id":"call","images":[{"id":"image","media_type":"image/png","size_bytes":0}],"unavailable":true}}}`
	frame, err := parseThreadFrame("delta", "1-1", true, []byte(`{"run_id":"run","attempt":1,"sequence":1,"event":`+event+`,"item":null}`))
	if err != nil {
		t.Fatal(err)
	}
	delta := frame.(DeltaFrame)
	if string(delta.Event["name"]) != `"a13n.tool_images"` || string(delta.Event["subagentRunId"]) != `"child"` || string(delta.Event["value"]) != `{"event":{"tool_call_id":"call","images":[{"id":"image","media_type":"image/png","size_bytes":0}],"unavailable":true}}` {
		t.Fatal(delta.Event)
	}
	var item generated.Item
	if err := json.Unmarshal([]byte(`{"id":"item","kind":"tool_call","state":"completed","ordinal":1,"content":{"tool_images":[{"id":"image","media_type":"image/png","size_bytes":0}],"tool_image_unavailable":true,"subagentRunId":"child"}}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.Content["tool_image_unavailable"] != true || item.Content["subagentRunId"] != "child" {
		t.Fatal(item.Content)
	}
}
