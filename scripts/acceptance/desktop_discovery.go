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
	response, err := api.GetAssistanceApiV1RunsRunIdDesktopAssistanceCallIdGet(ctx, "run", "call", &generated.GetAssistanceApiV1RunsRunIdDesktopAssistanceCallIdGetParams{XWorkspaceID: &scope})
	check(must(a13n.ParseJSON[generated.View](client, response, err, 200)).Value.Id == "assist", "native assistance metadata")
	response, err = api.GetControlApiV1RunsRunIdDesktopControlGet(ctx, "run", &generated.GetControlApiV1RunsRunIdDesktopControlGetParams{XWorkspaceID: &scope})
	check(must(a13n.ParseJSON[*generated.View](client, response, err, 200)).Value == nil, "absent control is JSON null")
	response, err = api.OpenControlApiV1RunsRunIdDesktopControlPost(ctx, "run", &generated.OpenControlApiV1RunsRunIdDesktopControlPostParams{XWorkspaceID: &scope}, generated.Request{Mount: "desktop", Reason: "inspect"})
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	response, err = api.RequestControlApiV1DesktopAssistanceAssistanceIdRequestControlPost(ctx, "assist", &generated.RequestControlApiV1DesktopAssistanceAssistanceIdRequestControlPostParams{XWorkspaceID: &scope})
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	control := generated.Control{SessionId: browser, Epoch: 0}
	response, err = api.ViewDesktopApiV1DesktopAssistanceAssistanceIdViewPost(ctx, "assist", &generated.ViewDesktopApiV1DesktopAssistanceAssistanceIdViewPostParams{XWorkspaceID: &scope}, control)
	_ = must(a13n.ParseJSON[generated.Access](client, response, err, 200))
	response, err = api.ControlDesktopApiV1DesktopAssistanceAssistanceIdControlPost(ctx, "assist", &generated.ControlDesktopApiV1DesktopAssistanceAssistanceIdControlPostParams{XWorkspaceID: &scope}, control)
	_ = must(a13n.ParseJSON[generated.Access](client, response, err, 200))
	response, err = api.DisconnectDesktopApiV1DesktopAssistanceAssistanceIdDisconnectPost(ctx, "assist", &generated.DisconnectDesktopApiV1DesktopAssistanceAssistanceIdDisconnectPostParams{XWorkspaceID: &scope}, control)
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	response, err = api.CompleteAssistanceApiV1DesktopAssistanceAssistanceIdCompletePost(ctx, "assist", &generated.CompleteAssistanceApiV1DesktopAssistanceAssistanceIdCompletePostParams{XWorkspaceID: &scope}, generated.Completion{SessionId: browser, Epoch: 0, Note: a13n.Ptr("done")})
	_ = must(a13n.ParseJSON[generated.View](client, response, err, 200))
	response, err = api.ContextUsageApiV1UsageContextGet(ctx, &generated.ContextUsageApiV1UsageContextGetParams{RecordId: a13n.Ptr("record"), XWorkspaceID: &scope})
	usage := must(a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)).Value
	check(usage != nil && usage.InputTokens.IsNull() && usage.OutputTokens.GetOrEmpty() == 0 && usage.ContextWindowTokens.IsNull(), "nullable native context usage")
	check(len(seen) == 9, "all nine native added operations called exactly once")
	for _, n := range seen {
		check(n == 1, "no automatic desktop renew/retry/resume")
	}
	fmt.Println("Installed module: nine native desktop/context routes, browser-session/epoch bodies, nullable context/control and no automatic continuation passed (local mock; no desktop execution)")
}

func contextRead(ctx context.Context, client *a13n.Client, params generated.ContextUsageApiV1UsageContextGetParams) a13n.Result[*generated.ContextUsage] {
	api := must(client.API())
	response, err := api.ContextUsageApiV1UsageContextGet(ctx, &params)
	return must(a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200))
}

