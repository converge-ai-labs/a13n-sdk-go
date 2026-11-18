package a13n

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestService010OrganizationAppPlugins(t *testing.T) {
	var seen []string
	client := coverageClient(t, func(req *http.Request) *http.Response {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		seen = append(seen, req.Method+" "+req.URL.Path+" "+req.Header.Get("If-Match")+" "+string(body))
		if req.Header.Get("X-Workspace-ID") != "" {
			t.Error("organization route must not inject workspace scope")
		}
		return coverageResponse(200, `{}`)
	})
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := api.ListAppPlugins(ctx, "org_example"); err != nil {
		t.Fatal(err)
	}
	match := `"app:1"`
	if _, err := api.UpdateAppPlugin(ctx, "org_example", "example.app", &generated.UpdateAppPluginParams{IfMatch: &match}, generated.AppUpdate{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "GET /proxy/api/v1/organizations/org_example/app-plugins  " || seen[1] != `PUT /proxy/api/v1/organizations/org_example/app-plugins/example.app "app:1" {"enabled":false}` {
		t.Fatalf("unexpected requests: %#v", seen)
	}
}

func TestService010SessionFindingAnalysis(t *testing.T) {
	for _, value := range []string{`"fan_01234567890123456789"`, `null`} {
		run := strings.Replace(sessionRunJSON, `"status":`, `"finding_analysis_id":`+value+`,"status":`, 1)
		thread := strings.Replace(sessionThreadJSON, `"current_run":null`, `"current_run":`+run, 1)
		frame, err := parseSessionFrame(sseEvent{event: "thread", data: []byte(thread)})
		if err != nil {
			t.Fatalf("finding_analysis_id=%s: %v", value, err)
		}
		if value != `null` {
			current, err := frame.(*SessionThreadFrame).CurrentRun.Get()
			if err != nil {
				t.Fatal(err)
			}
			id, err := current.FindingAnalysisId.Get()
			if err != nil || id != "fan_01234567890123456789" {
				t.Fatalf("finding analysis ID %q: %v", id, err)
			}
		}
	}
}
