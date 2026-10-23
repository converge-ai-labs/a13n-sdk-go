package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestExecutionForwarding(t *testing.T) {
	for _, raw := range []string{`{}`, `{"execution":null}`, `{"execution":{}}`, `{"execution":{"environment":null}}`, `{"execution":{"environment":{}}}`, `{"execution":{"tools":{"mode":"auto","foreground_wait_seconds":0},"steering":"boundary","environment":{"APP_ENV":"test","EMPTY":""}}}`} {
		t.Run(raw, func(t *testing.T) {
			var options RunOptions
			var expected map[string]any
			if err := json.Unmarshal([]byte(raw), &options); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &expected); err != nil {
				t.Fatal(err)
			}
			var bodies []map[string]any
			client := coverageClient(t, func(req *http.Request) *http.Response {
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, body)
				if req.Method == "PATCH" {
					if req.Header.Get("If-Match") != `"v1"` {
						t.Fatal("missing precondition")
					}
					return coverageResponse(200, `{"id":"thr"}`)
				}
				return coverageResponse(201, queuedReceipt)
			})
			ctx := context.Background()
			first, err := client.Agent("agent").Start(ctx, "x", StartOptions{Execution: options.Execution, Options: &options})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			next, err := client.Agent("agent").Send(ctx, "thr", "x", SendOptions{Options: &options})
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			api, _ := client.API()
			response, err := api.UpdateThread(ctx, "thr", &generated.UpdateThreadParams{IfMatch: Ptr(`"v1"`)}, generated.ThreadUpdate{Execution: options.Execution})
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if !reflect.DeepEqual(bodies[0]["options"], expected) || !reflect.DeepEqual(bodies[1]["options"], expected) || !reflect.DeepEqual(bodies[2], expected) {
				t.Fatal(bodies)
			}
			value, present := bodies[0]["execution"]
			wanted, exists := expected["execution"]
			if present != exists || !reflect.DeepEqual(value, wanted) {
				t.Fatal(bodies[0])
			}
			if _, exists := bodies[1]["execution"]; exists {
				t.Fatal("run execution leaked into thread defaults")
			}
		})
	}
}

func TestTypedExecutionZeroAndEmpty(t *testing.T) {
	value := ExecutionOverride{Environment: Value(EnvironmentVariables{"EMPTY": ""}), Tools: Value(ToolExecutionOverride{ForegroundWaitSeconds: Value(float32(0)), Mode: Value(ToolExecutionMode("inline"))}), Steering: Value(Steering("immediate"))}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["environment"].(map[string]any)["EMPTY"] != "" || got["tools"].(map[string]any)["foreground_wait_seconds"] != float64(0) {
		t.Fatal(string(data))
	}
}

func TestHighLevelReadsRequestDetailedAndRejectOtherViews(t *testing.T) {
	for _, kind := range []string{"run", "entry"} {
		for _, shape := range []string{`"view":"compact"`, `"representation":"text"`} {
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if req.URL.Query().Get("view") != "detailed" {
					t.Fatal("missing detailed query", req.URL)
				}
				return coverageResponse(200, `{"id":"record","thread_id":"thread",`+shape+`}`)
			})
			var err error
			if kind == "run" {
				_, err = client.Run("record").Get(context.Background())
			} else {
				_, err = client.Entry("thread", "record").Get(context.Background())
			}
			if !errors.Is(err, ErrProtocol) {
				t.Fatal(kind, shape, err)
			}
		}
	}
}

func TestAgentExecutionUsesCanonicalOverride(t *testing.T) {
	config := generated.AgentConfigInput{Model: "model", Execution: Value(ExecutionOverride{Environment: Value(EnvironmentVariables{"APP_ENV": "agent", "EMPTY": ""})})}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	env := body["execution"].(map[string]any)["environment"]
	if !reflect.DeepEqual(env, map[string]any{"APP_ENV": "agent", "EMPTY": ""}) {
		t.Fatal(env)
	}
}
