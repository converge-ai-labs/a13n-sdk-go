package a13n

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

func importedConversation() generated.MessageHistory {
	return generated.MessageHistory{
		json.RawMessage(`{"kind":"request","parts":[{"part_kind":"user-prompt","content":"What changed?"}]}`),
		json.RawMessage(`{"kind":"response","parts":[{"part_kind":"text","content":"The API changed."}]}`),
		json.RawMessage(`{"kind":"response","parts":[{"part_kind":"tool-call","tool_name":"lookup","tool_call_id":"call-1","args":{"z":9007199254740993,"a":{"last":false,"first":null}}}]}`),
		json.RawMessage(`{"kind":"request","parts":[{"part_kind":"tool-return","tool_name":"lookup","tool_call_id":"call-1","content":{"z":1,"a":2}}]}`),
	}
}

func TestImportedHistoryHighAndLowLevelWireIsSubmitOnly(t *testing.T) {
	history := importedConversation()
	expected, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var posts int
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method == "POST" {
			posts++
			var body map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if req.URL.Path == "/proxy/api/v1/threads" {
				if string(body["message_history"]) != string(expected) {
					t.Fatalf("history wire: got %s, want %s", body["message_history"], expected)
				}
				if req.Header.Get("Idempotency-Key") == "" {
					t.Fatal("missing caller key")
				}
			} else if req.URL.Path == "/proxy/api/v1/threads/thr/inbox" {
				if _, exists := body["message_history"]; exists {
					t.Fatal("continuation re-seeded imported history")
				}
			} else {
				t.Fatalf("unexpected mutation: %s", req.URL.Path)
			}
			return coverageResponse(201, queuedReceipt)
		}
		switch {
		case req.URL.Path == "/proxy/api/v1/threads/thr":
			return coverageResponse(200, `{"id":"thr"}`)
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			return coverageResponse(200, entryView("consumed", `"run"`))
		default:
			return coverageResponse(200, runView("run", "completed", "thr"))
		}
	})
	ctx := context.Background()
	start, err := client.Agent("agent").Start(ctx, "Now answer.", StartOptions{IdempotencyKey: "history-high", MessageHistory: &history})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Close()
	if outcome, err := start.Result(ctx); err != nil || outcome.Run.ID != "run" {
		t.Fatalf("exact initial Run: %v %v", outcome, err)
	}
	view, err := start.Thread().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reflect.TypeOf(view.Value).FieldByName("MessageHistory"); exists || view.ETag() != `"v1"` {
		t.Fatal("Thread readback exposes imported history", view)
	}

	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := api.CreateThreadApiV1ThreadsPost(ctx, &generated.CreateThreadApiV1ThreadsPostParams{IdempotencyKey: "history-low"},
		generated.NewThread{AgentId: "agent", Payload: testTextPayload("Another new Thread"), MessageHistory: &history})
	receipt, err := ParseJSON[generated.Submitted](client, raw, err, 201)
	if err != nil || receipt.StatusCode != 201 || receipt.ETag() != `"v1"` {
		t.Fatalf("raw Create metadata: %v %#v", err, receipt)
	}
	next, err := client.Agent("agent").Send(ctx, start.Thread().ID, "Continue.", SendOptions{IdempotencyKey: "followup"})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if _, err := next.Result(ctx); err != nil {
		t.Fatal(err)
	}
	if posts != 3 {
		t.Fatalf("new/low/continuation mutations: %d", posts)
	}
}

func TestResumeInputAndResultsUseOneHighLevelMutationAndRawReplay(t *testing.T) {
	var asset generated.Part
	if err := asset.FromAssetPart(generated.AssetPart{Type: generated.AssetPartTypeAsset, AssetId: "asset"}); err != nil {
		t.Fatal(err)
	}
	payload := testTextPayload("Please include the attached report.")
	payload.Content = append(payload.Content, asset)
	var approval generated.ApprovalDecision
	if err := approval.FromApprove(generated.Approve{Action: generated.ApproveActionApprove}); err != nil {
		t.Fatal(err)
	}
	var call generated.CallResult
	if err := call.FromReturned(generated.Returned{Status: generated.ReturnedStatusReturned, Value: map[string]any{"summary": "done"}}); err != nil {
		t.Fatal(err)
	}
	request := generated.Resume{Approvals: map[string]generated.ApprovalDecision{"approval-1": approval},
		Calls: map[string]generated.CallResult{"tool-1": call}, Input: nullable.NewNullableWithValue(payload)}
	var postBodies [][]byte
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method != "POST" || req.URL.Path != "/proxy/api/v1/runs/wait/resume" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		postBodies = append(postBodies, encoded)
		if req.Header.Get("Idempotency-Key") != "resume-key" {
			t.Fatal("resume key not forwarded")
		}
		if len(postBodies) == 1 {
			return coverageResponse(201, runView("successor", "accepted", "thr"))
		}
		return coverageResponse(200, runView("successor", "completed", "thr"))
	})
	ctx := context.Background()
	interaction, err := client.Run("wait").Resume(ctx, Answers{"approval-1": Approve(), "tool-1": Returned(map[string]any{"summary": "done"})}, ResumeOptions{IdempotencyKey: "resume-key", Input: Value(InputFromWire(payload))})
	if err != nil {
		t.Fatal(err)
	}
	defer interaction.Close()
	successor, bound := interaction.Run()
	result := interaction.ReceiptEvidence().Resume
	if !bound || successor.ID != "successor" || result.StatusCode != 201 || result.ETag() != `"v1"` || interaction.Entry() != nil {
		t.Fatalf("atomic resume: %v %v", successor, result)
	}
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := api.ResumeRunApiV1RunsRunIdResumePost(ctx, "wait", &generated.ResumeRunApiV1RunsRunIdResumePostParams{IdempotencyKey: "resume-key"}, request)
	replayed, err := ParseJSON[generated.RunView](client, raw, err, 200)
	if err != nil || replayed.Value.Id != successor.ID || replayed.StatusCode != 200 || len(postBodies) != 2 || string(postBodies[0]) != string(postBodies[1]) {
		t.Fatalf("raw replay input/results/headers: %v %v", replayed, err)
	}
	var actual generated.Resume
	if err := json.Unmarshal(postBodies[0], &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Approvals, request.Approvals) || !reflect.DeepEqual(actual.Calls, request.Calls) || !reflect.DeepEqual(actual.Input, request.Input) {
		t.Fatalf("resume body omitted answer or attachment: %s", postBodies[0])
	}
}
