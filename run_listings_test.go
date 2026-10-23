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
		item := generated.RunListItem{Id: "run", InputPreview: generated.RunInputPreview{Text: preview, Truncated: true}}
		body, err := json.Marshal(generated.ThreadRunPage{Items: []generated.RunListItem{item}, Total: 10000, FirstNumber: 501, PreviousCursor: Value("newer"), NextCursor: Value("older")})
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
	if page.Value.Total != 10000 || page.Value.FirstNumber != 501 || page.Value.Items[0].InputPreview.Text != preview || !page.Value.Items[0].InputPreview.Truncated {
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
