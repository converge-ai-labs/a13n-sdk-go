// This external consumer runs from an installed module ZIP outside the SDK source tree.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func required(name string) string {
	value := os.Getenv(name)
	check(value != "", name+" is required")
	return value
}
func key() string           { return fmt.Sprintf("go-sdk-%d", time.Now().UnixNano()) }
func ptr[T any](value T) *T { return &value }

// Imported history is the upstream Pydantic AI JSON shape, not an SDK-specific
// prompt transcript. Only the new Thread receives it; later Sends do not reseed.
func importedHistory() generated.MessageHistory {
	return generated.MessageHistory{
		{"kind": "request", "parts": []map[string]any{{"part_kind": "user-prompt", "content": "What changed?"}}},
		{"kind": "response", "parts": []map[string]any{{"part_kind": "text", "content": "The API changed."}}},
	}
}

func offline() {
	var submitted, continued, resumeCalls, streams int
	history := importedHistory()
	expectedHistory := must(json.Marshal(history))
	var resumeBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "consumer-request")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "not a Service JSON response")
		case r.URL.Path == "/api/v1/threads" && r.Method == "POST":
			submitted++
			check(r.Header.Get("Idempotency-Key") == "offline", "caller key")
			check(r.Header.Get("Authorization") == "Bearer offline", "auth")
			check(r.Header.Get("X-Workspace-ID") == "", "API-key scope implicit")
			var body map[string]json.RawMessage
			check(json.NewDecoder(r.Body).Decode(&body) == nil && string(body["message_history"]) == string(expectedHistory), "installed imported history wire")
			if submitted == 1 {
				w.WriteHeader(201)
			} else {
				w.WriteHeader(200)
			}
			_, _ = io.WriteString(w, `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr"},"run":null}`)
		case r.URL.Path == "/api/v1/threads/thr/inbox" && r.Method == "POST":
			continued++
			var body map[string]json.RawMessage
			check(json.NewDecoder(r.Body).Decode(&body) == nil, "decode continuation")
			_, reseeded := body["message_history"]
			check(!reseeded, "continuation must not re-import history")
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr"},"run":null}`)
		case r.URL.Path == "/api/v1/threads/thr" && r.Method == "GET":
			encoded := must(json.Marshal(map[string]any{"id": "thr", "message_history": history}))
			_, _ = w.Write(encoded)
		case r.URL.Path == "/api/v1/runs/wait/resume":
			resumeCalls++
			body := must(io.ReadAll(r.Body))
			check(r.Header.Get("Idempotency-Key") == "resume-offline" && string(body) == string(resumeBody), "installed atomic resume maps and input")
			if resumeCalls == 1 {
				w.WriteHeader(201)
			} else {
				w.WriteHeader(200)
			}
			_, _ = io.WriteString(w, `{"id":"successor","thread_id":"thr","status":"accepted"}`)
		case r.URL.Path == "/api/v1/threads/thr/inbox/ent":
			_, _ = io.WriteString(w, `{"id":"ent","thread_id":"thr","status":"consumed","assigned_run_id":"run"}`)
		case r.URL.Path == "/api/v1/runs/run":
			_, _ = io.WriteString(w, `{"id":"run","thread_id":"thr","status":"completed"}`)
		case r.URL.Path == "/api/v1/threads" && r.Method == "GET":
			w.Header().Set("ETag", `"1"`)
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
		case r.URL.EscapedPath() == "/api/v1/memories/mem/files/projects%2F%E8%AE%A1%E5%88%92%20%231%25.md":
			if r.Method == "PUT" {
				check(r.Header.Get("If-Match") == `"file:v1"`, "memory CAS")
				_, _ = io.WriteString(w, `{"content":"second","path":"projects/计划 #1%.md"}`)
			} else {
				_, _ = io.WriteString(w, `{"content":"first","path":"projects/计划 #1%.md"}`)
			}
		case r.URL.Path == "/api/v1/memories/mem/revisions":
			if r.URL.Query().Get("cursor") == "" {
				_, _ = io.WriteString(w, `{"items":[{"seq":7,"op":"create"}],"next_cursor":"next"}`)
			} else {
				check(r.URL.Query().Get("cursor") == "next", "memory cursor")
				_, _ = io.WriteString(w, `{"items":[{"seq":8,"op":"update"}],"next_cursor":null}`)
			}
		case r.URL.Path == "/api/v1/memories/mem/revisions/8/restore":
			check(r.Header.Get("If-Match") == `"file:v1"`, "numeric memory restore CAS")
			_, _ = io.WriteString(w, `{"path":"projects/计划 #1%.md","file":null}`)
		case strings.HasSuffix(r.URL.Path, "/stream"):
			streams++
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":{"code":"unexpected","message":"Result-only opened SSE"}}`)
		default:
			panic("unexpected consumer route: " + r.URL.Path)
		}
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
	defer client.Close()
	ctx := context.Background()
	interaction := must(client.Agent("agent").Start(ctx, "Hello", a13n.StartOptions{RequestKey: "offline", MessageHistory: &history}))
	defer interaction.Close()
	check(interaction.Run == nil && interaction.Receipt.StatusCode == 201, "queued receipt")
	outcome := must(interaction.Result(ctx))
	check(outcome.Run.ID == "run" && outcome.Status() == generated.RunStatusCompleted && outcome.Snapshot.RequestID() == "consumer-request", "exact Run and metadata")
	_, err := interaction.Next()
	check(errors.Is(err, io.EOF) && streams == 0 && submitted == 1, "finite result-only, no duplicate mutation")
	thread := must(interaction.Thread.Get(ctx))
	readback := must(json.Marshal(thread.Value.MessageHistory))
	check(string(readback) == string(expectedHistory), "installed imported history readback")
	api := must(client.API())
	replayResponse, err := api.CreateThreadApiV1ThreadsPost(ctx,
		&generated.CreateThreadApiV1ThreadsPostParams{IdempotencyKey: "offline"},
		generated.NewThread{AgentId: "agent", Payload: a13n.TextPayload("Hello"), MessageHistory: &history})
	replay := must(a13n.ParseJSON[generated.Submitted](client, replayResponse, err, 200))
	check(replay.Value.Entry.Id == interaction.Entry.ID && submitted == 2, "installed generated same-key Create replay")
	followup := must(client.Agent("agent").Send(ctx, interaction.Thread.ID, "Continue.", a13n.SendOptions{RequestKey: "followup-offline"}))
	defer followup.Close()
	check(must(followup.Result(ctx)).Run.ID == outcome.Run.ID && continued == 1, "no history reseed on continuation")
	var call generated.CallResult
	check(call.FromReturned(generated.Returned{Status: generated.ReturnedStatusReturned, Value: map[string]any{"answer": 42}}) == nil, "typed call result")
	resume := generated.Resume{Approvals: map[string]generated.ApprovalDecision{},
		Calls: map[string]generated.CallResult{"call-1": call},
		Input: nullable.NewNullableWithValue(a13n.TextPayload("Plus this user input."))}
	resumeBody = must(json.Marshal(resume))
	successor, accepted, err := client.Run("wait").Resume(ctx, resume, "resume-offline")
	check(err == nil && successor.ID == "successor" && accepted.StatusCode == 201, "installed atomic resume successor")
	replayedResponse, err := api.ResumeRunApiV1RunsRunIdResumePost(ctx, "wait",
		&generated.ResumeRunApiV1RunsRunIdResumePostParams{IdempotencyKey: "resume-offline"}, resume)
	replayed := must(a13n.ParseJSON[generated.RunView](client, replayedResponse, err, 200))
	check(replayed.Value.Id == successor.ID && resumeCalls == 2 && continued == 1, "installed raw resume replay no follow-up input send")
	pages := 0
	for page, err := range a13n.Pages(ctx, "", func(ctx context.Context, cursor *string) (a13n.Result[generated.ThreadPage], error) {
		response, err := api.ListThreadsApiV1ThreadsGet(ctx, &generated.ListThreadsApiV1ThreadsGetParams{Cursor: cursor})
		return a13n.ParseJSON[generated.ThreadPage](client, response, err, 200)
	}, func(page generated.ThreadPage) string { return page.NextCursor.GetOrEmpty() }) {
		check(err == nil && page.StatusCode == 200 && page.ETag() == `"1"`, "generated pagination/metadata")
		pages++
	}
	check(pages == 1, "pagination")
	path := "projects/计划 #1%.md"
	fileResponse, err := api.ReadFileApiV1MemoriesMemoryIdFilesPathGet(ctx, "mem", path,
		&generated.ReadFileApiV1MemoriesMemoryIdFilesPathGetParams{})
	file := must(a13n.ParseJSON[generated.MemoryFile](client, fileResponse, err, 200))
	check(file.Value.Content == "first", "installed generated Unicode file route")
	etag := `"file:v1"`
	fileResponse, err = api.ReplaceFileApiV1MemoriesMemoryIdFilesPathPut(ctx, "mem", path,
		&generated.ReplaceFileApiV1MemoriesMemoryIdFilesPathPutParams{IfMatch: &etag}, generated.MemoryFileReplace{Content: "second"})
	check(must(a13n.ParseJSON[generated.MemoryFile](client, fileResponse, err, 200)).Value.Content == "second", "installed memory CAS")
	seqs := []int{}
	for page, err := range a13n.Pages(ctx, "", func(ctx context.Context, cursor *string) (a13n.Result[generated.MemoryRevisionPage], error) {
		response, err := api.ListRevisionsApiV1MemoriesMemoryIdRevisionsGet(ctx, "mem",
			&generated.ListRevisionsApiV1MemoriesMemoryIdRevisionsGetParams{Path: &path, Cursor: cursor})
		return a13n.ParseJSON[generated.MemoryRevisionPage](client, response, err, 200)
	}, func(page generated.MemoryRevisionPage) string { return page.NextCursor.GetOrEmpty() }) {
		check(err == nil, "installed numeric revision pages")
		for _, item := range page.Value.Items {
			seqs = append(seqs, item.Seq)
		}
	}
	check(len(seqs) == 2 && seqs[0] == 7 && seqs[1] == 8, "installed numeric revisions")
	restoreResponse, err := api.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePost(ctx, "mem", seqs[1],
		&generated.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePostParams{IfMatch: &etag})
	restored := must(a13n.ParseJSON[generated.MemoryFileState](client, restoreResponse, err, 200))
	check(restored.Value.File.IsNull(), "installed nullable memory restore")
	var part generated.Part
	check(part.FromTextPart(generated.TextPart{Type: "text", Text: "typed"}) == nil, "typed union")
	body := must(json.Marshal(generated.AgentUpdate{Description: nullable.NewNullNullable[string]()}))
	check(string(body) == `{"description":null}`, "nullable omission")
	response, err := api.HealthHealthzGet(ctx)
	_, err = a13n.ParseJSON[map[string]any](client, response, err, 200)
	var protocol *a13n.ProtocolError
	check(errors.Is(err, a13n.ErrProtocol) && errors.As(err, &protocol) && protocol.Kind == "content_type", "bounded protocol errors")
	streamRecoveryOffline()
	fmt.Println("Installed module: interaction, exact Run, lazy pages, metadata, unions, null and diagnostics passed")
}

