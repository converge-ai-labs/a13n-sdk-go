package main

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
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

func configurationOptions(label string) *generated.RunOptionsInput {
	extensions := map[string]generated.JsonValue{"acceptance.settings": map[string]any{"label": label, "enabled": false, "empty": []any{}, "nested": map[string]any{"zero": 0, "null": nil}}}
	return &generated.RunOptionsInput{Configuration: nullable.NewNullableWithValue(generated.RunConfigurationInput{AllowedHosts: nullable.NewNullNullable[[]string](), Extensions: &extensions})}
}

func checkConfiguration(view generated.RunView, expected *generated.RunOptionsInput) {
	actual := must(view.Options.Configuration.Get())
	wanted := must(expected.Configuration.Get())
	check(string(must(json.Marshal(actual))) == string(must(json.Marshal(wanted))), "installed configuration readback retained the full snapshot")
}

// The installed ZIP consumer exercises request/response contracts with a local
// mock. This does not perform a provider login or validate external credentials.
func refreshOffline() {
	options := configurationOptions("offline")
	expected := string(must(json.Marshal(options)))
	posts := 0
	nativeEvent := `{"type":"TOOL_CALL_RESULT","messageId":"same","toolCallId":"call","subagentRunId":"child","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"url","value":"https://example.invalid/a.png"}},{"type":"text","text":"after"}],"metadata":{"display":false,"media":true}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "POST" && (req.URL.Path == "/api/v1/threads" || strings.HasSuffix(req.URL.Path, "/inbox")):
			var body struct {
				Options json.RawMessage `json:"options"`
			}
			check(json.NewDecoder(req.Body).Decode(&body) == nil && string(body.Options) == expected, "installed StartPayload/SendPayload configuration wire")
			posts++
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"thread":{"id":"refresh-thread"},"entry":{"id":"refresh-entry","thread_id":"refresh-thread","status":"pending"},"run":null}`)
		case strings.HasSuffix(req.URL.Path, "/inbox/refresh-entry"):
			_, _ = io.WriteString(w, `{"id":"refresh-entry","thread_id":"refresh-thread","status":"consumed","assigned_run_id":"refresh-run"}`)
		case req.URL.Path == "/api/v1/runs/refresh-run":
			_, _ = fmt.Fprintf(w, `{"id":"refresh-run","thread_id":"refresh-thread","status":"completed","options":%s}`, expected)
		case strings.HasSuffix(req.URL.Path, "/items"):
			_, _ = fmt.Fprintf(w, `{"run":{"id":"refresh-run","thread_id":"refresh-thread","status":"completed","options":%s},"items":[{"id":"child-result","kind":"tool_call","state":"completed","first_stream_id":"1-1","last_stream_id":"1-1","content":{"subagentRunId":"child","result_parts":[{"type":"image","source":{"type":"url","value":"https://example.invalid/a.png"}}],"truncated":true}}],"position":"1-1","resume_after":null,"complete":true,"dropped":2}`, expected)
		case strings.HasSuffix(req.URL.Path, "/stream"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "id: 1-1\nevent: delta\ndata: {\"run_id\":\"refresh-run\",\"attempt\":1,\"sequence\":1,\"event\":%s,\"item\":null}\n\n", nativeEvent)
		case req.URL.Path == "/api/v1/model-providers/provider/authorization":
			if req.Method == "DELETE" {
				_, _ = io.WriteString(w, `{"local_tokens_cleared":true,"revocation_confirmed":null}`)
			} else {
				_, _ = io.WriteString(w, `{"provider_id":"provider","state":"disconnected","subject":null,"pending":false}`)
			}
		case req.URL.Path == "/api/v1/model-providers/provider/authorize":
			_, _ = io.WriteString(w, `{"attempt_id":"attempt","authorization_url":"https://issuer.invalid/authorize","expires_at":"2026-10-01T01:00:00Z","method":"manual_callback"}`)
		case req.URL.Path == "/api/v1/model-providers/provider/authorization/callback":
			_, _ = io.WriteString(w, `{"provider_id":"provider","state":"connected"}`)
		case req.URL.Path == "/api/v1/model-providers/provider/models":
			_, _ = io.WriteString(w, `[{"slug":"native-chat","display_name":"Native Chat"}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
	defer client.Close()
	ctx := context.Background()
	payload := a13n.TextPayload("Native configuration")
	first := must(client.Agent("agent").StartPayload(ctx, payload, a13n.StartOptions{RequestKey: "start-refresh", Options: options}))
	checkConfiguration(must(first.Result(ctx)).Snapshot.Value, options)
	_ = first.Close()
	sent := must(client.Agent("agent").SendPayload(ctx, "refresh-thread", payload, a13n.SendOptions{RequestKey: "send-refresh", Options: options}))
	outcome := must(sent.Result(ctx))
	checkConfiguration(outcome.Snapshot.Value, options)
	_ = sent.Close()
	check(posts == 2, "installed native configuration submissions")
	for _, value := range []string{`{}`, `{"configuration":null}`, `{"configuration":{}}`, `{"configuration":{"allowed_hosts":[],"extensions":{}}}`} {
		var selected generated.RunOptionsInput
		check(json.Unmarshal([]byte(value), &selected) == nil, "installed native configuration tri-state decode")
		expected = string(must(json.Marshal(selected)))
		interaction := must(client.Agent("agent").SendPayload(ctx, "refresh-thread", payload, a13n.SendOptions{RequestKey: key(), Options: &selected}))
		view := must(interaction.Result(ctx)).Snapshot.Value
		check(string(must(json.Marshal(view.Options))) == expected, "installed omitted/null/empty configuration request and readback")
		_ = interaction.Close()
	}
	expected = string(must(json.Marshal(options)))
	items := must(outcome.Run.Items(ctx))
	check(items.Value.Dropped == 2 && items.Value.Items[0].Content["subagentRunId"] == "child" && items.Value.Items[0].Content["truncated"] == true, "installed media/attribution readback")
	api := must(client.API())
	stream := must(api.ThreadStreamApiV1ThreadsThreadIdStreamGet(ctx, "refresh-thread", &generated.ThreadStreamApiV1ThreadsThreadIdStreamGetParams{}))
	wire := string(must(io.ReadAll(stream.Body)))
	_ = stream.Body.Close()
	check(strings.Contains(wire, nativeEvent), "installed native AG-UI multipart payload unchanged")
	raw, err := api.ModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationGet(ctx, "provider", &generated.ModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationGetParams{})
	check(must(a13n.ParseJSON[generated.AuthorizationStatus](client, raw, err, 200)).Value.Subject.IsNull(), "installed provider status null")
	raw, err = api.AuthorizeModelApiV1ModelProvidersProviderIdAuthorizePost(ctx, "provider", &generated.AuthorizeModelApiV1ModelProvidersProviderIdAuthorizePostParams{}, generated.ProviderAuthorizationRequest{})
	check(must(a13n.ParseJSON[generated.AuthorizationStart](client, raw, err, 200)).Value.AttemptId == "attempt", "installed authorize operation")
	callback := "http://127.0.0.1:1456/auth/callback?code=test&state=test"
	body := generated.AuthorizationCallback{AttemptId: "attempt", CallbackUrl: &callback}
	check(!strings.Contains(fmt.Sprintf("%+v", body), "code=test"), "installed callback diagnostic redaction")
	raw, err = api.CompleteModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationCallbackPost(ctx, "provider", &generated.CompleteModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationCallbackPostParams{}, body)
	check(must(a13n.ParseJSON[generated.AuthorizationStatus](client, raw, err, 200)).Value.State == "connected", "installed completion operation")
	raw, err = api.DiscoverModelProviderModelsApiV1ModelProvidersProviderIdModelsGet(ctx, "provider", &generated.DiscoverModelProviderModelsApiV1ModelProvidersProviderIdModelsGetParams{})
	check(must(a13n.ParseJSON[[]generated.ChatGPTModel](client, raw, err, 200)).Value[0].Slug == "native-chat", "installed discovery operation")
	raw, err = api.DisconnectModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationDelete(ctx, "provider", &generated.DisconnectModelAuthorizationApiV1ModelProvidersProviderIdAuthorizationDeleteParams{})
	check(must(a13n.ParseJSON[generated.AuthorizationDisconnect](client, raw, err, 200)).Value.RevocationConfirmed.IsNull(), "installed disconnect operation")
	refreshInteractionOffline()
	fmt.Println("Installed module: native configuration StartPayload/SendPayload/readback, AG-UI media/child content and five OAuth/discovery operations passed (local mock)")
}

func refreshInteractionOffline() {
	var sealed atomic.Bool
	native := `{"type":"RUN_FINISHED","threadId":"inline-child-thread","runId":"inline-child","outcome":{"type":"success"}}`
	parts := `{"type":"TOOL_CALL_RESULT","toolCallId":"same","messageId":"same","subagentRunId":"inline-child","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"url","value":"https://example.invalid/a.png"}},{"type":"text","text":"after"}],"metadata":{"display":false,"media":true}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "POST":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"thread":{"id":"refresh-thread"},"entry":{"id":"refresh-entry","thread_id":"refresh-thread","status":"pending"},"run":null}`)
		case strings.HasSuffix(req.URL.Path, "/inbox/refresh-entry"):
			_, _ = io.WriteString(w, `{"id":"refresh-entry","thread_id":"refresh-thread","status":"consumed","assigned_run_id":"refresh-run"}`)
		case req.URL.Path == "/api/v1/runs/refresh-run":
			status := "running"
			if sealed.Load() {
				status = "completed"
			}
			_, _ = fmt.Fprintf(w, `{"id":"refresh-run","thread_id":"refresh-thread","status":%q}`, status)
		case strings.HasSuffix(req.URL.Path, "/stream"):
			w.Header().Set("Content-Type", "text/event-stream")
			for sequence, event := range []string{native, parts} {
				_, _ = fmt.Fprintf(w, "id: 10-%d\nevent: delta\ndata: {\"run_id\":\"refresh-run\",\"attempt\":1,\"sequence\":%d,\"event\":%s,\"item\":null}\n\n", sequence+1, sequence+1, event)
			}
			w.(http.Flusher).Flush()
			<-req.Context().Done()
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	interaction := must(client.Agent("agent").Start(ctx, "Inline child", a13n.StartOptions{RequestKey: key()}))
	defer interaction.Close()
	frame := must(interaction.Next()).(a13n.DeltaFrame)
	check(frame.RunID == "refresh-run" && string(frame.Event["runId"]) == `"inline-child"`, "installed native child terminal is not Service identity")
	frame = must(interaction.Next()).(a13n.DeltaFrame)
	check(string(frame.Event["subagentRunId"]) == `"inline-child"` && strings.Contains(string(frame.Event["content"]), `"type":"image"`), "installed finite parser preserves child multipart content")
	canceled, stop := context.WithCancel(ctx)
	stop()
	_, err := interaction.Result(canceled)
	check(errors.Is(err, context.Canceled), "installed child terminal cannot finish root")
	sealed.Store(true)
	result := must(interaction.Result(ctx))
	check(result.Run.ID == "refresh-run" && result.Status() == generated.RunStatusCompleted, "installed exact authoritative root result")
	_, err = interaction.Next()
	check(errors.Is(err, io.EOF), "installed root seal ends finite parser")
}

// Conflict is an active-Run precondition, not a property of a sealed waiting
// Run. Keep this probe on its own Thread so it cannot add an Entry to the
// separate next_run/resume scenario if the assertion fails.
func activeConfigurationConflictLive(ctx context.Context, client *a13n.Client, agentID string) {
	options := configurationOptions("active-probe")
	active := must(client.Agent(agentID).StartPayload(ctx,
		a13n.TextPayload("[interruptible] [slow] [long] Verify an active configuration snapshot."),
		a13n.StartOptions{RequestKey: key(), Options: options}))
	defer active.Close()
	ready, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var exactRun string
	for {
		if active.Run != nil {
			exactRun = active.Run.ID
		}
		if exactRun == "" {
			entry := must(active.Entry.Get(ready))
			if entry.Value.Status == generated.EntryStatusConsumed {
				exactRun = entry.Value.AssignedRunId.GetOrEmpty()
			}
		}
		if exactRun != "" {
			run := must(client.Run(exactRun).Get(ready))
			thread := must(active.Thread.Get(ready))
			if run.Value.Status == generated.RunStatusRunning && thread.Value.CurrentRunId.GetOrEmpty() == exactRun {
				checkConfiguration(run.Value, options)
				break
			}
			check(run.Value.Status == generated.RunStatusAccepted || run.Value.Status == generated.RunStatusRunning,
				"active configuration probe sealed before the still-running check")
		}
		select {
		case <-ready.Done():
			panic("active configuration probe did not reach exact running Thread/Run before deadline")
		case <-ticker.C:
		}
	}
	_, err := client.Agent(agentID).SendPayload(ctx, active.Thread.ID,
		a13n.TextPayload("Attempt incompatible steering."),
		a13n.SendOptions{RequestKey: key(), Options: configurationOptions("different")})
	var immutable *a13n.ApiError
	check(errors.As(err, &immutable) && immutable.Status == 409 && string(immutable.Details["reason"]) == `"run_configuration_immutable"`,
		"Service-owned configuration steering conflict on the exact active Run")
	outcome := must(active.Result(ctx))
	check(outcome.Run.ID == exactRun && outcome.Status() == generated.RunStatusCompleted,
		"active configuration conflict probe ends on its exact original Run")
	checkConfiguration(outcome.Snapshot.Value, options)
	fmt.Println("Verified HTTPS: exact active Thread/Run configuration steering conflict and unchanged final snapshot passed")
}
