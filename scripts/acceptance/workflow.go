package main

// Common workflows in this installed consumer import only the authored root.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
)

func workflowDXOffline() {
	huge := "1" + strings.Repeat("0", 309)
	hugeRun := `{"id":"huge","thread_id":"thr","status":"completed","output":` + huge + `}`
	var posts, streams atomic.Int32
	var previewSealed atomic.Bool
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "root-workflow")
		switch {
		case req.Method == "POST":
			posts.Add(1)
			keys = append(keys, req.Header.Get("Idempotency-Key"))
			check(keys[len(keys)-1] != "", "installed optional key")
			var body map[string]json.RawMessage
			check(json.NewDecoder(req.Body).Decode(&body) == nil, "installed body")
			if keys[len(keys)-1] == "structured-key" {
				check(strings.Contains(string(body["payload"]), `"n":9007199254740993`), "installed exact multipart JSON number")
				var options map[string]json.RawMessage
				check(json.Unmarshal(body["options"], &options) == nil, "installed RunOptions grouping")
				var overrides map[string]json.RawMessage
				check(json.Unmarshal(options["overrides"], &overrides) == nil && len(overrides) == 14, "installed ALL override fields without generated imports")
				for _, name := range []string{"environments", "memories", "message_history"} {
					check(string(body[name]) == "[]", "installed explicit empty "+name)
				}
				check(string(body["agent_revision_id"]) == "null" && string(options["max_usage"]) == "null", "installed Optional null")
			}
			if strings.HasSuffix(req.URL.Path, "/resume") {
				check(string(body["approvals"]) == `{"allow":{"action":"approve"},"deny":{"action":"deny","reason":""}}`, "installed approvals adapter")
				check(string(body["calls"]) == `{"failure":{"message":"local failure","status":"failed"},"value":{"status":"returned","value":null}}` && string(body["input"]) == "null", "installed call adapter and resume input")
				w.WriteHeader(201)
				_, _ = io.WriteString(w, `{"id":"successor","thread_id":"thr","status":"completed","output":"resumed"}`)
				return
			}
			if keys[len(keys)-1] == "code-only-key" {
				w.WriteHeader(504)
				_, _ = io.WriteString(w, `{"error":{"code":"gateway_timeout"}}`)
				return
			}
			if keys[len(keys)-1] == "canonical-rejection-key" {
				w.WriteHeader(409)
				_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"rejected","details":{},"request_id":null}}`)
				return
			}
			if keys[len(keys)-1] == "gateway-key" {
				w.WriteHeader(504)
				_, _ = io.WriteString(w, "<html>gateway timeout</html>")
				return
			}
			w.WriteHeader(201)
			if keys[len(keys)-1] == "huge-receipt-key" {
				_, _ = io.WriteString(w, `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr"},"run":`+hugeRun+`}`)
				return
			}
			if keys[len(keys)-1] == "malformed-key" {
				_, _ = io.WriteString(w, `{"thread":`)
				return
			}
			_, _ = io.WriteString(w, `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr"},"run":null}`)
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			_, _ = io.WriteString(w, `{"id":"ent","thread_id":"thr","status":"consumed","assigned_run_id":"run"}`)
		case strings.HasSuffix(req.URL.Path, "/runs/run"):
			_, _ = io.WriteString(w, `{"id":"run","thread_id":"thr","status":"completed","output":{"n":9007199254740993,"enabled":false,"empty":[]},"pending":null,"failure":null}`)
		case strings.HasSuffix(req.URL.Path, "/runs/huge"):
			_, _ = io.WriteString(w, hugeRun)
		case strings.HasSuffix(req.URL.Path, "/runs/preview"):
			status := "running"
			if previewSealed.Load() {
				status = "completed"
			}
			_, _ = fmt.Fprintf(w, `{"id":"preview","thread_id":"thr","status":%q,"output":"final-not-preview"}`, status)
		case strings.HasSuffix(req.URL.Path, "/stream"):
			streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			events := []string{
				`{"type":"TEXT_MESSAGE_START","messageId":"hidden","role":"assistant","metadata":{"display":false}}`,
				`{"type":"TEXT_MESSAGE_CONTENT","messageId":"hidden","delta":"suppressed"}`,
				`{"type":"TEXT_MESSAGE_END","messageId":"hidden"}`,
				`{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`,
				`{"type":"CUSTOM","name":"a13n.stream.fragment","subagentRunId":"child","value":{"id":"same","index":0,"count":2,"data":"child framing"}}`,
				`{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"preview"}`,
				`{"type":"CUSTOM","name":"a13n.stream.fragment","value":{"id":"opaque","index":0,"count":2,"data":"unsupported custom framing"}}`,
				`{"type":"CUSTOM","name":"future.extension","value":null}`,
			}
			for j, event := range events {
				_, _ = fmt.Fprintf(w, "id: 1-%d\nevent: delta\ndata: {\"run_id\":\"preview\",\"attempt\":1,\"sequence\":%d,\"event\":%s,\"item\":null}\n\n", 2*(j+1), j+1, event)
				if j == 2 {
					_, _ = io.WriteString(w, "id: 1-7\nevent: boundary\ndata: {\"run_id\":\"preview\",\"attempt\":1,\"sequence\":1}\n\n")
				}
			}
			w.(http.Flusher).Flush()
			<-req.Context().Done()
		default:
			panic("unexpected root workflow route: " + req.URL.Path)
		}
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("root-only"), nil))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := must(client.Agent("agent").Start(ctx, "First reply"))
	defer first.Close()
	result := must(first.Result(ctx))
	check(result.Status == a13n.RunCompleted && result.RunID == "run" && result.Pending == nil && result.Failure == nil, "installed direct result")
	decoded := must(a13n.DecodeOutput[struct {
		N int64 `json:"n"`
	}](result))
	check(decoded.N == 9007199254740993, "installed output beyond 2^53")
	check(result.Output.(map[string]any)["n"].(json.Number).String() == "9007199254740993", "installed native output precision")
	check(first.Receipt().IdempotencyKey == keys[0], "installed generated key evidence")
	hugeResult := must(client.Run("huge").Result(ctx))
	check(hugeResult.Output.(json.Number).String() == huge && must(a13n.DecodeOutput[json.Number](hugeResult)).String() == huge, "installed output beyond float64 range")
	check(hugeResult.Evidence().Value.Output.GetOrEmpty().(json.Number).String() == huge && string(hugeResult.Evidence().RawJSON()) == hugeRun, "installed exact typed evidence and original bytes")
	hugeReceipt := must(client.Agent("agent").Start(ctx, "large immediate output", a13n.StartOptions{IdempotencyKey: "huge-receipt-key"}))
	check(hugeReceipt.ReceiptEvidence().Submission.Value.Run.GetOrEmpty().Output.GetOrEmpty().(json.Number).String() == huge, "installed nested receipt exact output")
	check(hugeReceipt.Close() == nil, "installed lazy receipt cleanup")
	receiptBytes := must(json.Marshal(first.Receipt()))
	var saved a13n.Receipt
	check(json.Unmarshal(receiptBytes, &saved) == nil, "installed receipt persistence")
	recovered := must(client.Entry(saved.ThreadID, saved.EntryID).Observe(ctx))
	defer recovered.Close()
	check(recovered.ReceiptEvidence().Submission == nil && must(recovered.Result(ctx)).RunID == result.RunID, "installed lazy Entry recovery")
	follow := must(client.Agent("agent").Send(ctx, result.ThreadID, "Follow up"))
	defer follow.Close()
	check(must(follow.Result(ctx)).RunID == "run" && first.Receipt().IdempotencyKey != follow.Receipt().IdempotencyKey, "installed minimal follow-up has distinct key")
	input := a13n.Input{Content: []a13n.Part{a13n.TextPart(""), a13n.JSONPart(map[string]any{"n": json.Number("9007199254740993")}), a13n.URLPart("https://example.invalid/report"), a13n.AssetPart("asset"), a13n.TextPart("last")}}
	overrides := a13n.Overrides{
		ClientTools:     a13n.Value([]a13n.ClientToolDefinition{{Name: "local", Description: "local", ParametersJsonSchema: map[string]any{"type": "object"}, Permission: a13n.Ptr(a13n.ClientToolDefinitionPermission("allow"))}}),
		ConnectionTools: a13n.Value([]a13n.ConnectionSelection{{ConnectionId: "connection", Permission: a13n.Ptr(a13n.Permission("allow"))}}),
		Instructions:    a13n.Value(""), MediaUnderstanding: a13n.Value(a13n.MediaUnderstandingSelection{}), Model: a13n.Value("model"),
		ModelCharacteristics: a13n.Value(a13n.AgentModelCharacteristics{ContextWindowTokens: a13n.Null[int]()}),
		ModelSettings:        a13n.Value(map[string]any{"extra_body": map[string]any{}, "extra_headers": map[string]any{}}),
		OutputSpec:           a13n.Value(a13n.OutputSpec{Schema: a13n.Value(map[string]any{"type": "object"}), Variants: a13n.Value([]a13n.OutputVariant{})}),
		Plugins:              a13n.Value([]a13n.PluginSelection{}), Retries: a13n.Value(a13n.RetryOverride{Output: a13n.Value(0), Tools: a13n.Null[int]()}),
		Reviewer: a13n.Value(a13n.AgentReviewer{Model: "reviewer", Rules: a13n.Ptr(map[string]a13n.ToolReviewRule{}), TimeoutSeconds: a13n.Ptr(float32(0))}),
		Skills:   a13n.Value([]a13n.SkillSelection{}), Subagents: a13n.Value(map[string]any{"child": nil}),
		Toolsets: a13n.Value(map[string]a13n.ToolsetSelection{"local": {Enabled: a13n.Ptr(false), Config: a13n.Ptr(map[string]any{})}}),
	}
	structured := must(client.Agent("agent").StartInput(ctx, input, a13n.StartOptions{IdempotencyKey: "structured-key",
		AgentRevisionId: a13n.Null[string](), SessionId: a13n.Null[string](), Delivery: a13n.Ptr(a13n.Delivery("next_run")), Kind: a13n.Ptr(a13n.NewThreadKind("message")),
		Environments: a13n.Ptr(a13n.InitialMounts{}), Memories: a13n.Ptr([]a13n.MemoryMount{}), MessageHistory: a13n.Ptr(a13n.MessageHistory{}), McpHeaders: a13n.Ptr(a13n.McpHeaders{}),
		Options: &a13n.RunOptions{Configuration: a13n.Value(a13n.Configuration{AllowedHosts: a13n.Value([]string{}), Extensions: a13n.Ptr(map[string]any{"app": map[string]any{"enabled": false, "zero": 0, "null": nil}})}), Overrides: a13n.Value(overrides), Labels: a13n.Ptr(map[string]string{}), MaxUsage: a13n.Null[a13n.UsageLimit]()}}))
	defer structured.Close()
	check(must(structured.Result(ctx)).Status == a13n.RunCompleted, "installed structured/configured input")
	successor := must(client.Run("waiting").Resume(ctx, a13n.Answers{"allow": a13n.Approve(), "deny": a13n.Deny(a13n.Value("")), "failure": a13n.Failed("local failure"), "value": a13n.Returned(nil)}, a13n.ResumeOptions{Input: a13n.Null[a13n.Input]()}))
	defer successor.Close()
	check(successor.Entry() == nil && successor.Receipt().Origin == "resumed_run" && successor.Receipt().RunID == "successor", "installed no fake resumed Entry")
	resumed := must(successor.Result(ctx))
	text, ok := resumed.Text()
	check(ok && text == "resumed", "installed string convenience")
	preview := must(client.Run("preview").Observe(ctx))
	defer preview.Close()
	var chunks string
	var interrupted *a13n.PreviewInterruptedError
	for chunk, err := range preview.Text() {
		if err != nil {
			check(errors.As(err, &interrupted) && interrupted.Reason == "fragment", "installed typed interruption")
			break
		}
		chunks += chunk
	}
	check(chunks == "preview", "installed root-only preview child-fragment exclusion")
	raw := must(preview.Next())
	check(raw.EventType() == "delta", "installed raw continuation after interruption")
	previewSealed.Store(true)
	check(must(preview.Result(ctx)).Output == "final-not-preview" && streams.Load() == 1, "installed interrupted preview preserves Result and one stream")
	before := posts.Load()
	_, err := client.Agent("agent").Start(ctx, "unknown", a13n.StartOptions{IdempotencyKey: "malformed-key"})
	var unknown *a13n.UnknownOutcomeError
	check(errors.As(err, &unknown) && unknown.IdempotencyKey == "malformed-key" && posts.Load() == before+1, "installed malformed receipt unknown with no retry")
	before = posts.Load()
	_, err = client.Agent("agent").Start(ctx, "gateway", a13n.StartOptions{IdempotencyKey: "gateway-key"})
	check(errors.As(err, &unknown) && unknown.IdempotencyKey == "gateway-key" && unknown.StatusCode == 504 && posts.Load() == before+1, "installed gateway failure remains unknown with no retry")
	before = posts.Load()
	_, err = client.Agent("agent").Start(ctx, "local", a13n.StartOptions{IdempotencyKey: "bad\nkey"})
	check(err != nil && !errors.As(err, &unknown) && posts.Load() == before, "installed invalid header is pre-dispatch")
	before = posts.Load()
	_, err = client.Agent("agent").Start(ctx, "code only", a13n.StartOptions{IdempotencyKey: "code-only-key"})
	check(errors.As(err, &unknown) && errors.Is(err, a13n.ErrProtocol) && unknown.IdempotencyKey == "code-only-key" && unknown.StatusCode == 504 && posts.Load() == before+1, "installed code-only gateway JSON is not a known Service rejection")
	before = posts.Load()
	_, err = client.Agent("agent").Start(ctx, "rejected", a13n.StartOptions{IdempotencyKey: "canonical-rejection-key"})
	var rejection *a13n.ApiError
	check(errors.As(err, &rejection) && !errors.As(err, &unknown) && rejection.Status == 409 && rejection.RequestID == "root-workflow" && posts.Load() == before+1, "installed full canonical ErrorBody remains definite rejection")
	workflowPreviewIntegrityOffline()
	fmt.Println("Installed root-only workflows: first/follow-up, ALL public options, multipart, exact numbers, atomic answers, receipt recovery, preview/raw/result and unknown outcomes passed (local mock)")
}

