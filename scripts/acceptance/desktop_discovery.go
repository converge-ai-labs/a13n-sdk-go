package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func desktopDiscoveryOffline() {
	const browser = "browser_session_0123456789"
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path]++
		check(r.Header.Get("Authorization") == "Bearer test", "new native API transport ownership")
		check(r.Header.Get("X-Workspace-ID") == "explicit", "new native API explicit workspace")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/usage/context":
			check(r.URL.Query().Get("record_id") == "record", "record selector")
			_, _ = io.WriteString(w, `{"run_id":"run","record_id":"record","input_tokens":null,"output_tokens":0,"context_window_tokens":null}`)
		case r.URL.Path == "/api/v1/runs/run/desktop-control" && r.Method == "GET":
			_, _ = io.WriteString(w, `null`)
		case strings.HasSuffix(r.URL.Path, "/view") || strings.HasSuffix(r.URL.Path, "/control") || strings.HasSuffix(r.URL.Path, "/disconnect"):
			var value generated.Control
			check(json.NewDecoder(r.Body).Decode(&value) == nil && value.SessionId == browser && value.Epoch == 0, "browser-session token and epoch zero")
			if strings.HasSuffix(r.URL.Path, "/disconnect") {
				_, _ = io.WriteString(w, `{"id":"assist","state":"awaiting_user"}`)
			} else {
				_, _ = io.WriteString(w, `{"assistance":{"id":"assist"},"mode":"view","websocket_url":"wss://example.invalid/direct","password":"fixture","expires_in_seconds":60}`)
			}
		case strings.HasSuffix(r.URL.Path, "/complete"):
			var value generated.Completion
			check(json.NewDecoder(r.Body).Decode(&value) == nil && value.SessionId == browser && value.Epoch == 0 && value.Note != nil && *value.Note == "done", "native completion body")
			_, _ = io.WriteString(w, `{"id":"assist","state":"ready_to_resume"}`)
		case r.URL.Path == "/api/v1/runs/run/desktop-control" && r.Method == "POST":
			var value generated.Request
			check(json.NewDecoder(r.Body).Decode(&value) == nil && value.Mount == "desktop" && value.Reason == "inspect", "open native mount/reason")
			_, _ = io.WriteString(w, `{"id":"assist","state":"viewing"}`)
		case strings.HasSuffix(r.URL.Path, "/request-control"), r.URL.Path == "/api/v1/runs/run/desktop-assistance/call":
			_, _ = io.WriteString(w, `{"id":"assist","state":"preparing"}`)
		default:
			panic("unexpected native desktop request: " + r.URL.String())
		}
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("test"), nil))
	defer client.Close()
	api := must(client.API())
	ctx := context.Background()
	scope := "explicit"
	response, err := api.GetAssistance(ctx, "run", "call", &generated.GetAssistanceParams{XWorkspaceID: &scope})
	check(must(a13n.ParseJSON[generated.View](client, response, err, 200)).Value.Id == "assist", "native assistance metadata")
	response, err = api.GetControl(ctx, "run", &generated.GetControlParams{XWorkspaceID: &scope})
	check(must(a13n.ParseJSON[*generated.View](client, response, err, 200)).Value == nil, "absent control is JSON null")
	response, err = api.OpenControl(ctx, "run", &generated.OpenControlParams{XWorkspaceID: &scope}, generated.Request{Mount: "desktop", Reason: "inspect"})
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	response, err = api.RequestControl(ctx, "assist", &generated.RequestControlParams{XWorkspaceID: &scope})
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	control := generated.Control{SessionId: browser, Epoch: 0}
	response, err = api.ViewDesktop(ctx, "assist", &generated.ViewDesktopParams{XWorkspaceID: &scope}, control)
	_ = must(a13n.ParseJSON[generated.Access](client, response, err, 200))
	response, err = api.ControlDesktop(ctx, "assist", &generated.ControlDesktopParams{XWorkspaceID: &scope}, control)
	_ = must(a13n.ParseJSON[generated.Access](client, response, err, 200))
	response, err = api.DisconnectDesktop(ctx, "assist", &generated.DisconnectDesktopParams{XWorkspaceID: &scope}, control)
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	response, err = api.CompleteAssistance(ctx, "assist", &generated.CompleteAssistanceParams{XWorkspaceID: &scope}, generated.Completion{SessionId: browser, Epoch: 0, Note: a13n.Ptr("done")})
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	response, err = api.ContextUsage(ctx, &generated.ContextUsageParams{RecordId: a13n.Ptr("record"), XWorkspaceID: &scope})
	usage := must(a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)).Value
	check(usage != nil && usage.InputTokens.IsNull() && usage.OutputTokens.GetOrEmpty() == 0 && usage.ContextWindowTokens.IsNull(), "nullable native context usage")
	check(len(seen) == 9, "all nine native added operations called exactly once")
	for _, n := range seen {
		check(n == 1, "no automatic desktop renew/retry/resume")
	}
	fmt.Println("Installed module: nine native desktop/context routes, browser-session/epoch bodies, nullable context/control and no automatic continuation passed (local mock; no desktop execution)")
}

func contextRead(ctx context.Context, client *a13n.Client, params generated.ContextUsageParams) a13n.Result[*generated.ContextUsage] {
	api := must(client.API())
	response, err := api.ContextUsage(ctx, &params)
	return must(a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200))
}