func desktopDiscoveryLive(ctx context.Context, client *a13n.Client, agent string) {
	api := must(client.API())
	if record := os.Getenv("A13N_MODEL_RECORD"); record != "" {
		byRecord := contextRead(ctx, client, generated.ContextUsageApiV1UsageContextGetParams{RecordId: &record})
		check(byRecord.Value != nil, "completed model record contribution exists")
		run := must(client.Run(byRecord.Value.RunId).Get(ctx)).Value
		byRun := contextRead(ctx, client, generated.ContextUsageApiV1UsageContextGetParams{RunId: &run.Id})
		byThread := contextRead(ctx, client, generated.ContextUsageApiV1UsageContextGetParams{ThreadId: &run.ThreadId})
		check(reflect.DeepEqual(byRecord.Value, byRun.Value) && reflect.DeepEqual(byRecord.Value, byThread.Value), "context record/run/thread select same latest completed contribution")
		check(byRecord.Value.ContextWindowTokens.IsNull(), "fixture missing context window stays null")
		for _, params := range []generated.ContextUsageApiV1UsageContextGetParams{{}, {RunId: &run.Id, RecordId: &record}} {
			response, err := api.ContextUsageApiV1UsageContextGet(ctx, &params)
			_, err = a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
			var failure *a13n.ApiError
			check(errors.As(err, &failure) && failure.Status == 400, "Service validates exactly one context selector")
		}
		response, err := api.GetControlApiV1RunsRunIdDesktopControlGet(ctx, run.Id, nil)
		check(must(a13n.ParseJSON[*generated.View](client, response, err, 200)).Value == nil, "absent desktop-control stays successful null")
	} else {
		fmt.Println("Verified HTTPS: no A13N_MODEL_RECORD; context selector equivalence not exercised")
	}
	missing := "run_" + strings.Repeat("0", 24)
	response, err := api.ContextUsageApiV1UsageContextGet(ctx, &generated.ContextUsageApiV1UsageContextGetParams{RunId: &missing})
	_, err = a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
	var failure *a13n.ApiError
	check(errors.As(err, &failure) && failure.Status == 404, "missing native context target")
	response, err = api.GetControlApiV1RunsRunIdDesktopControlGet(ctx, missing, nil)
	_, err = a13n.ParseJSON[*generated.View](client, response, err, 200)
	check(errors.As(err, &failure) && failure.Status == 404, "missing desktop-control target")
	failed := must(client.Agent(agent).Start(ctx, required("A13N_FAILURE_PROMPT")))
	defer failed.Close()
	result := must(failed.Result(ctx))
	check(result.Status == a13n.RunFailed, "dedicated failed Run without completed generation")
	check(contextRead(ctx, client, generated.ContextUsageApiV1UsageContextGetParams{RunId: &result.RunID}).Value == nil, "no completed contribution is successful JSON null")
	agentResponse, err := api.GetAgentApiV1AgentsAgentIdGet(ctx, agent, nil)
	source := must(a13n.ParseJSON[generated.Agent](client, agentResponse, err, 200)).Value
	revisionResponse, err := api.GetRevisionApiV1AgentsAgentIdRevisionsRevisionIdGet(ctx, agent, must(source.DefaultRevisionId.Get()), nil)
	revision := must(a13n.ParseJSON[generated.AgentRevision](client, revisionResponse, err, 200)).Value
	for _, raw := range []string{
		`{"config":{"mode":"proxy","search_name":"find_grouped_tools","call_name":"invoke_grouped_tool","max_results":7},"groups":{}}`,
		`{"config":{"mode":"native"},"groups":{}}`, `{"config":{"mode":"native","strategy":null},"groups":{}}`,
		`{"config":{"mode":"native","strategy":"regex"},"groups":{}}`,
	} {
		var search a13n.ToolSearch
		check(json.Unmarshal([]byte(raw), &search) == nil, "typed tool search fixture")
		response, err := api.CreateAgentApiV1AgentsPost(ctx, nil, generated.AgentCreate{Name: "Go SDK typed discovery", Config: generated.AgentConfigInput{Model: revision.Config.Model, ToolSearch: a13n.Value(search)}})
		created := must(a13n.ParseJSON[generated.Agent](client, response, err, 201)).Value
		response, err = api.GetRevisionApiV1AgentsAgentIdRevisionsRevisionIdGet(ctx, created.Id, must(created.DefaultRevisionId.Get()), nil)
		stored := must(a13n.ParseJSON[generated.AgentRevision](client, response, err, 200)).Value.Config.ToolSearch.GetOrEmpty()
		check(stored.Groups != nil && len(*stored.Groups) == 0, "stored groups:{} retained")
		wanted := must(search.Config.ValueByDiscriminator())
		got := must(stored.Config.ValueByDiscriminator())
		if native, ok := wanted.(generated.NativeToolSearchConfig); ok {
			value := got.(generated.NativeToolSearchConfig)
			check(native.Strategy.IsSpecified() == false || reflect.DeepEqual(native.Strategy, value.Strategy), "stored native strategy and explicit null")
		} else {
			value := got.(generated.ProxyToolSearchConfig)
			check(value.MaxResults != nil && *value.MaxResults == 7 && *value.SearchName == "find_grouped_tools", "stored proxy discovery configuration")
		}
	}
	// Invalid variants cannot enter the typed union. Use the generated raw-body
	// escape hatch to verify the Service boundary separately, not another transport.
	for _, raw := range []string{`{"mode":"native","max_results":7}`, `{"mode":"proxy","strategy":"regex"}`, `{"mode":"native","strategy":"invalid"}`, `{"strategy":"regex"}`} {
		body := fmt.Sprintf(`{"name":"Invalid Go discovery","config":{"model":%q,"tool_search":{"config":%s,"groups":{}}}}`, revision.Config.Model, raw)
		response, err := api.CreateAgentApiV1AgentsPostWithBody(ctx, nil, "application/json", strings.NewReader(body))
		_, err = a13n.ParseJSON[generated.Agent](client, response, err, 201)
		check(errors.As(err, &failure) && failure.Status == 400, "Service rejects invalid tool search variants")
	}
	fmt.Println("Verified HTTPS: context record/run/thread and nullable no-contribution/control, selector/missing errors, typed proxy/native revision roundtrip and invalid variants passed; no desktop/provider execution")
}