func workflowPreviewIntegrityOffline() {
	for _, mode := range []string{"initial_boundary", "initial_orphan", "duplicate", "descending", "unmatched_end"} {
		func() {
			var sealed atomic.Bool
			var streams atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(req.URL.Path, "/runs/integrity") {
					status := "running"
					if sealed.Load() {
						status = "completed"
					}
					_, _ = fmt.Fprintf(w, `{"id":"integrity","thread_id":"thr","status":%q,"output":"authoritative"}`, status)
					return
				}
				check(req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/stream"), "installed integrity reader is observation only")
				streams.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				cursor := 0
				frame := func(kind string, sequence int, event string) {
					cursor++
					data := fmt.Sprintf(`{"run_id":"integrity","attempt":1,"sequence":%d}`, sequence)
					if kind == "delta" {
						data = fmt.Sprintf(`{"run_id":"integrity","attempt":1,"sequence":%d,"event":%s,"item":null}`, sequence, event)
					}
					_, _ = fmt.Fprintf(w, "id: 1-%d\nevent: %s\ndata: %s\n\n", cursor, kind, data)
				}
				if mode == "initial_boundary" || mode == "initial_orphan" {
					frame("boundary", 42, "")
					if mode == "initial_orphan" {
						frame("delta", 43, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"orphan"}`)
					} else {
						frame("delta", 43, `{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`)
						frame("delta", 44, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"visible"}`)
						frame("delta", 45, `{"type":"TEXT_MESSAGE_END","messageId":"root"}`)
					}
				} else {
					frame("delta", 1, `{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`)
					frame("delta", 2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"visible"}`)
					sequence, event := 2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"duplicate"}`
					if mode == "descending" {
						sequence = 1
					}
					if mode == "unmatched_end" {
						sequence, event = 3, `{"type":"TEXT_MESSAGE_END","messageId":"unseen"}`
					}
					frame("delta", sequence, event)
				}
				frame("delta", 46, `{"type":"CUSTOM","name":"future.extension","value":null}`)
				w.(http.Flusher).Flush()
				<-req.Context().Done()
			}))
			defer server.Close()
			client := must(a13n.NewClient(server.URL, a13n.NewSecret("root-only"), nil))
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			interaction := must(client.Run("integrity").Observe(ctx))
			defer interaction.Close()
			var interruption *a13n.PreviewInterruptedError
			var chunks string
			for chunk, err := range interaction.Text() {
				if err != nil {
					check(errors.As(err, &interruption), "installed discontinuity has typed interruption")
					break
				}
				chunks += chunk
				if mode == "initial_boundary" {
					break
				}
			}
			if mode == "initial_boundary" {
				check(interruption == nil && chunks == "visible", "installed initial boundary seeds coordinates without interrupting a later matched START")
			} else {
				reason, expected := "sequence_discontinuity", "visible"
				if mode == "unmatched_end" {
					reason = "missing_start"
				}
				if mode == "initial_orphan" {
					reason, expected = "missing_start", ""
				}
				check(interruption != nil && interruption.Reason == reason && chunks == expected, "installed duplicate/descending/orphan-END does not silently repair preview")
				for _, err := range interaction.Text() {
					check(err == interruption, "installed preview interruption remains sticky")
				}
				check(must(interaction.Next()).EventType() == "delta", "installed interrupted reader permits deliberate raw continuation")
			}
			sealed.Store(true)
			check(must(interaction.Result(ctx)).Output == "authoritative" && streams.Load() == 1, "installed integrity interruption preserves Result and one stream")
		}()
	}
	fmt.Println("Installed root-only preview integrity: initial coordinate/no message repair, duplicate/descending delta and unmatched END with sticky interruption/raw/result passed")
}
