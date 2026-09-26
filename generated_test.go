package a13n_test

import (
	"context"
	"encoding/json"
	"fmt"
	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
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
	response, err := client.Resources().Workspaces().Ref("ws").Assets().Ref("ast").Content().Get(context.Background())
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
	if !reflect.DeepEqual(paths, []string{"/prefix/api/v1/workspaces/ws", "/prefix/api/v1/workspaces/ws/assets/ast/content"}) {
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
