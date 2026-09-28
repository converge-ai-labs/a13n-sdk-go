package a13n_test

import (
	"context"
	"encoding/json"
	"fmt"
	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGeneratedWireModels(t *testing.T) {
	for _, input := range []string{`{}`, `{"name":null}`, `{"name":"new"}`} {
		var model generated.AgentUpdate
		if err := json.Unmarshal([]byte(input), &model); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(model)
		if err != nil || string(data) != input {
			t.Fatalf("nullable field: %s %v", data, err)
		}
	}
	payload := a13n.TextPayload("hello")
	text, err := payload.Content[0].AsTextPart()
	if err != nil || text.Text != "hello" || text.Type != "text" {
		t.Fatalf("typed union: %#v %v", text, err)
	}
	var secret generated.ProviderCreate
	if err := json.Unmarshal([]byte(`{"credential":{"api_key":"do-not-print"}}`), &secret); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", secret, secret), "do-not-print") {
		t.Fatal("write-only credential leaked in diagnostics")
	}
	wire, err := json.Marshal(secret)
	if err != nil || !strings.Contains(string(wire), "do-not-print") {
		t.Fatal("credential missing from authorized wire serialization")
	}
	var status generated.RunStatus
	if err := json.Unmarshal([]byte(`"future_state"`), &status); err != nil || status != "future_state" {
		t.Fatal("unknown Run status lost")
	}
	var receipt generated.Submitted
	if err := json.Unmarshal([]byte(`{"run":null}`), &receipt); err != nil || !receipt.Run.IsNull() {
		t.Fatal("queued receipt collapsed")
	}
}

func TestModelPriceRuleSelectors(t *testing.T) {
	for _, payload := range []string{
		`{"prices":[],"rule_id":"default"}`,
		`{"prices":[],"rule_id":"default","max_input_tokens":null,"service_tier":null}`,
		`{"prices":[],"rule_id":"default","max_input_tokens":128000,"service_tier":"priority"}`,
	} {
		for _, model := range []any{&generated.ModelPriceRuleInput{}, &generated.ModelPriceRuleOutput{}} {
			if err := json.Unmarshal([]byte(payload), model); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(model)
			if err != nil {
				t.Fatal(err)
			}
			var expected, actual map[string]any
			if err := json.Unmarshal([]byte(payload), &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("%T selectors lost: got %s, want %s", model, encoded, payload)
			}
		}
	}
}

func TestGeneratedHTTPAndBinaryShareTransport(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		w.Header().Set("X-Request-ID", "req_test")
		w.Header().Set("ETag", `"v1"`)
		if strings.HasSuffix(r.URL.Path, "/content") {
			_, _ = w.Write([]byte("streamed"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ws"}`))
	}))
	defer server.Close()
	client, err := a13n.NewClient(server.URL+"/prefix", a13n.NewSecret("test-token"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.GetWorkspaceApiV1WorkspacesWorkspaceIdGetWithResponse(context.Background(), "ws")
	if err != nil || result.JSON200 == nil || result.HTTPResponse.Header.Get("X-Request-ID") != "req_test" {
		t.Fatalf("metadata: %v %v", result, err)
	}
	response, err := client.Asset("ast").Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Close()
	if err != nil || string(data) != "streamed" {
		t.Fatal(err)
	}
	_ = client.Close()
	if _, err := api.GetWorkspaceApiV1WorkspacesWorkspaceIdGet(context.Background(), "ws"); err == nil {
		t.Fatal("closed client allowed request")
	}
	if !reflect.DeepEqual(paths, []string{"/prefix/api/v1/workspaces/ws", "/prefix/api/v1/assets/ast/content"}) {
		t.Fatal(paths)
	}
}

func TestGeneratedRejectsWrongFieldTypes(t *testing.T) {
	source := filepath.Join(t.TempDir(), "invalid.go")
	code := `package invalid
import "github.com/converge-ai-labs/a13n-sdk-go/generated"
var _ = generated.AgentUpdate{Name: 42}
var _ = generated.MessagePayload{Content: 42}
`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("go", "test", source).CombinedOutput()
	if err == nil || strings.Count(string(output), "cannot use 42") != 2 {
		t.Fatalf("expected two type errors: %v: %s", err, output)
	}
}

func TestModelSettingsExtraObjectsPreserveEmptyAndReplacementWire(t *testing.T) {
	override := generated.AgentOverrideInput{ModelSettings: nullable.NewNullableWithValue(map[string]generated.JsonValue{
		"extra_body":    map[string]any{},
		"extra_headers": map[string]any{"X-Trace": "trace-1"},
	})}
	body := generated.NewThread{AgentId: "agent", Payload: a13n.TextPayload("hi"), Options: &generated.RunOptionsInput{Overrides: nullable.NewNullableWithValue(override)}}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	options := decoded["options"].(map[string]any)["overrides"].(map[string]any)["model_settings"].(map[string]any)
	if !reflect.DeepEqual(options["extra_body"], map[string]any{}) || !reflect.DeepEqual(options["extra_headers"], map[string]any{"X-Trace": "trace-1"}) {
		t.Fatalf("model settings object replacement lost: %s", encoded)
	}
	if strings.Contains(string(encoded), `"session_id"`) || strings.Contains(string(encoded), `"agent_revision_id"`) {
		t.Fatal("omitted nullable fields serialized")
	}
}

func TestGeneratedMemoryPathEscapesUnicodeAndReservedCharacters(t *testing.T) {
	var escaped string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		escaped = req.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"first","path":"projects/计划 #1%.md"}`))
	}))
	defer server.Close()
	client, err := a13n.NewClient(server.URL+"/proxy", a13n.NewSecret("key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.ReadFileApiV1MemoriesMemoryIdFilesPathGet(context.Background(), "mem",
		"projects/计划 #1%.md", &generated.ReadFileApiV1MemoriesMemoryIdFilesPathGetParams{})
	result, err := a13n.ParseJSON[generated.MemoryFile](client, response, err, 200)
	if err != nil || result.Value.Path != "projects/计划 #1%.md" {
		t.Fatal(result, err)
	}
	if escaped != "/proxy/api/v1/memories/mem/files/projects%2F%E8%AE%A1%E5%88%92%20%231%25.md" {
		t.Fatalf("path encoding: %s", escaped)
	}
}
