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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check(r.Header.Get("Authorization") == "Bearer test", "native transport ownership")
		check(r.Header.Get("X-Workspace-ID") == "explicit", "explicit workspace")
		check(r.URL.Path == "/api/v1/usage/context" && r.URL.Query().Get("record_id") == "record", "context selector")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"run_id":"run","record_id":"record","input_tokens":null,"output_tokens":0,"context_window_tokens":null}`)
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("test"), nil))
	defer client.Close()
	api := must(client.API())
	scope := "explicit"
	response, err := api.ContextUsage(context.Background(), &generated.ContextUsageParams{RecordId: a13n.Ptr("record"), XWorkspaceID: &scope})
	usage := must(a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)).Value
	check(usage != nil && usage.InputTokens.IsNull() && usage.OutputTokens.GetOrEmpty() == 0 && usage.ContextWindowTokens.IsNull(), "nullable native context usage")
	fmt.Println("Installed module: context usage route and nullable fields passed (local mock)")
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

	} else {
		fmt.Println("Verified HTTPS: no A13N_MODEL_RECORD; context selector equivalence not exercised")
	}
	missing := "run_" + strings.Repeat("0", 24)
	response, err := api.ContextUsage(ctx, &generated.ContextUsageParams{RunId: &missing})
	_, err = a13n.ParseJSON[*generated.ContextUsage](client, response, err, 200)
	var failure *a13n.ApiError
	check(errors.As(err, &failure) && failure.Status == 404, "missing native context target")

	failed := must(client.Agent(agent).Start(ctx, required("A13N_FAILURE_PROMPT")))
	defer failed.Close()
	result := must(failed.Result(ctx))
	check(result.Status == a13n.RunFailed, "dedicated failed Run without completed generation")
	empty := contextRead(ctx, client, generated.ContextUsageParams{RunId: &result.RunID}).Value
	check(empty != nil && empty.RunId == result.RunID && empty.RecordId.IsNull() && empty.InputTokens.IsNull() && empty.OutputTokens.IsNull() && empty.ContextWindowTokens.IsNull(), "fixture failed Run has unknown contribution and capacity")
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
	fmt.Println("Verified HTTPS: context record/run/thread and nullable no-contribution, selector/missing errors, typed proxy revision roundtrip and invalid variants passed; no desktop/provider execution")
}