func desktopDiscoveryLive(ctx context.Context, client *a13n.Client, agent string) {
	api := must(client.API())
	if record := os.Getenv("A13N_MODEL_RECORD"); record != "" {
		byRecord := contextRead(ctx, client, generated.ContextUsageParams{RecordId: &record})
		check(byRecord.Value != nil, "completed model record contribution exists")
		run := must(client.Run(byRecord.Value.RunId).Get(ctx)).Value
		byRun := contextRead(ctx, client, generated.ContextUsageParams{RunId: &run.Id})
		byThread := contextRead(ctx, client, generated.ContextUsageParams{ThreadId: &run.ThreadId})
		check(reflect.DeepEqual(byRecord.Value, byRun.Value) && reflect.DeepEqual(byRecord.Value, byThread.Value), "context record/run/thread select same latest completed contribution")
		check(byRecord.Value.ContextWindowTokens.IsNull(), "fixture missing context window stays null")
		for _, params := range []generated.ContextUsageParams{{}, {RunId: &run.Id, RecordId: &record}} {
			response, err := api.ContextUsage(ctx, &params)
			_, err = a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
			var failure *a13n.ApiError
			check(errors.As(err, &failure) && failure.Status == 400, "Service validates exactly one context selector")
		}
		response, err := api.GetControl(ctx, run.Id, nil)
		check(must(a13n.ParseJSON[*generated.View](client, response, err, 200)).Value == nil, "absent desktop-control stays successful null")
	} else {
		fmt.Println("Verified HTTPS: no A13N_MODEL_RECORD; context selector equivalence not exercised")
	}
	missing := "run_" + strings.Repeat("0", 24)
	response, err := api.ContextUsage(ctx, &generated.ContextUsageParams{RunId: &missing})
	_, err = a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
	var failure *a13n.ApiError
	check(errors.As(err, &failure) && failure.Status == 404, "missing native context target")
	response, err = api.GetControl(ctx, missing, nil)
	_, err = a13n.ParseJSON[*generated.View](client, response, err, 200)
	check(errors.As(err, &failure) && failure.Status == 404, "missing desktop-control target")
	failed := must(client.Agent(agent).Start(ctx, required("A13N_FAILURE_PROMPT")))
	defer failed.Close()
	result := must(failed.Result(ctx))
	check(result.Status == a13n.RunFailed, "dedicated failed Run without completed generation")
	check(contextRead(ctx, client, generated.ContextUsageParams{RunId: &result.RunID}).Value == nil, "no completed contribution is successful JSON null")
	agentResponse, err := api.GetAgent(ctx, agent, nil)
	source := must(a13n.ParseJSON[generated.Agent](client, agentResponse, err, 200)).Value
	revisionResponse, err := api.GetAgentRevision(ctx, agent, must(source.DefaultRevisionId.Get()), nil)
	revision := must(a13n.ParseJSON[generated.AgentRevision](client, revisionResponse, err, 200)).Value
	for _, raw := range []string{
		`{"config":{"mode":"proxy","search_name":"find_grouped_tools","call_name":"invoke_grouped_tool","max_results":7},"groups":{}}`,
	} {
		var search a13n.ToolSearch
		check(json.Unmarshal([]byte(raw), &search) == nil, "typed tool search fixture")
		response, err := api.CreateAgent(ctx, nil, generated.AgentCreate{Name: "Go SDK typed discovery", Config: generated.AgentConfigInput{Model: revision.Config.Model, ToolSearch: a13n.Value(search)}})
		created := must(a13n.ParseJSON[generated.Agent](client, response, err, 201)).Value
		response, err = api.GetAgentRevision(ctx, created.Id, must(created.DefaultRevisionId.Get()), nil)
		stored := must(a13n.ParseJSON[generated.AgentRevision](client, response, err, 200)).Value.Config.ToolSearch.GetOrEmpty()
		check(stored.Groups != nil && len(*stored.Groups) == 0, "stored groups:{} retained")
		value := stored.Config
		check(value != nil && value.MaxResults != nil && *value.MaxResults == 7 && *value.SearchName == "find_grouped_tools", "stored proxy discovery configuration")
	}
	// Invalid configurations cannot enter the typed model. Use the generated raw-body
	// escape hatch to verify the Service boundary separately, not another transport.
	for _, raw := range []string{`{"mode":"native","max_results":7}`, `{"mode":"proxy","strategy":"regex"}`, `{"mode":"native","strategy":"invalid"}`, `{"strategy":"regex"}`} {
		body := fmt.Sprintf(`{"name":"Invalid Go discovery","config":{"model":%q,"tool_search":{"config":%s,"groups":{}}}}`, revision.Config.Model, raw)
		response, err := api.CreateAgentWithBody(ctx, nil, "application/json", strings.NewReader(body))
		_, err = a13n.ParseJSON[generated.Agent](client, response, err, 201)
		check(errors.As(err, &failure) && failure.Status == 400, "Service rejects invalid tool search variants")
	}
	fmt.Println("Verified HTTPS: context record/run/thread and nullable no-contribution/control, selector/missing errors, typed proxy revision roundtrip and invalid variants passed; no desktop/provider execution")
}