func live() {
	ca := must(os.ReadFile(required("A13N_CA_BUNDLE")))
	roots := x509.NewCertPool()
	check(roots.AppendCertsFromPEM(ca), "invalid fixture CA")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.Proxy = nil
	client := must(a13n.NewClient(required("A13N_SERVICE_URL"), a13n.NewSecret(required("A13N_API_TOKEN")), transport))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	api := must(client.API())
	response := must(api.HealthHealthzGet(ctx))
	check(response.StatusCode == 200, "health")
	_ = response.Body.Close()
	agent := required("A13N_AGENT")
	history := importedHistory()
	firstKey := key()
	firstText := "[slow] [long] Summarize this project."
	first := must(client.Agent(agent).Start(ctx, firstText,
		a13n.StartOptions{RequestKey: firstKey, MessageHistory: &history}))
	defer first.Close()
	frames := 0 // a Run can seal before SSE attachment; zero retained frames is valid.
	for {
		frame, err := first.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		check(err == nil, fmt.Sprintf("finite interaction stream: %v", err))
		if frame != nil {
			frames++
		}
	}
	outcome := must(first.Result(ctx))
	check(outcome.Status() == generated.RunStatusCompleted, "Agent start")
	items := must(outcome.Run.Items(ctx))
	check(items.Value.Complete && items.Value.Run.Id == outcome.Run.ID, "exact committed Items")
	readCtx, stopRead := context.WithTimeout(ctx, 20*time.Second)
	recoveryResponse := must(api.ThreadStreamApiV1ThreadsThreadIdStreamGet(readCtx, first.Thread.ID, recoveryParams(items.Value)))
	check(recoveryResponse.StatusCode == 200 && strings.HasPrefix(recoveryResponse.Header.Get("Content-Type"), "text/event-stream"), "snapshot coverage stream read")
	_ = recoveryResponse.Body.Close()
	stopRead()
	fmt.Println("Verified HTTPS: exact RunItems position/resume_after and paired recovery reader passed")
	thread := must(first.Thread.Get(ctx))
	check(len(thread.Value.MessageHistory) == len(history), "immutable imported history readback")
	replayResponse, err := api.CreateThreadApiV1ThreadsPost(ctx,
		&generated.CreateThreadApiV1ThreadsPostParams{IdempotencyKey: firstKey},
		generated.NewThread{AgentId: agent, Payload: a13n.TextPayload(firstText), MessageHistory: &history})
	replay := must(a13n.ParseJSON[generated.Submitted](client, replayResponse, err, 200))
	check(replay.Value.Thread.Id == first.Thread.ID && replay.Value.Entry.Id == first.Entry.ID,
		"same-key imported history replay returns original Entry")
	followUp := must(client.Agent(agent).Send(ctx, first.Thread.ID, "Explain the trade-off.", a13n.SendOptions{RequestKey: key()}))
	defer followUp.Close()
	check(must(followUp.Result(ctx)).Status() == generated.RunStatusCompleted, "explicit Agent continuation")
	fmt.Printf("Verified HTTPS: finite Agent iteration (%d provisional frames), exact Run/Items and continuation passed\n", frames)

	clientToolAgent := required("A13N_CLIENT_TOOL_AGENT")
	waiting := must(client.Agent(clientToolAgent).Start(ctx, "[client] Review local SDK scenario.", a13n.StartOptions{RequestKey: key()}))
	defer waiting.Close()
	waitingOutcome := must(waiting.Result(ctx))
	check(waitingOutcome.Status() == generated.RunStatusWaiting, "client-tool waiting")
	pending := must(waitingOutcome.Pending().Get())
	check(len(pending.Approvals) == 0 && len(pending.Calls) == 1, "pending client call count")
	delivery := generated.NextRun
	queuedPayload := a13n.TextPayload("Follow up after the client tool answer.")
	queuedKey := key()
	queued := must(client.Agent(clientToolAgent).SendPayload(ctx, waiting.Thread.ID, queuedPayload,
		a13n.SendOptions{RequestKey: queuedKey, Delivery: &delivery}))
	defer queued.Close()
	check(queued.Run == nil && queued.Receipt.StatusCode == 201, "next_run queued receipt")
	// Reconcile the same logical mutation through the complete generated API.
	// This explicit same-key replay must return the original Entry, not submit twice.
	replayResponse, err = api.SubmitMessageApiV1ThreadsThreadIdInboxPost(ctx, waiting.Thread.ID,
		&generated.SubmitMessageApiV1ThreadsThreadIdInboxPostParams{IdempotencyKey: queuedKey},
		generated.Message{AgentId: clientToolAgent, Payload: queuedPayload, Delivery: &delivery})
	replay = must(a13n.ParseJSON[generated.Submitted](client, replayResponse, err, 200))
	check(replay.StatusCode == 200 && replay.Value.Entry.Id == queued.Entry.ID && replay.Value.Thread.Id == waiting.Thread.ID, "same-key replay")
	var answer generated.CallResult
	check(answer.FromReturned(generated.Returned{Status: generated.ReturnedStatusReturned,
		Value: map[string]any{"decision": "reviewed"}}) == nil, "typed client-tool result")
	successorInput := a13n.TextPayload("Also consider the follow-up constraints.")
	resume := generated.Resume{Approvals: map[string]generated.ApprovalDecision{},
		Calls: map[string]generated.CallResult{pending.Calls[0].ToolCallId: answer},
		Input: nullable.NewNullableWithValue(successorInput)}
	successor, successorReceipt, err := waitingOutcome.Run.Resume(ctx, resume, key())
	check(err == nil && successor.ID != waitingOutcome.Run.ID && successorReceipt.Value.Id == successor.ID,
		"explicit distinct resume successor")
	check(must(successor.Wait(ctx)).Status() == generated.RunStatusCompleted, "resume successor completion")
	check(must(waitingOutcome.Run.Get(ctx)).Value.Status == generated.RunStatusWaiting, "waiting Run did not follow successor")
	queuedOutcome := must(queued.Result(ctx))
	entry := must(queued.Entry.Get(ctx))
	check(entry.Value.Status == generated.EntryStatusConsumed && entry.Value.AssignedRunId.GetOrEmpty() == queuedOutcome.Run.ID &&
		queuedOutcome.Run.ID != waitingOutcome.Run.ID && queuedOutcome.Status() == generated.RunStatusCompleted,
		"queued next_run bound to its exact consuming Run")
	fmt.Println("Verified HTTPS: waiting, queued next_run, same-key replay, atomic resume with input and consumed Entry passed")

	content := bytes.Repeat([]byte("asset\n"), 50000)
	upload := must(client.Upload(ctx, a13n.UploadFile{Name: "acceptance.bin", ContentType: "application/octet-stream", Reader: bytes.NewReader(content)}, key()))
	assetResponse, err := api.CreateAssetApiV1AssetsPost(ctx, &generated.CreateAssetApiV1AssetsPostParams{}, generated.AssetCreate{Name: "Acceptance", UploadId: upload.Value.UploadId})
	asset := must(a13n.ParseJSON[generated.Asset](client, assetResponse, err, 200, 201))
	download := must(client.Asset(asset.Value.Id).Download(ctx))
	received := must(io.ReadAll(download.Body))
	_ = download.Close()
	check(bytes.Equal(received, content), "streamed binary transfer")
	currentResponse, err := api.GetAgentApiV1AgentsAgentIdGet(ctx, agent, &generated.GetAgentApiV1AgentsAgentIdGetParams{})
	current := must(a13n.ParseJSON[generated.Agent](client, currentResponse, err, 200))
	changedResponse, err := api.UpdateAgentApiV1AgentsAgentIdPatch(ctx, agent, &generated.UpdateAgentApiV1AgentsAgentIdPatchParams{IfMatch: &[]string{current.ETag()}[0]}, generated.AgentUpdate{Description: nullable.NewNullableWithValue("Go acceptance")})
	changed := must(a13n.ParseJSON[generated.Agent](client, changedResponse, err, 200))
	staleResponse, err := api.UpdateAgentApiV1AgentsAgentIdPatch(ctx, agent, &generated.UpdateAgentApiV1AgentsAgentIdPatchParams{IfMatch: &[]string{current.ETag()}[0]}, generated.AgentUpdate{})
	_, err = a13n.ParseJSON[generated.Agent](client, staleResponse, err, 200)
	var failure *a13n.ApiError
	check(errors.As(err, &failure) && failure.Status == 412 && changed.ETag() != current.ETag(), "CAS conflict evidence")
	fmt.Println("Verified HTTPS: upload/download and generated CAS conflict passed")
	memory(ctx, client, api, agent)
}

