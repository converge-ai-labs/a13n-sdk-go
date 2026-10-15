package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"time"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func nativeJSON[T any](client *a13n.Client, status int, call func() (*http.Response, error)) a13n.Result[T] {
	response, err := call()
	result := must(a13n.ParseJSON[T](client, response, err, status))
	check(result.RequestID() == "memory-computer" && result.ETag() == `"resource:2"`, "new native operation metadata")
	return result
}

// All new operations run against a loopback fixture from the installed module,
// with both key and session auth. Computer/provider backends remain fixture data.
func memoryComputerOffline() {
	for _, session := range []bool{false, true} {
		seen := map[string]int{}
		image := []byte{0x89, 'P', 'N', 'G', 0, 0xff, 0x80}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := r.Method + " " + r.URL.Path
			seen[route]++
			if session {
				check(r.Header.Get("Authorization") == "" && r.Header.Get("X-Workspace-ID") == "workspace", "new operation explicit session scope")
				if r.Method != "GET" {
					check(r.Header.Get("X-CSRF-Token") == "csrf", "new operation session CSRF")
				}
			} else {
				check(r.Header.Get("Authorization") == "Bearer "+"offline" && r.Header.Get("X-Workspace-ID") == "", "new operation implicit key scope")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "memory-computer")
			w.Header().Set("ETag", `"resource:2"`)
			if r.Method == "DELETE" || r.Method == "PATCH" {
				check(r.Header.Get("If-Match") == `"resource:1"`, "new operation CAS")
			}
			if r.Method == "GET" || r.Method == "DELETE" || strings.HasSuffix(route, "/computer/check") || strings.HasSuffix(route, "/organizations") {
				check(len(must(io.ReadAll(r.Body))) == 0, "bodyless native operation")
			}
			switch route {
			case "DELETE /api/v1/environment-providers/provider", "DELETE /api/v1/memory-spaces/space":
				w.WriteHeader(204)
			case "GET /api/v1/environment-providers/provider/references":
				_, _ = io.WriteString(w, `{"environment_count":0,"environments":[],"template_count":0,"templates":[]}`)
			case "GET /api/v1/memory-spaces":
				check(reflect.DeepEqual(r.URL.Query()["label"], []string{"team=one", "kind=shared"}) && r.URL.Query().Get("enabled") == "false" && r.URL.Query().Get("cursor") == "opaque+/=", "Space filters")
				_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
			case "POST /api/v1/memory-spaces":
				check(string(must(io.ReadAll(r.Body))) == `{"always_load":[],"enabled":false,"name":"shared"}`, "Space create preserves empty/false")
				check(r.Header.Get("Idempotency-Key") == "", "Space create has no invented idempotency")
				w.WriteHeader(201)
				_, _ = io.WriteString(w, `{"id":"space","name":"shared","enabled":false,"memory_id":"memory","always_load":[],"labels":{},"organization":{}}`)
			case "GET /api/v1/memory-spaces/space", "PATCH /api/v1/memory-spaces/space":
				if r.Method == "PATCH" {
					check(string(must(io.ReadAll(r.Body))) == `{"description":null,"enabled":false,"organization":{"cooldown_seconds":0,"enabled":false}}`, "Space update preserves null/zero/false")
				}
				_, _ = io.WriteString(w, `{"id":"space","name":"shared","enabled":false,"memory_id":"memory","always_load":[],"labels":{},"organization":{}}`)
			case "GET /api/v1/memory-spaces/space/organization", "POST /api/v1/memory-spaces/space/organizations":
				if r.Method == "POST" {
					check(r.Header.Get("Idempotency-Key") == "", "organization request has no invented idempotency")
					w.WriteHeader(202)
				}
				_, _ = io.WriteString(w, `{"session_id":null,"pending":false,"history_gap":false,"processed_seq":0,"active_run_id":null}`)
			case "GET /api/v1/memory-spaces/space/organizations", "GET /api/v1/memory-spaces/space/sessions":
				check(r.URL.Query().Get("limit") == "2" && r.URL.Query().Get("cursor") == "opaque+/=", "Space history paging")
				_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
			case "GET /api/v1/runs":
				q := r.URL.Query()
				check(q.Get("agent_id") == "agent" && q.Get("session_id") == "session" && q.Get("thread_id") == "thread" && q.Get("principal_id") == "principal" && q.Get("status") == "completed" && q.Get("limit") == "2" && q.Get("cursor") == "opaque+/=", "Run filter conjunction")
				check(q.Get("created_after") == "2026-10-14T00:00:00+08:00" && q.Get("created_before") == "2026-10-15T00:00:00Z", "aware Run time filters")
				_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
			case "GET /api/v1/runs/run/agent":
				_, _ = io.WriteString(w, `{"revision":{"id":"revision"},"config":{"builtin_skills":[],"platform_tools":{"groups":[],"read_permission":"deny","write_permission":"ask"},"memory":{"enabled":false,"spaces":[]}}}`)
			case "GET /api/v1/runs/run/attempts/attempt":
				_, _ = io.WriteString(w, `{"id":"attempt","run_id":"run","sequence":1}`)
			case "GET /api/v1/runs/run/images/image":
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				_, _ = w.Write(image)
			case "GET /api/v1/usage/records/record/metrics":
				_, _ = io.WriteString(w, `{"time_to_first_chunk_seconds":null,"request_duration_seconds":0.25,"output_tokens_per_second":0}`)
			case "PATCH /api/v1/threads/thread/environments/desktop":
				check(string(must(io.ReadAll(r.Body))) == `{"permission_ceiling":{"operations":[]}}`, "mount update exact empty permission ceiling")
				_, _ = io.WriteString(w, `{"name":"desktop","environment_id":"environment","working_directory":null,"permission_ceiling":{"operations":[]}}`)
			case "POST /api/v1/environments/environment/computer/check":
				_, _ = io.WriteString(w, `{"status":"unsupported","operations":[],"observe_ready":false,"input_ready":false,"display_count":0}`)
			default:
				panic("unexpected new operation " + route)
			}
		}))
		var options []a13n.ClientOption
		var scope *string
		secret := a13n.NewSecret("offline")
		if session {
			jar := must(cookiejar.New(nil))
			options = append(options, a13n.WithSession(jar, func() string { return "csrf" }), a13n.WithSessionWorkspace("semantic-only"))
			scope = ptr("workspace")
			secret = a13n.NewSecret("")
		}
		client := must(a13n.NewClient(server.URL, secret, nil, options...))
		api := must(client.API())
		ctx := context.Background()
		cas, cursor, limit := `"resource:1"`, "opaque+/=", 2
		after := must(time.Parse(time.RFC3339, "2026-10-14T00:00:00+08:00"))
		before := must(time.Parse(time.RFC3339, "2026-10-15T00:00:00Z"))
		calls := []func() (*http.Response, error){
			func() (*http.Response, error) {
				return api.DeleteEnvironmentProviderApiV1EnvironmentProvidersProviderIdDelete(ctx, "provider", &generated.DeleteEnvironmentProviderApiV1EnvironmentProvidersProviderIdDeleteParams{XWorkspaceID: scope, IfMatch: &cas})
			},
			func() (*http.Response, error) {
				return api.DeleteSpaceApiV1MemorySpacesSpaceIdDelete(ctx, "space", &generated.DeleteSpaceApiV1MemorySpacesSpaceIdDeleteParams{XWorkspaceID: scope, IfMatch: &cas})
			},
			func() (*http.Response, error) {
				return api.EnvironmentProviderReferencesApiV1EnvironmentProvidersProviderIdReferencesGet(ctx, "provider", &generated.EnvironmentProviderReferencesApiV1EnvironmentProvidersProviderIdReferencesGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.ListSpacesApiV1MemorySpacesGet(ctx, &generated.ListSpacesApiV1MemorySpacesGetParams{XWorkspaceID: scope, Label: ptr([]string{"team=one", "kind=shared"}), Enabled: ptr(false), Limit: &limit, Cursor: &cursor})
			},
			func() (*http.Response, error) {
				return api.GetSpaceApiV1MemorySpacesSpaceIdGet(ctx, "space", &generated.GetSpaceApiV1MemorySpacesSpaceIdGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.GetSpaceOrganizationApiV1MemorySpacesSpaceIdOrganizationGet(ctx, "space", &generated.GetSpaceOrganizationApiV1MemorySpacesSpaceIdOrganizationGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.ListSpaceOrganizationsApiV1MemorySpacesSpaceIdOrganizationsGet(ctx, "space", &generated.ListSpaceOrganizationsApiV1MemorySpacesSpaceIdOrganizationsGetParams{XWorkspaceID: scope, Limit: &limit, Cursor: &cursor})
			},
			func() (*http.Response, error) {
				return api.ListSpaceSessionsApiV1MemorySpacesSpaceIdSessionsGet(ctx, "space", &generated.ListSpaceSessionsApiV1MemorySpacesSpaceIdSessionsGetParams{XWorkspaceID: scope, Limit: &limit, Cursor: &cursor})
			},
			func() (*http.Response, error) {
				return api.ListRunsApiV1RunsGet(ctx, &generated.ListRunsApiV1RunsGetParams{XWorkspaceID: scope, AgentId: ptr("agent"), SessionId: ptr("session"), ThreadId: ptr("thread"), PrincipalId: ptr("principal"), Status: ptr(generated.ListRunsApiV1RunsGetParamsStatus("completed")), CreatedAfter: &after, CreatedBefore: &before, Limit: &limit, Cursor: &cursor})
			},
			func() (*http.Response, error) {
				return api.RunAgentApiV1RunsRunIdAgentGet(ctx, "run", &generated.RunAgentApiV1RunsRunIdAgentGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.GetAttemptApiV1RunsRunIdAttemptsAttemptIdGet(ctx, "run", "attempt", &generated.GetAttemptApiV1RunsRunIdAttemptsAttemptIdGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.RunImageApiV1RunsRunIdImagesImageIdGet(ctx, "run", "image", &generated.RunImageApiV1RunsRunIdImagesImageIdGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.ModelRequestMetricsApiV1UsageRecordsRecordIdMetricsGet(ctx, "record", &generated.ModelRequestMetricsApiV1UsageRecordsRecordIdMetricsGetParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.UpdateSpaceApiV1MemorySpacesSpaceIdPatch(ctx, "space", &generated.UpdateSpaceApiV1MemorySpacesSpaceIdPatchParams{XWorkspaceID: scope, IfMatch: &cas}, generated.MemorySpaceUpdate{Description: a13n.Null[string](), Enabled: a13n.Value(false), Organization: a13n.Value(generated.OrganizationPolicy{Enabled: ptr(false), CooldownSeconds: ptr(0)})})
			},
			func() (*http.Response, error) {
				return api.UpdateMountApiV1ThreadsThreadIdEnvironmentsNamePatch(ctx, "thread", "desktop", &generated.UpdateMountApiV1ThreadsThreadIdEnvironmentsNamePatchParams{XWorkspaceID: scope, IfMatch: &cas}, generated.MountUpdate{PermissionCeiling: generated.EnvironmentPermissionSetInput{Operations: ptr([]generated.EnvironmentAction{})}})
			},
			func() (*http.Response, error) {
				return api.CheckComputerApiV1EnvironmentsEnvironmentIdComputerCheckPost(ctx, "environment", &generated.CheckComputerApiV1EnvironmentsEnvironmentIdComputerCheckPostParams{XWorkspaceID: scope})
			},
			func() (*http.Response, error) {
				return api.CreateSpaceApiV1MemorySpacesPost(ctx, &generated.CreateSpaceApiV1MemorySpacesPostParams{XWorkspaceID: scope}, generated.MemorySpaceCreate{Name: "shared", Enabled: ptr(false), AlwaysLoad: ptr([]string{})})
			},
			func() (*http.Response, error) {
				return api.OrganizeSpaceApiV1MemorySpacesSpaceIdOrganizationsPost(ctx, "space", &generated.OrganizeSpaceApiV1MemorySpacesSpaceIdOrganizationsPostParams{XWorkspaceID: scope})
			},
		}
		for _, call := range calls[:2] {
			response, err := call()
			check(err == nil && response.StatusCode == 204 && response.Header.Get("X-Request-ID") == "memory-computer", "native empty deletion")
			check(len(must(io.ReadAll(response.Body))) == 0, "204 empty body")
			mustClose(response.Body)
		}
		check(nativeJSON[generated.EnvironmentProviderReferences](client, 200, calls[2]).Value.EnvironmentCount == 0, "provider references")
		check(nativeJSON[generated.MemorySpacePage](client, 200, calls[3]).Value.NextCursor.IsNull(), "Space page")
		check(nativeJSON[generated.MemorySpace](client, 200, calls[4]).Value.MemoryId == "memory", "Space managed memory")
		check(nativeJSON[generated.SpaceOrganizationStatus](client, 200, calls[5]).Value.SessionId.IsNull(), "organization null session")
		_ = nativeJSON[generated.RunPage](client, 200, calls[6])
		_ = nativeJSON[generated.SessionPage](client, 200, calls[7])
		_ = nativeJSON[generated.RunPage](client, 200, calls[8])
		agent := nativeJSON[generated.RunAgent](client, 200, calls[9]).Value
		check(agent.Revision.Id == "revision" && agent.Config.Memory != nil && agent.Config.Memory.Enabled != nil && !*agent.Config.Memory.Enabled && agent.Config.PlatformTools != nil, "executed Agent native configuration")
		check(nativeJSON[generated.AttemptView](client, 200, calls[10]).Value.Id == "attempt", "attempt detail")
		response, err := calls[11]()
		check(err == nil && response.StatusCode == 200 && response.Header.Get("Content-Type") == "image/png" && response.Header.Get("Cache-Control") == "no-store" && response.Header.Get("X-Content-Type-Options") == "nosniff", "retained image raw metadata")
		check(bytes.Equal(must(io.ReadAll(response.Body)), image), "retained image exact bytes")
		mustClose(response.Body)
		metrics := nativeJSON[generated.ModelRequestMetrics](client, 200, calls[12]).Value
		check(metrics.TimeToFirstChunkSeconds.IsNull() && metrics.OutputTokensPerSecond.GetOrEmpty() == 0 && !metrics.OutputTokensPerSecond.IsNull(), "metric null versus zero")
		_ = nativeJSON[generated.MemorySpace](client, 200, calls[13])
		check(nativeJSON[generated.MountView](client, 200, calls[14]).Value.PermissionCeiling != nil, "mount permission ceiling readback")
		check(nativeJSON[generated.ComputerCheck](client, 200, calls[15]).Value.Status == generated.ComputerCheckStatus("unsupported"), "computer unsupported probe")
		_ = nativeJSON[generated.MemorySpace](client, 201, calls[16])
		check(!*nativeJSON[generated.SpaceOrganizationStatus](client, 202, calls[17]).Value.Pending, "organization asynchronous status")
		check(len(seen) == 18, "all 18 new operations dispatched")
		mustClose(client)
		for _, call := range calls {
			_, err := call()
			check(errors.Is(err, a13n.ErrClosed), "new native operation lifetime")
		}
		server.Close()
	}
	fmt.Println("Installed module: all 18 new operations, key/session scope, CSRF/CAS, filters, metrics and binary images passed (local mock)")
}

func mustClose(closer io.Closer) { check(closer.Close() == nil, "close owned resource") }

// Matching Service reads are explicit and use the Run completed by the ordinary
// live journey. No desktop/provider infrastructure is provisioned by the caller.
func memoryComputerReadback(ctx context.Context, client *a13n.Client, outcome a13n.RunOutcome) {
	api := must(client.API())
	raw, err := api.RunAgentApiV1RunsRunIdAgentGet(ctx, outcome.RunID, &generated.RunAgentApiV1RunsRunIdAgentGetParams{})
	agent := must(a13n.ParseJSON[generated.RunAgent](client, raw, err, 200))
	check(agent.Value.Revision.Id != "", "matching Service executed Agent revision")
	raw, err = api.ListRunsApiV1RunsGet(ctx, &generated.ListRunsApiV1RunsGetParams{ThreadId: &outcome.ThreadID, Limit: ptr(100)})
	runs := must(a13n.ParseJSON[generated.RunPage](client, raw, err, 200))
	found := false
	for _, run := range runs.Value.Items {
		if run.Id == outcome.RunID {
			found = true
		}
	}
	check(found, "matching Service workspace Run list")
	raw, err = api.RunAttemptsApiV1RunsRunIdAttemptsGet(ctx, outcome.RunID, &generated.RunAttemptsApiV1RunsRunIdAttemptsGetParams{})
	attempts := must(a13n.ParseJSON[generated.Attempts](client, raw, err, 200))
	for _, attempt := range attempts.Value.Items {
		raw, err = api.GetAttemptApiV1RunsRunIdAttemptsAttemptIdGet(ctx, outcome.RunID, attempt.Id, &generated.GetAttemptApiV1RunsRunIdAttemptsAttemptIdGetParams{})
		check(must(a13n.ParseJSON[generated.AttemptView](client, raw, err, 200)).Value.Id == attempt.Id, "matching Service attempt detail")
	}
	raw, err = api.CreateSpaceApiV1MemorySpacesPost(ctx, &generated.CreateSpaceApiV1MemorySpacesPostParams{}, generated.MemorySpaceCreate{Name: "go-acceptance-" + outcome.RunID, Enabled: ptr(false), AlwaysLoad: ptr([]string{})})
	space := must(a13n.ParseJSON[generated.MemorySpace](client, raw, err, 201))
	check(!space.Value.Enabled && len(space.Value.AlwaysLoad) == 0 && space.Value.MemoryId != "", "matching Service Space create")
	raw, err = api.UpdateSpaceApiV1MemorySpacesSpaceIdPatch(ctx, space.Value.Id, &generated.UpdateSpaceApiV1MemorySpacesSpaceIdPatchParams{IfMatch: ptr(space.ETag())}, generated.MemorySpaceUpdate{Description: a13n.Value("installed Go caller"), Organization: a13n.Value(generated.OrganizationPolicy{Enabled: ptr(false), CooldownSeconds: ptr(0)})})
	space = must(a13n.ParseJSON[generated.MemorySpace](client, raw, err, 200))
	check(space.Value.Organization.CooldownSeconds != nil && *space.Value.Organization.CooldownSeconds == 0, "matching Service organization zero cooldown")
	raw, err = api.GetSpaceOrganizationApiV1MemorySpacesSpaceIdOrganizationGet(ctx, space.Value.Id, &generated.GetSpaceOrganizationApiV1MemorySpacesSpaceIdOrganizationGetParams{})
	_ = must(a13n.ParseJSON[generated.SpaceOrganizationStatus](client, raw, err, 200))
	raw, err = api.ListSpaceOrganizationsApiV1MemorySpacesSpaceIdOrganizationsGet(ctx, space.Value.Id, &generated.ListSpaceOrganizationsApiV1MemorySpacesSpaceIdOrganizationsGetParams{})
	_ = must(a13n.ParseJSON[generated.RunPage](client, raw, err, 200))
	raw, err = api.ListSpaceSessionsApiV1MemorySpacesSpaceIdSessionsGet(ctx, space.Value.Id, &generated.ListSpaceSessionsApiV1MemorySpacesSpaceIdSessionsGetParams{})
	_ = must(a13n.ParseJSON[generated.SessionPage](client, raw, err, 200))
	raw, err = api.DeleteSpaceApiV1MemorySpacesSpaceIdDelete(ctx, space.Value.Id, &generated.DeleteSpaceApiV1MemorySpacesSpaceIdDeleteParams{IfMatch: ptr(space.ETag())})
	check(err == nil && raw.StatusCode == 204, "matching Service Space delete")
	mustClose(raw.Body)
	if recordID := os.Getenv("A13N_MODEL_RECORD"); recordID != "" {
		raw, err = api.ModelRequestMetricsApiV1UsageRecordsRecordIdMetricsGet(ctx, recordID, &generated.ModelRequestMetricsApiV1UsageRecordsRecordIdMetricsGetParams{})
		metrics := must(a13n.ParseJSON[generated.ModelRequestMetrics](client, raw, err, 200))
		check(metrics.StatusCode == 200, "matching Service model request metrics")
		fmt.Println("Matching Service: supplied model-request record metrics read passed")
	} else {
		fmt.Println("Matching Service: no A13N_MODEL_RECORD supplied; model-request metrics covered offline")
	}
	if model := os.Getenv("A13N_ORGANIZER_MODEL"); model != "" {
		raw, err = api.CreateSpaceApiV1MemorySpacesPost(ctx, &generated.CreateSpaceApiV1MemorySpacesPostParams{}, generated.MemorySpaceCreate{Name: "go-organization-" + outcome.RunID, Organization: &generated.OrganizationPolicy{Enabled: ptr(false), Model: a13n.Value(model), CooldownSeconds: ptr(0)}})
		organizationSpace := must(a13n.ParseJSON[generated.MemorySpace](client, raw, err, 201))
		raw, err = api.OrganizeSpaceApiV1MemorySpacesSpaceIdOrganizationsPost(ctx, organizationSpace.Value.Id, &generated.OrganizeSpaceApiV1MemorySpacesSpaceIdOrganizationsPostParams{})
		_ = must(a13n.ParseJSON[generated.SpaceOrganizationStatus](client, raw, err, 202))
		fmt.Println("Matching Service: manual organization request accepted (202); asynchronous execution/cleanup remains fixture-owned")
	} else {
		fmt.Println("Matching Service: no A13N_ORGANIZER_MODEL supplied; organization request covered offline")
	}
	fmt.Println("Matching Service: Run list, executed Agent, attempts and disabled Space CRUD/status/history passed; desktop screenshots and automatic organization remain offline fixtures")
}
