package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestSkillSyncNativeUnionAndWorkflowOverrides(t *testing.T) {
	for _, input := range []string{`{}`, `{"skill_sync":null}`, `{"skill_sync":{}}`, `{"skill_sync":{"mode":"shared","overwrite":false,"cleanup":false}}`, `{"skill_sync":{"mode":"isolated","cache_count":0}}`} {
		for _, value := range []any{&Overrides{}, &generated.AgentOverrideOutput{}} {
			if err := json.Unmarshal([]byte(input), value); err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			_ = json.Unmarshal(wire, &got)
			_ = json.Unmarshal([]byte(input), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%T changed policy: %s", value, wire)
			}
		}
	}
	var shared, isolated SkillSync
	if err := shared.FromSharedSkillSync(SharedSkillSync{Overwrite: Ptr(false), Cleanup: Ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if err := isolated.FromIsolatedSkillSync(IsolatedSkillSync{CacheCount: Ptr(0)}); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []SkillSync{shared, isolated} {
		overrides := Overrides{LazyEnvironment: Value(false), SkillSync: Value(policy)}
		client := coverageClient(t, func(req *http.Request) *http.Response {
			var body generated.NewThread
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			got, err := body.Options.Overrides.Get()
			if err != nil {
				t.Fatal(err)
			}
			lazy, err := got.LazyEnvironment.Get()
			if err != nil || lazy {
				t.Fatal("explicit eager environment lost", err)
			}
			selected, err := got.SkillSync.Get()
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(selected)
			want, _ := json.Marshal(policy)
			if string(wire) != string(want) {
				t.Fatal(string(wire), string(want))
			}
			return coverageResponse(201, queuedReceipt)
		})
		i, err := client.Agent("agent").Start(context.Background(), "prepare", StartOptions{Options: &RunOptions{Overrides: Value(overrides)}})
		if err != nil {
			t.Fatal(err)
		}
		_ = i.Close()
		var config generated.AgentConfigInput
		config.SkillSync = &policy
		wire, _ := json.Marshal(config)
		var readback generated.AgentConfigOutput
		if err := json.Unmarshal(wire, &readback); err != nil || readback.SkillSync == nil {
			t.Fatal(err)
		}
		actual, _ := json.Marshal(readback.SkillSync)
		wanted, _ := json.Marshal(policy)
		if string(actual) != string(wanted) {
			t.Fatal(string(actual))
		}
	}
}

func TestThreadMemorySourcesOptionalAndNativeWire(t *testing.T) {
	for _, suffix := range []string{"", `,"memory_sources":null`, `,"memory_sources":[]`, `,"memory_sources":[{"kind":"record","memory":"notes","status":"recalled","memory_id":null,"cursor":null,"records":[{"id":"rec","sha256":"digest"}],"files":[],"sources_truncated":false},{"kind":"file","memory":"files","status":"changes","files":[{"path":"plan.md","sha256":"digest","bytes":0,"truncated":false}]}]`} {
		item := `{"id":"item","kind":"text_message","state":"completed"` + suffix + `}`
		data := `{"run_id":"run","attempt":1,"sequence":1,"event":{"type":"TEXT_MESSAGE_END","messageId":"reply"},"item":` + item + `}`
		frame, err := parseThreadFrame("delta", "1-1", true, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		reencoded, _ := json.Marshal(frame.(DeltaFrame).Item)
		var got, want any
		_ = json.Unmarshal(reencoded, &got)
		_ = json.Unmarshal([]byte(item), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("optional source evidence changed: %s", reencoded)
		}
	}
	for _, source := range []string{`true`, `[true]`, `[{"kind":"other","memory":"notes","status":"recalled"}]`, `[{"kind":"file","memory":"notes","status":"other"}]`, `[{"kind":"file","memory":"notes","status":"changes","files":[{"path":"a","sha256":"d","bytes":-1}]}]`} {
		data := `{"run_id":"run","attempt":1,"sequence":1,"event":{},"item":{"id":"item","kind":"text_message","state":"completed","memory_sources":` + source + `}}`
		if _, err := parseThreadFrame("delta", "1-1", true, []byte(data)); !errors.Is(err, ErrProtocol) {
			t.Fatal("malformed sources accepted", source, err)
		}
	}
}

func TestRunContentPreservesNativeNumbersAndOriginalEvidence(t *testing.T) {
	for _, value := range []string{`null`, `""`, `{"integer":9007199254740993,"large":1e309,"false":false,"empty":[],"null":null}`} {
		body := `{"id":"content","media_type":"application/json","value":` + value + `,"truncated":false}`
		client := coverageClient(t, func(req *http.Request) *http.Response { return coverageResponse(200, body) })
		api, _ := client.API()
		resp, err := api.GetRunContent(context.Background(), "run", "content", &generated.GetRunContentParams{})
		content, err := ParseJSON[generated.RunContent](client, resp, err, 200)
		if err != nil || string(content.RawJSON()) != body {
			t.Fatal(err, string(content.RawJSON()))
		}
		if object, ok := content.Value.Value.(map[string]any); ok {
			if object["integer"] != json.Number("9007199254740993") || object["large"] != json.Number("1e309") {
				t.Fatal(object)
			}
		}
	}
}

func TestCatchupNineOperationsShareAuthScopeMetadataAndLifetime(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(map[bool]string{false: "key", true: "session"}[session], func(t *testing.T) {
			seen := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				route := req.Method + " " + req.URL.Path
				seen[route]++
				if (session && (req.Header.Get("Authorization") != "" || req.Header.Get("X-Workspace-ID") != "workspace" || (req.Method != "GET" && req.Header.Get("X-CSRF-Token") != "csrf"))) || (!session && (req.Header.Get("Authorization") != "Bearer test-key" || req.Header.Get("X-Workspace-ID") != "")) {
					t.Error("shared auth/scope lost", route)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "catchup")
				w.Header().Set("ETag", `"finding:2"`)
				switch route {
				case "GET /api/v1/findings":
					if req.URL.Query().Get("closed") != "false" || req.URL.Query().Get("cursor") != "next" {
						t.Error("filters lost")
					}
					_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
				case "POST /api/v1/findings":
					var body generated.FindingCreate
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Title != "Finding" || len(body.Evidence) != 0 {
						t.Error(body, err)
					}
					w.WriteHeader(201)
					_, _ = io.WriteString(w, `{"id":"finding","version":1,"evidence":[]}`)
				case "GET /api/v1/findings/finding":
					_, _ = io.WriteString(w, `{"id":"finding","version":2,"evidence":[]}`)
				case "PATCH /api/v1/findings/finding":
					if req.Header.Get("If-Match") != `"finding:1"` {
						t.Error("CAS lost")
					}
					var body generated.FindingUpdate
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || !body.AssessmentNote.IsNull() {
						t.Error(err)
					}
					closed, err := body.Closed.Get()
					if err != nil || closed {
						t.Error("false update lost")
					}
					_, _ = io.WriteString(w, `{"id":"finding","version":2,"evidence":[]}`)
				case "GET /api/v1/finding-analyses":
					_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
				case "POST /api/v1/finding-analyses":
					if req.Header.Get("Idempotency-Key") != "analysis-key" {
						t.Error("analysis key lost")
					}
					var body generated.AnalysisCreate
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || !body.TraceId.IsNull() || body.Presets == nil || len(*body.Presets) != 0 {
						t.Error(body, err)
					}
					w.WriteHeader(201)
					_, _ = io.WriteString(w, `{"id":"analysis","run_id":"run","thread_id":"thread","session_id":"session","selected_traces":[],"read_trace_ids":[],"presets":[]}`)
				case "POST /api/v1/finding-agent":
					_, _ = io.WriteString(w, `{"id":"agent","preset_kind":"finding"}`)
				case "GET /api/v1/sessions/session/message-authors":
					if !reflect.DeepEqual(req.URL.Query()["entry_id"], []string{"one", "two"}) {
						t.Error(req.URL.RawQuery)
					}
					_, _ = io.WriteString(w, `{"items":[]}`)
				case "GET /api/v1/runs/run/contents/content":
					_, _ = io.WriteString(w, `{"id":"content","media_type":"text/plain","value":"preview","truncated":true}`)
				default:
					t.Error("unexpected route", route)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			var opts []ClientOption
			secret := NewSecret("test-key")
			var scope *string
			if session {
				jar, _ := cookiejar.New(nil)
				opts = append(opts, WithSession(jar, func() string { return "csrf" }), WithSessionWorkspace("semantic-only"))
				secret = NewSecret("")
				scope = Ptr("workspace")
			}
			client, err := NewClient(server.URL, secret, nil, opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			api, _ := client.API()
			ctx := context.Background()
			calls := []func() (*http.Response, error){
				func() (*http.Response, error) {
					return api.ListFindings(ctx, &generated.ListFindingsParams{XWorkspaceID: scope, Closed: Ptr(false), Cursor: Ptr("next")})
				},
				func() (*http.Response, error) {
					return api.CreateFinding(ctx, &generated.CreateFindingParams{XWorkspaceID: scope}, generated.FindingCreate{AgentId: "agent", AgentRevisionId: "revision", Category: generated.InstructionIssue, SourceKey: "source", Title: "Finding", Evidence: []generated.Evidence{}})
				},
				func() (*http.Response, error) {
					return api.GetFinding(ctx, "finding", &generated.GetFindingParams{XWorkspaceID: scope})
				},
				func() (*http.Response, error) {
					return api.UpdateFinding(ctx, "finding", &generated.UpdateFindingParams{XWorkspaceID: scope, IfMatch: Ptr(`"finding:1"`)}, generated.FindingUpdate{Closed: Value(false), AssessmentNote: Null[string]()})
				},
				func() (*http.Response, error) {
					return api.ListFindingAnalyses(ctx, &generated.ListFindingAnalysesParams{XWorkspaceID: scope})
				},
				func() (*http.Response, error) {
					return api.StartAnalysis(ctx, &generated.StartAnalysisParams{XWorkspaceID: scope, IdempotencyKey: "analysis-key"}, generated.AnalysisCreate{TraceId: Null[string](), Presets: Ptr([]generated.Preset{})})
				},
				func() (*http.Response, error) {
					return api.PrepareFindingAgent(ctx, &generated.PrepareFindingAgentParams{XWorkspaceID: scope})
				},
				func() (*http.Response, error) {
					return api.GetMessageAuthors(ctx, "session", &generated.GetMessageAuthorsParams{XWorkspaceID: scope, EntryId: []string{"one", "two"}})
				},
				func() (*http.Response, error) {
					return api.GetRunContent(ctx, "run", "content", &generated.GetRunContentParams{XWorkspaceID: scope})
				},
			}
			for _, call := range calls {
				resp, err := call()
				result, err := ParseJSON[map[string]json.RawMessage](client, resp, err, 200, 201)
				if err != nil || result.RequestID() != "catchup" || result.ETag() != `"finding:2"` {
					t.Fatal(err, result)
				}
			}
			if len(seen) != 9 {
				t.Fatal(seen)
			}
			_ = client.Close()
			for _, call := range calls {
				if _, err := call(); !errors.Is(err, ErrClosed) {
					t.Fatal("new operation escaped lifetime", err)
				}
			}
		})
	}
}

func TestRejectedSubmissionReplayKeepsDisposition(t *testing.T) {
	var posts int
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method == "POST" {
			posts++
			return coverageResponse(200, `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr","status":"failed","assigned_run_id":null,"failure":{"code":"invalid_argument","details":{"field":"agent_id"}}},"run":null}`)
		}
		if strings.HasSuffix(req.URL.Path, "/inbox/ent") {
			return coverageResponse(200, `{"id":"ent","thread_id":"thr","status":"failed","assigned_run_id":null,"failure":{"code":"invalid_argument"}}`)
		}
		t.Fatal("rejected entry polled unrelated Run")
		return nil
	})
	i, err := client.Agent("agent").Send(context.Background(), "thr", "same input", SendOptions{IdempotencyKey: "persisted"})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	_, err = i.Result(context.Background())
	var disposition *EntryDispositionError
	if !errors.As(err, &disposition) || disposition.Snapshot.Value.Status != generated.EntryStatusFailed || posts != 1 {
		t.Fatal(err, posts)
	}
}

func TestCatchupNativeContentAndProvenanceShapes(t *testing.T) {
	for _, fixture := range []struct {
		model any
		wire  string
	}{
		{&generated.ProviderModel{}, `{"slug":"native","display_name":"Native"}`},
		{&generated.ProviderModel{}, `{"slug":"native","display_name":"Native","characteristics":null}`},
		{&generated.ProviderModel{}, `{"slug":"native","display_name":"Native","characteristics":{"image_input":{"max_images":0,"support_gif":false}}}`},
		{&generated.ContentRef{}, `{"id":"content","media_type":"text/plain","preview":"prefix","size_bytes":16777216,"truncated":true}`},
		{&generated.MemoryRecordProvenance{}, `{"created":null,"last_write":{"principal_id":null,"run_id":null,"attempt_id":null,"tool_call_id":null,"confirmed_at":"2026-10-13T00:00:00Z"},"content_matches":false}`},
		{&generated.SelectedTrace{}, `{"trace_id":"0123456789abcdef0123456789abcdef","run_id":"run"}`},
	} {
		if err := json.Unmarshal([]byte(fixture.wire), fixture.model); err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(fixture.model)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		_ = json.Unmarshal(wire, &got)
		_ = json.Unmarshal([]byte(fixture.wire), &want)
		for key, value := range want {
			if !reflect.DeepEqual(got[key], value) {
				t.Fatalf("%T lost %s: %s", fixture.model, key, wire)
			}
		}
	}
	for _, value := range []string{"", `,"provenance":null`, `,"provenance":{"created":null,"last_write":{"principal_id":null,"run_id":null,"attempt_id":null,"tool_call_id":null,"confirmed_at":"2026-10-13T00:00:00Z"},"content_matches":false}`} {
		var record generated.MemoryRecordView
		if err := json.Unmarshal([]byte(`{"id":"record"`+value+`}`), &record); err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(record)
		var encoded map[string]json.RawMessage
		_ = json.Unmarshal(wire, &encoded)
		if value == "" {
			if _, ok := encoded["provenance"]; ok {
				t.Fatal("absent provenance filled")
			}
		} else if strings.Contains(value, `"provenance":null`) && !record.Provenance.IsNull() {
			t.Fatal("null provenance lost")
		}
	}
	client := coverageClient(t, func(req *http.Request) *http.Response {
		return coverageResponse(200, `{"run":{"id":"run","thread_id":"thr","status":"completed"},"items":[{"id":"item","ordinal":1,"kind":"text_message","state":"completed","first_stream_id":"1-1","last_stream_id":"1-1","content":{"text":"prefix","memory_sources":[]},"content_refs":{"text":{"id":"content","size_bytes":32769,"media_type":"text/plain","preview":"prefix","truncated":false}}}],"complete":true}`)
	})
	items, err := client.Run("run").Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	refs := items.Value.Items[0].ContentRefs
	if refs == nil || (*refs)["text"].Id != "content" || items.Value.Items[0].Content["text"] != "prefix" {
		t.Fatal("committed ref/preview lost")
	}
}

func TestRunContentRequiresOneCompleteJSONDocument(t *testing.T) {
	body := `{"id":"content","media_type":"application/json","value":1e309}`
	for _, suffix := range []string{`{"value":2}`, ` garbage`, "\n\t "} {
		client := coverageClient(t, func(req *http.Request) *http.Response { return coverageResponse(200, body+suffix) })
		api, _ := client.API()
		raw, err := api.GetRunContent(context.Background(), "run", "content", &generated.GetRunContentParams{})
		content, err := ParseJSON[generated.RunContent](client, raw, err, 200)
		if strings.TrimSpace(suffix) == "" {
			if err != nil || content.Value.Value != json.Number("1e309") {
				t.Fatal("valid trailing whitespace rejected", err)
			}
		} else if !errors.Is(err, ErrProtocol) {
			t.Fatal("trailing content accepted", suffix, err)
		}
	}
}