// memory exercises the removed resource tree's nontrivial routes through the
// one generated transport: escaped Unicode paths, CAS, paged numeric revisions,
// nullable restore, and frozen Run mounts.
func memory(ctx context.Context, client *a13n.Client, api *generated.ClientWithResponses, agent string) {
	createdResponse, err := api.CreateMemoryApiV1MemoriesPost(ctx, &generated.CreateMemoryApiV1MemoriesPostParams{},
		generated.MemoryCreate{Name: "Go acceptance", Type: ptr("postgres")})
	created := must(a13n.ParseJSON[generated.Memory](client, createdResponse, err, 201))
	memoryID, path := created.Value.Id, "projects/计划 #1%.md"
	firstResponse, err := api.CreateFileApiV1MemoriesMemoryIdFilesPost(ctx, memoryID,
		&generated.CreateFileApiV1MemoriesMemoryIdFilesPostParams{}, generated.MemoryFileCreate{Path: path, Content: "first"})
	first := must(a13n.ParseJSON[generated.MemoryFile](client, firstResponse, err, 201))
	check(first.Value.Path == path, "Unicode file creation")
	read := func() a13n.Result[generated.MemoryFile] {
		response, err := api.ReadFileApiV1MemoriesMemoryIdFilesPathGet(ctx, memoryID, path,
			&generated.ReadFileApiV1MemoriesMemoryIdFilesPathGetParams{})
		return must(a13n.ParseJSON[generated.MemoryFile](client, response, err, 200))
	}
	check(read().Value.Content == "first", "Unicode file read")
	etag := first.ETag()
	replacedResponse, err := api.ReplaceFileApiV1MemoriesMemoryIdFilesPathPut(ctx, memoryID, path,
		&generated.ReplaceFileApiV1MemoriesMemoryIdFilesPathPutParams{IfMatch: &etag},
		generated.MemoryFileReplace{Content: "second"})
	_ = must(a13n.ParseJSON[generated.MemoryFile](client, replacedResponse, err, 200))
	limit := 1
	revisions := []generated.MemoryRevision{}
	for page, err := range a13n.Pages(ctx, "", func(ctx context.Context, cursor *string) (a13n.Result[generated.MemoryRevisionPage], error) {
		response, err := api.ListRevisionsApiV1MemoriesMemoryIdRevisionsGet(ctx, memoryID,
			&generated.ListRevisionsApiV1MemoriesMemoryIdRevisionsGetParams{Path: &path, Limit: &limit, Cursor: cursor})
		return a13n.ParseJSON[generated.MemoryRevisionPage](client, response, err, 200)
	}, func(page generated.MemoryRevisionPage) string { return page.NextCursor.GetOrEmpty() }) {
		check(err == nil, fmt.Sprintf("memory revision pagination: %v", err))
		revisions = append(revisions, page.Value.Items...)
	}
	check(len(revisions) == 2, "two revisions of Unicode file")
	var original, updated int
	for _, revision := range revisions {
		if revision.Op == "create" {
			original = revision.Seq
		}
		if revision.Op == "update" {
			updated = revision.Seq
		}
	}
	check(original > 0 && updated > original, "numeric revision ordering")
	etag = read().ETag()
	deletedResponse, err := api.DeleteFileApiV1MemoriesMemoryIdFilesPathDelete(ctx, memoryID, path,
		&generated.DeleteFileApiV1MemoriesMemoryIdFilesPathDeleteParams{IfMatch: &etag})
	_ = must(a13n.ParseJSON[struct{}](client, deletedResponse, err, 204))
	restoredResponse, err := api.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePost(ctx, memoryID, updated,
		&generated.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePostParams{})
	restored := must(a13n.ParseJSON[generated.MemoryFileState](client, restoredResponse, err, 200))
	check(must(restored.Value.File.Get()).Content == "first", "numeric revision restore")
	etag = read().ETag()
	undoneResponse, err := api.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePost(ctx, memoryID, original,
		&generated.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePostParams{IfMatch: &etag})
	undone := must(a13n.ParseJSON[generated.MemoryFileState](client, undoneResponse, err, 200))
	check(undone.Value.File.IsNull(), "restore creation yields null file")
	restoredResponse, err = api.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePost(ctx, memoryID, updated,
		&generated.RestoreRevisionApiV1MemoriesMemoryIdRevisionsSeqRestorePostParams{})
	_ = must(a13n.ParseJSON[generated.MemoryFileState](client, restoredResponse, err, 200))

	mounts := []generated.MemoryMount{{MemoryId: memoryID, Name: "notes", Access: generated.MemoryAccessRead}}
	interaction := must(client.Agent(agent).Start(ctx, "Memory acceptance.", a13n.StartOptions{RequestKey: key(), Memories: &mounts}))
	defer interaction.Close()
	outcome := must(interaction.Result(ctx))
	check(outcome.Status() == generated.RunStatusCompleted, "memory-mounted Run")
	thread := must(interaction.Thread.Get(ctx))
	etag = thread.ETag()
	mountResponse, err := api.UpdateMountApiV1ThreadsThreadIdMemoriesNamePatch(ctx, interaction.Thread.ID, "notes",
		&generated.UpdateMountApiV1ThreadsThreadIdMemoriesNamePatchParams{IfMatch: &etag},
		generated.MemoryMountUpdate{Access: nullable.NewNullableWithValue(generated.MemoryAccessWrite)})
	_ = must(a13n.ParseJSON[generated.MemoryMount](client, mountResponse, err, 200))
	check(must(outcome.Run.Get(ctx)).Value.MemoryMounts[0].Access == generated.MemoryAccessRead, "Run mount frozen")
	fmt.Println("Verified HTTPS: Unicode memory file, CAS, paged numeric history, nullable restore and frozen mounts passed")
}

