package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// catchupOffline exercises the new native shapes from the independently installed
// ZIP. Real Service reads use catchupReadback after the ordinary live journey.
func catchupOffline() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check(r.Header.Get("Authorization") == "Bearer offline", "catchup native calls share authentication")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "catchup-installed")
		switch r.URL.Path {
		case "/api/v1/runs/run/contents/content":
			_, _ = io.WriteString(w, `{"id":"content","media_type":"application/json","truncated":false,"value":{"z":9007199254740993,"large":1e309,"a":false}}`)
		case "/api/v1/sessions/session/message-authors":
			check(strings.Join(r.URL.Query()["entry_id"], ",") == "one,two", "installed author repeated queries")
			_, _ = io.WriteString(w, `{"items":[{"entry_id":"two","principal_id":"principal","principal":null,"submitted_at":"2026-10-13T00:00:00Z"}]}`)
		case "/api/v1/findings":
			if r.Method == "GET" {
				_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
			} else {
				w.WriteHeader(201)
				_, _ = io.WriteString(w, `{"id":"finding","evidence":[]}`)
			}
		case "/api/v1/findings/finding":
			if r.Method == "PATCH" {
				check(r.Header.Get("If-Match") == `"finding:1"`, "installed finding CAS")
			}
			_, _ = io.WriteString(w, `{"id":"finding","evidence":[]}`)
		case "/api/v1/finding-analyses":
			if r.Method == "GET" {
				_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
			} else {
				check(r.Header.Get("Idempotency-Key") == "analysis", "installed analysis idempotency")
				w.WriteHeader(201)
				_, _ = io.WriteString(w, `{"id":"analysis","run_id":"run","presets":[],"selected_traces":[],"read_trace_ids":[]}`)
			}
		case "/api/v1/finding-agent":
			_, _ = io.WriteString(w, `{"id":"agent","preset_kind":"finding"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
	defer client.Close()
	ctx := context.Background()
	api := must(client.API())
	raw, err := api.RunContentApiV1RunsRunIdContentsContentIdGet(ctx, "run", "content", &generated.RunContentApiV1RunsRunIdContentsContentIdGetParams{})
	content := must(a13n.ParseJSON[generated.RunContent](client, raw, err, 200))
	object := content.Value.Value.(map[string]any)
	check(object["z"] == json.Number("9007199254740993") && object["large"] == json.Number("1e309"), "installed saved native JSON precision")
	raw, err = api.GetMessageAuthorsApiV1SessionsSessionIdMessageAuthorsGet(ctx, "session", &generated.GetMessageAuthorsApiV1SessionsSessionIdMessageAuthorsGetParams{EntryId: []string{"one", "two"}})
	authors := must(a13n.ParseJSON[generated.MessageAuthors](client, raw, err, 200))
	check(len(authors.Value.Items) == 1 && authors.Value.Items[0].EntryId == "two" && authors.Value.Items[0].Principal.IsNull(), "installed authors correlate by ID, not array index")
	raw, err = api.ListFindingsApiV1FindingsGet(ctx, &generated.ListFindingsApiV1FindingsGetParams{Closed: ptr(false)})
	check(must(a13n.ParseJSON[generated.FindingPage](client, raw, err, 200)).Value.NextCursor.IsNull(), "installed finding paging")
	raw, err = api.CreateFindingApiV1FindingsPost(ctx, &generated.CreateFindingApiV1FindingsPostParams{}, generated.FindingCreate{AgentId: "agent", AgentRevisionId: "revision", SourceKey: "source", Category: generated.InstructionIssue, Title: "Finding", Evidence: []generated.Evidence{}})
	check(must(a13n.ParseJSON[generated.Finding](client, raw, err, 201)).Value.Id == "finding", "installed finding creation")
	raw, err = api.GetFindingApiV1FindingsFindingIdGet(ctx, "finding", &generated.GetFindingApiV1FindingsFindingIdGetParams{})
	check(must(a13n.ParseJSON[generated.Finding](client, raw, err, 200)).Value.Id == "finding", "installed finding read")
	raw, err = api.UpdateFindingApiV1FindingsFindingIdPatch(ctx, "finding", &generated.UpdateFindingApiV1FindingsFindingIdPatchParams{IfMatch: ptr(`"finding:1"`)}, generated.FindingUpdate{Closed: a13n.Value(false), AssessmentNote: a13n.Null[string]()})
	check(must(a13n.ParseJSON[generated.Finding](client, raw, err, 200)).Value.Id == "finding", "installed finding update")
	raw, err = api.ListAnalysesApiV1FindingAnalysesGet(ctx, &generated.ListAnalysesApiV1FindingAnalysesGetParams{})
	check(must(a13n.ParseJSON[generated.AnalysisPage](client, raw, err, 200)).Value.NextCursor.IsNull(), "installed analysis paging")
	raw, err = api.StartAnalysisApiV1FindingAnalysesPost(ctx, &generated.StartAnalysisApiV1FindingAnalysesPostParams{IdempotencyKey: "analysis"}, generated.AnalysisCreate{TraceId: a13n.Null[string](), Presets: ptr([]generated.Preset{})})
	check(must(a13n.ParseJSON[generated.Analysis](client, raw, err, 201)).Value.RunId == "run", "installed analysis creation")
	raw, err = api.PrepareFindingAgentApiV1FindingAgentPost(ctx, &generated.PrepareFindingAgentApiV1FindingAgentPostParams{})
	check(must(a13n.ParseJSON[generated.Agent](client, raw, err, 200)).Value.Id == "agent", "installed finding agent preparation")
	var policy a13n.SkillSync
	check(policy.FromIsolatedSkillSync(a13n.IsolatedSkillSync{CacheCount: a13n.Ptr(0)}) == nil, "installed isolated policy constructor")
	overrides := a13n.Overrides{SkillSync: a13n.Value(policy), LazyEnvironment: a13n.Value(false)}
	wire := must(json.Marshal(overrides))
	check(strings.Contains(string(wire), `"cache_count":0`) && strings.Contains(string(wire), `"lazy_environment":false`), "installed native policy union")
	fmt.Println("Installed module: nine added operations, saved JSON precision, authors, finding/analysis paging, native SkillSync passed (local mock)")
}

// catchupReadback adds read-only matching-Service checks; references are expanded
// only here, explicitly, and never inserted into the finite text preview.
func catchupReadback(ctx context.Context, client *a13n.Client, result a13n.RunOutcome, entryID string) {
	api := must(client.API())
	thread := must(client.Thread(result.ThreadID).Get(ctx))
	raw, err := api.GetMessageAuthorsApiV1SessionsSessionIdMessageAuthorsGet(ctx, thread.Value.SessionId, &generated.GetMessageAuthorsApiV1SessionsSessionIdMessageAuthorsGetParams{EntryId: []string{entryID}})
	authors := must(a13n.ParseJSON[generated.MessageAuthors](client, raw, err, 200))
	check(len(authors.Value.Items) == 1 && authors.Value.Items[0].EntryId == entryID, "matching Service authors resolved submitted Entry")
	items := must(result.Run.Items(ctx))
	count := 0
	for _, item := range items.Value.Items {
		if item.ContentRefs == nil {
			continue
		}
		for _, ref := range *item.ContentRefs {
			raw, err = api.RunContentApiV1RunsRunIdContentsContentIdGet(ctx, result.RunID, ref.Id, &generated.RunContentApiV1RunsRunIdContentsContentIdGetParams{})
			content := must(a13n.ParseJSON[generated.RunContent](client, raw, err, 200))
			check(content.Value.Id == ref.Id && string(content.Value.MediaType) == string(ref.MediaType) && ((content.Value.Truncated != nil && *content.Value.Truncated) == (ref.Truncated != nil && *ref.Truncated)), "matching Service content identity/media/truncation")
			count++
		}
	}
	raw, err = api.ListFindingsApiV1FindingsGet(ctx, &generated.ListFindingsApiV1FindingsGetParams{Closed: ptr(false)})
	_ = must(a13n.ParseJSON[generated.FindingPage](client, raw, err, 200))
	raw, err = api.ListAnalysesApiV1FindingAnalysesGet(ctx, &generated.ListAnalysesApiV1FindingAnalysesGetParams{})
	_ = must(a13n.ParseJSON[generated.AnalysisPage](client, raw, err, 200))
	fmt.Printf("Matching Service catchup readback: authors and finding/analysis pages; expanded %d committed content refs\n", count)
}
