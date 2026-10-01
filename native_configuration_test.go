package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

// Compare actual decoded wire JSON, not just the Go options pointer.
func TestNativeConfigurationForwardingStartSendAndMessage(t *testing.T) {
	cases := []string{
		`{}`, `{"configuration":null}`, `{"configuration":{}}`,
		`{"configuration":{"allowed_hosts":null}}`,
		`{"configuration":{"allowed_hosts":[],"extensions":{}}}`,
		`{"configuration":{"allowed_hosts":["EXAMPLE.COM","regex:.*\\.example\\.com"],"extensions":{"app.settings":{"enabled":false,"empty":[],"object":{},"nullable":null,"nested":{"n":0,"text":""}}}},"overrides":{"instructions":"Distinct Agent override"}}`,
	}
	var payload generated.MessagePayload
	payloadJSON := `{"content":[{"type":"text","text":"Inspect media without rewriting it."},{"type":"json","value":{"enabled":false}},{"type":"url","url":"https://media.example/video.mp4"},{"type":"asset","asset_id":"ast_video"}]}`
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			var options generated.RunOptionsInput
			if err := json.Unmarshal([]byte(input), &options); err != nil {
				t.Fatal(err)
			}
			var expectedOptions any
			_ = json.Unmarshal([]byte(input), &expectedOptions)
			var expectedPayload any
			_ = json.Unmarshal([]byte(payloadJSON), &expectedPayload)
			var mu sync.Mutex
			posted := 0
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if req.Method == "POST" {
					data, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					var body map[string]any
					if err := json.Unmarshal(data, &body); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(body["options"], expectedOptions) || !reflect.DeepEqual(body["payload"], expectedPayload) {
						t.Fatalf("configuration or payload rewritten: %s", data)
					}
					mu.Lock()
					posted++
					mu.Unlock()
					return coverageResponse(201, queuedReceipt)
				}
				if strings.HasSuffix(req.URL.Path, "/inbox/ent") {
					return coverageResponse(200, entryView("consumed", `"run"`))
				}
				if strings.HasSuffix(req.URL.Path, "/runs/run") {
					return coverageResponse(200, runView("run", "completed", "thr"))
				}
				return coverageResponse(500, `{"error":{"code":"unexpected","message":"unexpected read"}}`)
			})
			ctx := context.Background()
			started, err := client.Agent("agent").StartPayload(ctx, payload, StartOptions{RequestKey: "start", Options: &options})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := started.Result(ctx); err != nil {
				t.Fatal(err)
			}
			_ = started.Close()
			sent, err := client.Agent("agent").SendPayload(ctx, "thr", payload, SendOptions{RequestKey: "send", Options: &options})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sent.Result(ctx); err != nil {
				t.Fatal(err)
			}
			_ = sent.Close()
			api, _ := client.API()
			raw, err := api.SubmitMessageApiV1ThreadsThreadIdInboxPost(ctx, "thr", &generated.SubmitMessageApiV1ThreadsThreadIdInboxPostParams{IdempotencyKey: "raw"}, generated.Message{AgentId: "agent", Payload: payload, Options: &options})
			if _, err := ParseJSON[generated.Submitted](client, raw, err, 201); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			count := posted
			mu.Unlock()
			if count != 3 {
				t.Fatal(count)
			}
			// Output options retain exactly the same native configuration values.
			var readback generated.RunOptionsOutput
			if err := json.Unmarshal([]byte(input), &readback); err != nil {
				t.Fatal(err)
			}
			output, err := json.Marshal(readback)
			if err != nil {
				t.Fatal(err)
			}
			var decoded any
			_ = json.Unmarshal(output, &decoded)
			if !reflect.DeepEqual(decoded, expectedOptions) {
				t.Fatalf("output lost configuration: %s", output)
			}
		})
	}
}

func TestConfigurationConflictRemainsServiceOwned(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		return coverageResponse(409, `{"error":{"code":"conflict","message":"Run configuration is frozen","details":{"reason":"run_configuration_immutable"}}}`)
	})
	options := generated.RunOptionsInput{Configuration: nullable.NewNullableWithValue(generated.RunConfigurationInput{AllowedHosts: nullable.NewNullableWithValue([]string{})})}
	_, err := client.Agent("agent").Send(context.Background(), "thr", "Different policy", SendOptions{RequestKey: "conflict", Options: &options})
	var apiErr *ApiError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "conflict" || string(apiErr.Details["reason"]) != `"run_configuration_immutable"` || calls != 1 {
		t.Fatal(err, calls)
	}
}
