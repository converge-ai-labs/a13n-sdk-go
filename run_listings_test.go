package a13n

import (
	"context"
	"encoding/json"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"net/http"
	"strings"
	"testing"
)

func TestThreadRunDiscoveryMetadataAndExplicitCursors(t *testing.T) {
	count := 0
	preview := strings.Repeat("😀", 200)
	client := coverageClient(t, func(request *http.Request) *http.Response {
		count++
		query := request.URL.Query()
		if query.Has("view") || query.Get("limit") != "2" {
			t.Fatal(query)
		}
		if count == 1 && (query.Get("anchor") != "run" || query.Has("cursor")) {
			t.Fatal(query)
		}
		if count == 2 && (query.Get("cursor") != "older" || query.Has("anchor")) {
			t.Fatal(query)
		}
		item := generated.ThreadRunItem{Id: "run", Number: 501, Cost: Null[string](), InputPreview: generated.RunInputPreview{Text: preview, Truncated: true}}
		body, err := json.Marshal(generated.ThreadRunPage{Items: []generated.ThreadRunItem{item}, Total: 10000, PreviousCursor: Value("newer"), NextCursor: Value("older")})
		if err != nil {
			t.Fatal(err)
		}
		return coverageResponse(200, string(body))
	})
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	first, err := api.ListThreadRuns(context.Background(), "thread", &generated.ListThreadRunsParams{Anchor: Ptr("run"), Limit: Ptr(2)})
	page, err := ParseJSON[generated.ThreadRunPage](client, first, err, 200)
	if err != nil {
		t.Fatal(err)
	}
	if page.Value.Total != 10000 || page.Value.Items[0].Number != 501 || page.Value.Items[0].InputPreview.Text != preview || !page.Value.Items[0].InputPreview.Truncated {
		t.Fatal(page.Value)
	}
	if count != 1 {
		t.Fatal("eager pagination", count)
	}
	next, err := api.ListThreadRuns(context.Background(), "thread", &generated.ListThreadRunsParams{Cursor: Ptr(page.Value.NextCursor.GetOrEmpty()), Limit: Ptr(2)})
	if _, err := ParseJSON[generated.ThreadRunPage](client, next, err, 200); err != nil {
		t.Fatal(err)
	}
}

func TestMetricSortingAndNullableCosts(t *testing.T) {
	client := coverageClient(t, func(request *http.Request) *http.Response {
		query := request.URL.Query()
		if query.Get("sort") != "cost" || query.Get("direction") != "asc" || query.Get("cursor") != "ranked" || query.Has("anchor") {
			t.Fatal(query)
		}
		if strings.HasSuffix(request.URL.Path, "/runs") {
			return coverageResponse(200, `{"items":[],"total":0,"next_cursor":null,"previous_cursor":null}`)
		}
		return coverageResponse(200, `{"items":[],"next_cursor":null}`)
	})
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.ListThreadRuns(context.Background(), "thread", &generated.ListThreadRunsParams{
		Sort: Ptr(generated.ListThreadRunsParamsSortCost), Direction: Ptr(generated.ListThreadRunsParamsDirectionAsc), Cursor: Ptr("ranked"),
	})
	if _, err := ParseJSON[generated.ThreadRunPage](client, response, err, 200); err != nil {
		t.Fatal(err)
	}
	response, err = api.ListSessions(context.Background(), &generated.ListSessionsParams{
		Sort: Ptr(generated.ListSessionsParamsSortCost), Direction: Ptr(generated.ListSessionsParamsDirectionAsc), Cursor: Ptr("ranked"),
	})
	if _, err := ParseJSON[generated.SessionPage](client, response, err, 200); err != nil {
		t.Fatal(err)
	}
	for _, cost := range []string{`null`, `"0"`, `"0.1234567890123456789"`} {
		var item generated.ThreadRunItem
		if err := json.Unmarshal([]byte(`{"number":501,"cost":`+cost+`,"model_calls":0,"unpriced_requests":2}`), &item); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(item.Cost)
		if err != nil || string(encoded) != cost || item.Number != 501 || item.ModelCalls != 0 || item.UnpricedRequests != 2 {
			t.Fatal(item, err)
		}
		var session generated.SessionView
		if err := json.Unmarshal([]byte(`{"run_count":0,"cost":`+cost+`,"unpriced_requests":2}`), &session); err != nil {
			t.Fatal(err)
		}
		encoded, err = json.Marshal(session.Cost)
		if err != nil || string(encoded) != cost || session.RunCount != 0 || session.UnpricedRequests != 2 {
			t.Fatal(session, err)
		}
	}
}
