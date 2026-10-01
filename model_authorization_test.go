package a13n

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestModelProviderAuthorizationAndDiscoveryNativeWire(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(fmt.Sprintf("session=%t", session), func(t *testing.T) {
			seen := map[string]int{}
			callback := "http://127.0.0.1:1456/auth/callback?code=secret-test-code&state=test-state"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				checkAuth := req.Header.Get("Authorization") == "Bearer key"
				if session {
					checkAuth = req.Header.Get("Authorization") == "" && (req.Method == "GET" || req.Header.Get("X-CSRF-Token") == "csrf")
				}
				if !checkAuth || req.Header.Get("X-Workspace-ID") != "wsp_test" {
					t.Error("auth/scope lost", req.Header)
				}
				route := req.Method + " " + req.URL.Path
				seen[route]++
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "oauth-wire")
				w.Header().Set("Cache-Control", "no-store")
				base := "/api/v1/model-providers/provider"
				switch route {
				case "GET " + base + "/authorization":
					_, _ = io.WriteString(w, `{"provider_id":"provider","state":"connected","pending":false,"subject":null,"client_id":null,"expires_at":null}`)
				case "POST " + base + "/authorize":
					var body generated.ProviderAuthorizationRequest
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.NewRegistration == nil || *body.NewRegistration {
						t.Error("false new_registration omitted", body, err)
					}
					_, _ = io.WriteString(w, `{"attempt_id":"attempt","authorization_url":"https://issuer.invalid/authorize","expires_at":"2026-10-01T01:00:00Z","method":"manual_callback"}`)
				case "POST " + base + "/authorization/callback":
					var body generated.AuthorizationCallback
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.AttemptId != "attempt" || body.CallbackUrl == nil || *body.CallbackUrl != callback {
						t.Error("callback payload changed", err)
					}
					_, _ = io.WriteString(w, `{"provider_id":"provider","state":"connected","pending":false}`)
				case "GET " + base + "/models":
					_, _ = io.WriteString(w, `[{"slug":"native-chat","display_name":"Native Chat"}]`)
				case "DELETE " + base + "/authorization":
					_, _ = io.WriteString(w, `{"local_tokens_cleared":true,"revocation_confirmed":null}`)
				default:
					t.Error("unexpected route", route)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			var opts []ClientOption
			if session {
				jar, _ := cookiejar.New(nil)
				opts = append(opts, WithSession(jar, func() string { return "csrf" }))
			}
			credential := NewSecret("key")
			if session {
				credential = NewSecret("")
			}
			client, err := NewClient(server.URL, credential, nil, opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			api, _ := client.API()
			ctx := context.Background()
			scope := "wsp_test"
			raw, err := api.ModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationGet(ctx, "provider", &generated.ModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationGetParams{XWorkspaceID: &scope})
			status, err := ParseJSON[generated.AuthorizationStatus](client, raw, err, 200)
			if err != nil || status.Value.State != "connected" || status.Value.Pending == nil || *status.Value.Pending || !status.Value.Subject.IsNull() || status.RequestID() != "oauth-wire" || status.Header.Get("Cache-Control") != "no-store" {
				t.Fatal(status, err)
			}
			newRegistration := false
			raw, err = api.AuthorizeModelApiV1ModelProvidersProviderIdAuthorizePost(ctx, "provider", &generated.AuthorizeModelApiV1ModelProvidersProviderIdAuthorizePostParams{XWorkspaceID: &scope}, generated.ProviderAuthorizationRequest{NewRegistration: &newRegistration})
			start, err := ParseJSON[generated.AuthorizationStart](client, raw, err, 200)
			if err != nil || start.Value.AttemptId != "attempt" || start.Value.Method == nil || *start.Value.Method != "manual_callback" {
				t.Fatal(start, err)
			}
			body := generated.AuthorizationCallback{AttemptId: "attempt", CallbackUrl: &callback}
			if strings.Contains(fmt.Sprintf("%+v %#v", body, body), "secret-test-code") {
				t.Fatal("callback secret diagnostic leak")
			}
			raw, err = api.CompleteModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationCallbackPost(ctx, "provider", &generated.CompleteModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationCallbackPostParams{XWorkspaceID: &scope}, body)
			completed, err := ParseJSON[generated.AuthorizationStatus](client, raw, err, 200)
			if err != nil || completed.Value.State != "connected" {
				t.Fatal(completed, err)
			}
			raw, err = api.DiscoverModelProviderModelsApiV1ModelProvidersProviderIdModelsGet(ctx, "provider", &generated.DiscoverModelProviderModelsApiV1ModelProvidersProviderIdModelsGetParams{XWorkspaceID: &scope})
			models, err := ParseJSON[[]generated.ChatGPTModel](client, raw, err, 200)
			if err != nil || !reflect.DeepEqual(models.Value, []generated.ChatGPTModel{{Slug: "native-chat", DisplayName: "Native Chat"}}) {
				t.Fatal(models, err)
			}
			raw, err = api.DisconnectModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationDelete(ctx, "provider", &generated.DisconnectModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationDeleteParams{XWorkspaceID: &scope})
			disconnected, err := ParseJSON[generated.AuthorizationDisconnect](client, raw, err, 200)
			if err != nil || disconnected.Value.LocalTokensCleared == nil || !*disconnected.Value.LocalTokensCleared || !disconnected.Value.RevocationConfirmed.IsNull() {
				t.Fatal(disconnected, err)
			}
			if len(seen) != 5 {
				t.Fatal(seen)
			}
		})
	}
}

func TestModelMediaPoliciesPreserveOmittedNullZeroAndFalse(t *testing.T) {
	for _, input := range []string{`{}`, `{"image_input":null}`, `{"image_input":{}}`,
		`{"image_input":{"max_images":0,"max_image_bytes":0,"max_image_dimension":0,"split_large_images":false,"support_gif":false},"url_input":{"video":[]},"video_input":{"max_video_bytes":4096}}`,
		`{"url_input":{"video":["youtube"]},"video_input":{}}`} {
		var characteristics generated.HarnessModelCharacteristicsInput
		if err := json.Unmarshal([]byte(input), &characteristics); err != nil {
			t.Fatal(err)
		}
		raw, err := generated.NewCreateModelApiV1ModelsPostRequest("https://service.invalid", &generated.CreateModelApiV1ModelsPostParams{}, generated.ModelCreate{Name: "Media", ProviderId: "provider", Config: generated.ModelConfigInput{ModelApi: "other.native", ModelName: "native", Characteristics: &characteristics}})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Config struct {
				Characteristics json.RawMessage `json:"characteristics"`
			} `json:"config"`
		}
		if err := json.NewDecoder(raw.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = raw.Body.Close()
		var got, want any
		_ = json.Unmarshal(body.Config.Characteristics, &got)
		_ = json.Unmarshal([]byte(input), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("media policy coerced: %s", body.Config.Characteristics)
		}
		var output generated.HarnessModelCharacteristicsOutput
		if err := json.Unmarshal(body.Config.Characteristics, &output); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(output)
		_ = json.Unmarshal(encoded, &got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("media readback lost policy: %s", encoded)
		}
	}
}
