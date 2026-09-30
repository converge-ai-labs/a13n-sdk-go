package a13n

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestRunItemsRecoveryHintsAndGeneratedStreamWire(t *testing.T) {
	for _, hint := range []string{"", `,"resume_after":null`, `,"resume_after":"123-4"`} {
		t.Run(hint, func(t *testing.T) {
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if strings.HasSuffix(req.URL.Path, "/items") {
					return coverageResponse(200, `{"run":{"id":"run","thread_id":"thr","status":"running"},"items":[],"position":"1-8","dropped":3,"complete":false`+hint+`}`)
				}
				if req.URL.Query().Get("run") != "run" || req.URL.Query().Get("position") != "1-8" {
					t.Fatal(req.URL)
				}
				expected := ""
				if strings.Contains(hint, "123-4") {
					expected = "123-4"
				}
				if req.Header.Get("Last-Event-ID") != expected {
					t.Fatal(req.Header)
				}
				response := coverageResponse(200, gapEvent)
				response.Header.Set("Content-Type", "text/event-stream")
				return response
			})
			snapshot, err := client.Run("run").Items(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Value.Dropped != 3 || snapshot.Value.Complete || snapshot.Value.Run.Id != "run" {
				t.Fatal(snapshot)
			}
			value := snapshot.Value
			if hint == "" && value.ResumeAfter.IsSpecified() {
				t.Fatal("absent hint lost")
			}
			if strings.Contains(hint, "null") && !value.ResumeAfter.IsNull() {
				t.Fatal("null hint lost")
			}
			api, _ := client.API()
			runID, position := value.Run.Id, value.Position.GetOrEmpty()
			params := generated.ThreadStreamApiV1ThreadsThreadIdStreamGetParams{Run: &runID, Position: &position}
			if after := value.ResumeAfter.GetOrEmpty(); after != "" {
				params.LastEventID = &after
			}
			response, err := api.ThreadStreamApiV1ThreadsThreadIdStreamGet(context.Background(), "thr", &params)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
		})
	}
}

func TestOtherModelNativeSettingsForwardUnchanged(t *testing.T) {
	settings := map[string]generated.JsonValue{"nested": map[string]any{"enabled": true, "nullable": nil, "values": []any{1, "x", false}}, "counter": 42}
	request, err := generated.NewCreateModelApiV1ModelsPostRequest("https://service.invalid", &generated.CreateModelApiV1ModelsPostParams{}, generated.ModelCreate{Name: "Native", ProviderId: "prv_x", Config: generated.ModelConfigInput{ModelApi: "other.native", ModelName: "native", Settings: &settings}})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Config struct {
			Settings map[string]any `json:"settings"`
		} `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = request.Body.Close()
	expected, _ := json.Marshal(settings)
	actual, _ := json.Marshal(body.Config.Settings)
	if string(actual) != string(expected) {
		t.Fatalf("native settings rewritten: %s != %s", actual, expected)
	}
	for _, input := range []string{`{"model_api":"other.native","model_name":"native"}`, `{"model_api":"other.native","model_name":"native","settings":{}}`} {
		var value generated.ModelConfigOutput
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(value)
		if string(data) != input {
			t.Fatalf("output omission/empty lost: %s", data)
		}
	}
}

func TestUploadIDAndEightCharacterPasswordWire(t *testing.T) {
	uploadID := "upl_" + strings.Repeat("a", 32)
	var upload generated.Upload
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"upload_id":%q,"filename":"file","content_type":"text/plain","size":1,"digest":"digest"}`, uploadID)), &upload); err != nil {
		t.Fatal(err)
	}
	request, err := generated.NewCreateAssetApiV1AssetsPostRequest("https://service.invalid", &generated.CreateAssetApiV1AssetsPostParams{}, generated.AssetCreate{Name: "file", UploadId: upload.UploadId})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(request.Body)
	_ = request.Body.Close()
	if !strings.Contains(string(data), uploadID) {
		t.Fatal("upload reference truncated")
	}
	password := "eight888"
	change := generated.PasswordChange{CurrentPassword: &password, Password: &password}
	reset := generated.PasswordResetConfirm{Password: &password, Token: strings.Repeat("x", 16)}
	changed, err := generated.NewChangePasswordApiV1UsersMePasswordPostRequest("https://service.invalid", change)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := generated.NewConfirmPasswordResetApiV1AuthPasswordResetConfirmPostRequest("https://service.invalid", reset)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*http.Request{changed, confirmed} {
		data, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if !strings.Contains(string(data), `"password":"eight888"`) {
			t.Fatal("password removed from authorized wire")
		}
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", change, reset), password) {
		t.Fatal("password leaked in diagnostics")
	}
	// Pin the schema evidence without implementing its policy on the client.
	document, err := os.ReadFile("contract/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(document, &spec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PasswordChange", "PasswordResetConfirm"} {
		if spec.Components.Schemas[name].Properties["password"]["minLength"] != float64(8) {
			t.Fatal("password schema changed", name)
		}
	}
	pattern := spec.Components.Schemas["AssetCreate"].Properties["upload_id"]["pattern"].(string)
	if !regexp.MustCompile(pattern).MatchString(uploadID) {
		t.Fatal("32-hex upload ID not in contract")
	}
	settingsSchema := spec.Components.Schemas["ModelConfig-Input"].Properties["settings"]
	if !reflect.DeepEqual(settingsSchema["additionalProperties"], map[string]any{"$ref": "#/components/schemas/JsonValue"}) {
		t.Fatal(settingsSchema)
	}
}