// Only the saved display position claims coverage. A Redis ID is optional.
func recoveryParams(snapshot generated.RunItems) *generated.ThreadStreamApiV1ThreadsThreadIdStreamGetParams {
	runID, position := snapshot.Run.Id, snapshot.Position.GetOrEmpty()
	check(position != "", "a committed display position is required before claiming coverage")
	params := &generated.ThreadStreamApiV1ThreadsThreadIdStreamGetParams{Run: &runID, Position: &position}
	if after := snapshot.ResumeAfter.GetOrEmpty(); after != "" {
		params.LastEventID = &after
	}
	return params
}

func streamRecoveryOffline() {
	for _, hint := range []string{"null", `"999-1"`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Request-ID", "recovery")
			if strings.HasSuffix(req.URL.Path, "/items") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"run":{"id":"recovered","thread_id":"recovery-thread","status":"running"},"items":[],"position":"2-8","resume_after":%s,"dropped":4,"complete":false}`, hint)
				return
			}
			check(req.URL.Query().Get("run") == "recovered" && req.URL.Query().Get("position") == "2-8", "installed run/position claims")
			expected := ""
			if hint != "null" {
				expected = "999-1"
			}
			check(req.Header.Get("Last-Event-ID") == expected, "installed nullable/expired hint")
			w.Header().Set("Content-Type", "text/event-stream")
			// A covered boundary remains observable even with no retained hint.
			_, _ = io.WriteString(w, "id: 1000-1\nevent: boundary\ndata: {\"run_id\":\"recovered\",\"attempt\":2,\"sequence\":8}\n\n")
		}))
		client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
		snapshot := must(client.Run("recovered").Items(context.Background()))
		check(snapshot.Value.Dropped == 4 && !snapshot.Value.Complete && snapshot.RequestID() == "recovery", "installed full committed display/metadata")
		if hint == "null" {
			check(snapshot.Value.ResumeAfter.IsNull(), "installed null recovery hint")
		}
		api := must(client.API())
		response := must(api.ThreadStreamApiV1ThreadsThreadIdStreamGet(context.Background(), "recovery-thread", recoveryParams(snapshot.Value)))
		body := must(io.ReadAll(response.Body))
		_ = response.Body.Close()
		check(strings.Contains(string(body), "event: boundary") && !strings.Contains(string(body), "event: gap"), "covered boundary and expired hint")
		_ = client.Close()
		server.Close()
	}
	for _, field := range []string{`null`, `"2-10"`} {
		var gap a13n.GapFrame
		check(json.Unmarshal([]byte(`{"run_id":"recovered","position":`+field+`}`), &gap) == nil, "installed gap decode")
		if field == "null" {
			check(gap.Position == nil, "unknown gap")
		} else {
			check(gap.Position != nil && *gap.Position == "2-10", "known gap")
		}
	}
	settings := map[string]generated.JsonValue{"nested": map[string]any{"enabled": true, "value": nil}}
	config := generated.ModelConfigInput{ModelApi: "other.native", ModelName: "native", Settings: &settings}
	check(strings.Contains(string(must(json.Marshal(config))), `"settings":{"nested":{"enabled":true,"value":null}}`), "installed native backend settings")
	fmt.Println("Installed module: display coverage, nullable/expired hints, run/position wire and gap positions passed")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--offline" {
		offline()
	} else {
		live()
	}
}
