package a13n

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestAppsCatalogsAndUsageNativeDispatch(t *testing.T) {
	ctx := context.Background()
	var method, path, body string
	var query map[string][]string
	var etag string
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method != method || req.URL.EscapedPath() != "/proxy/api/v1"+path {
			t.Fatalf("route: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("If-Match") != etag {
			t.Fatalf("If-Match: %q", req.Header.Get("If-Match"))
		}
		for key, want := range query {
			if !reflect.DeepEqual(req.URL.Query()[key], want) {
				t.Fatalf("query %s: %v", key, req.URL.Query())
			}
		}
		var wire []byte
		if req.Body != nil {
			wire, _ = io.ReadAll(req.Body)
		}
		if body == "" {
			if len(wire) != 0 {
				t.Fatalf("unexpected body %s", wire)
			}
		} else {
			var got, want any
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(body), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("body: %s", wire)
			}
		}
		if method == "DELETE" {
			return coverageResponse(204, "")
		}
		if strings.HasSuffix(path, "/content") {
			result := coverageResponse(200, "PK\x00\xff")
			result.Header.Set("Content-Type", "application/zip")
			return result
		}
		return coverageResponse(200, `{"items":[],"next_cursor":null}`)
	})
	api, err := client.API()
	if err != nil {
		t.Fatal(err)
	}
	check := func(response *http.Response, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 204 {
			t.Fatal(response.StatusCode)
		}
	}
	method, path = "GET", "/builtin-skills"
	check(api.ListBuiltinSkills(ctx, &generated.ListBuiltinSkillsParams{}))
	path = "/builtin-skills/platform-operations"
	check(api.GetBuiltinSkill(ctx, "platform-operations", &generated.GetBuiltinSkillParams{}))
	path += "/content"
	digest := strings.Repeat("a", 64)
	query = map[string][]string{"package_digest": {digest}}
	response, err := api.ReadBuiltinSkillArchive(ctx, "platform-operations", &generated.ReadBuiltinSkillArchiveParams{PackageDigest: &digest})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(data) != "PK\x00\xff" || response.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("archive: %q %v", data, err)
	}
	query = nil
	path = "/plugins"
	check(api.ListPlugins(ctx, &generated.ListPluginsParams{}))
	method, etag = "DELETE", `"provider:2"`
	path = "/connector-providers/provider"
	check(api.DeleteConnectorProvider(ctx, "provider", &generated.DeleteConnectorProviderParams{IfMatch: &etag}))
	path = "/memory-providers/provider"
	check(api.DeleteMemoryProvider(ctx, "provider", &generated.DeleteMemoryProviderParams{IfMatch: &etag}))
	path = "/model-providers/provider"
	check(api.DeleteModelProvider(ctx, "provider", &generated.DeleteModelProviderParams{IfMatch: &etag}))
	path = "/web-providers/provider"
	check(api.DeleteWebProvider(ctx, "provider", &generated.DeleteWebProviderParams{IfMatch: &etag}))
	method, etag, path = "POST", "", "/runs/run/apps/app%20%23%25/open"
	check(api.OpenRunApp(ctx, "run", "app #%", &generated.OpenRunAppParams{}))
	path, body = "/runs/run/apps/app%20%23%25/resources/read", `{"uri":"ui://chart"}`
	check(api.ReadRunAppResource(ctx, "run", "app #%", &generated.ReadRunAppResourceParams{}, generated.AppResourceRead{Uri: "ui://chart"}))
	path, body = "/runs/run/apps/app%20%23%25/tools/call", `{"name":"chart","arguments":{"nested":[null,false,1]}}`
	var call generated.AppToolCall
	if err := json.Unmarshal([]byte(body), &call); err != nil {
		t.Fatal(err)
	}
	check(api.CallRunAppTool(ctx, "run", "app #%", &generated.CallRunAppToolParams{}, call))
	method, path, body = "GET", "/sessions/session/threads", ""
	query = map[string][]string{"origin": {"new", "child"}, "thread_id": {"t1", "t2"}}
	response, err = api.ListSessionThreads(ctx, "session", &generated.ListSessionThreadsParams{Origin: Ptr([]generated.ListSessionThreadsParamsOrigin{"new", "child"}), ThreadId: Ptr([]string{"t1", "t2"})})
	page, err := ParseJSON[generated.ThreadNavigationPage](client, response, err, 200)
	if err != nil || !page.Value.NextCursor.IsNull() {
		t.Fatalf("navigation: %#v %v", page, err)
	}
	path = "/usage/members"
	query = map[string][]string{"principal_id": {"principal"}}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	check(api.ListUsageMembers(ctx, &generated.ListUsageMembersParams{Start: now, End: now.Add(time.Hour), PrincipalId: Ptr("principal")}))
	path = "/usage/runs"
	query = map[string][]string{"run_id": {"run"}, "cursor": {"next"}, "limit": {"1"}}
	response, err = api.ListRunUsage(ctx, &generated.ListRunUsageParams{RunId: Ptr("run"), Cursor: Ptr("next"), Limit: Ptr(1)})
	runs, err := ParseJSON[generated.RunUsagePage](client, response, err, 200)
	if err != nil || !runs.Value.NextCursor.IsNull() {
		t.Fatalf("usage: %#v %v", runs, err)
	}
}

func TestAppsAndContextNativeModelPresence(t *testing.T) {
	for _, test := range []struct {
		input string
		value any
	}{
		{`{"plugin_key":"p","display_name":"P","description":"","config_schema":null,"distribution_name":null,"distribution_version":null}`, &generated.PluginType{}},
		{`{"headers":{}}`, &generated.RequestContext{}},
		{`{"enabled":false,"blocking_threshold":1}`, &generated.BackgroundCompactionPolicy{}},
		{`{"result":{"nested":[null,false,0,{"empty":[]}]}}`, &generated.AppToolResult{}},
		{`{"uri":"ui://chart","mime_type":"text/html","html":"<p>chart</p>","metadata":{"x":null}}`, &generated.AppResource{}},
	} {
		if err := json.Unmarshal([]byte(test.input), test.value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(test.value)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal([]byte(test.input), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("model changed: %s -> %s", test.input, encoded)
		}
	}
}
