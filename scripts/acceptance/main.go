// This consumer runs from a module ZIP outside the SDK source tree.
package main

import (
	"bufio"
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

// Cut one real stream after its first cursor-bearing event, without buffering
// the entire response. The reconnect must carry that applied cursor.
type disconnectTransport struct {
	inner   *http.Transport
	cursors []string
	cuts    int
}

func (t *disconnectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.inner.RoundTrip(req)
	if err == nil && strings.HasSuffix(req.URL.Path, "/stream") {
		t.cursors = append(t.cursors, req.Header.Get("Last-Event-ID"))
		if len(t.cursors) == 1 {
			response.Body = &cutBody{ReadCloser: response.Body, reader: bufio.NewReader(response.Body), owner: t}
		}
	}
	return response, err
}
func (t *disconnectTransport) CloseIdleConnections() { t.inner.CloseIdleConnections() }

type cutBody struct {
	io.ReadCloser
	reader       *bufio.Reader
	pending      []byte
	cursor, stop bool
	owner        *disconnectTransport
}

func (b *cutBody) Read(p []byte) (int, error) {
	if len(b.pending) == 0 {
		if b.stop {
			b.owner.cuts++
			_ = b.Close()
			return 0, io.ErrUnexpectedEOF
		}
		line, err := b.reader.ReadBytes('\n')
		if err != nil {
			return 0, err
		}
		if bytes.HasPrefix(line, []byte("id:")) {
			b.cursor = true
		}
		if b.cursor && len(bytes.TrimSpace(line)) == 0 {
			b.stop = true
		}
		b.pending = line
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func offline() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("X-Request-ID", "consumer-request")
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "not a Service JSON response")
			return
		}
		check(r.URL.Path == "/api/v1/workspaces/example/threads", "consumer route")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "\"1\"")
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.Secret{}, nil))
	defer client.Close()
	pages := 0
	for page, err := range client.Resources().Workspaces().Ref("example").Threads().Pages(context.Background(), a13n.ThreadsListOptions{}) {
		check(err == nil && page.StatusCode == 200 && page.ETag() == "\"1\"", "consumer result metadata")
		pages++
	}
	check(pages == 1, "consumer pagination")
	var part generated.Part
	check(part.FromTextPart(generated.TextPart{Type: "text", Text: "typed"}) == nil, "consumer union")
	body := must(json.Marshal(generated.AgentUpdate{Description: nullable.NewNullNullable[string]()}))
	check(string(body) == `{"description":null}`, "consumer nullable omission")
	_, err := client.Resources().Healthz().Get(context.Background())
	var protocol *a13n.ProtocolError
	check(errors.Is(err, a13n.ErrProtocol) && errors.As(err, &protocol) && protocol.Kind == "content_type" && protocol.RequestID == "consumer-request", "consumer diagnostic evidence")
	member, source := a13n.MemberKindServiceAccount, a13n.SkillSourceGithub
	_ = a13n.OrganizationMembersListOptions{Kind: &member}
	_ = a13n.SkillsListOptions{Source: &source}
	_ = client.Resources().ProviderTypes().Ref(a13n.ProviderKindMemory)
	fmt.Println("Installed module: typed resources, pagination, metadata, union, nullable, domain enums and diagnostics passed")
}

func wait(ctx context.Context, run *a13n.RunResource, expected generated.RunStatus) a13n.Result[generated.RunView] {
	check(run != nil, "missing accepted Run")
	result := must(run.Wait(ctx, 100*time.Millisecond))
	check(result.Value.Status == expected, fmt.Sprintf("Run status %s, expected %s", result.Value.Status, expected))
	items := must(run.Items().Get(ctx))
	check(items.Value.Complete && items.Value.Run.Id == result.Value.Id, "Run readback")
	return result
}

func live() {
	ca := must(os.ReadFile(required("A13N_CA_BUNDLE")))
	roots := x509.NewCertPool()
	check(roots.AppendCertsFromPEM(ca), "invalid fixture CA")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.Proxy = nil
	wrapped := &disconnectTransport{inner: transport}
	client := must(a13n.NewClient(required("A13N_SERVICE_URL"), a13n.NewSecret(required("A13N_API_TOKEN")), wrapped))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	resources := client.Resources()
	ws := resources.Workspaces().Ref(required("A13N_WORKSPACE"))
	agent := required("A13N_AGENT")
	check(must(resources.Healthz().Get(ctx)).StatusCode == 200, "health")
	check(must(resources.Readyz().Get(ctx)).StatusCode == 200, "ready")
	check(must(resources.Auth().Configuration().Get(ctx)).Value.Initialized, "auth configuration")
	canonical := must(ws.Get(ctx)).Value
	byKey := resources.Workspaces().Ref(canonical.Key)
	request := generated.NewThread{AgentId: agent, Payload: a13n.TextPayload("[slow] [long] Exercise Go Thread SSE recovery.")}
	requestKey := key()
	submitted := must(byKey.Threads().Create(ctx, request, a13n.ThreadsCreateOptions{IdempotencyKey: requestKey}))
	replay := must(byKey.Threads().Create(ctx, request, a13n.ThreadsCreateOptions{IdempotencyKey: requestKey}))
	check(submitted.Receipt.StatusCode == 201 && replay.Receipt.StatusCode == 200 && submitted.Receipt.Value.Thread.Id == replay.Receipt.Value.Thread.Id, "submission replay")
	check(submitted.Receipt.Value.Thread.WorkspaceId == canonical.Id, "canonical workspace receipt")
	check(must(submitted.Thread.Get(ctx)).Value.WorkspaceId == canonical.Id, "canonical bound Thread")
	stream := must(submitted.Thread.Events(ctx, a13n.StreamOptions{MaxReconnects: 3, ReconnectDelay: 10 * time.Millisecond}))
	cursors := []string{}
	for len(cursors) < 2 || len(wrapped.cursors) < 2 {
		frame := must(stream.Next())
		switch frame := frame.(type) {
		case a13n.DeltaFrame:
			cursors = append(cursors, frame.Cursor())
		case a13n.BoundaryFrame:
			cursors = append(cursors, frame.Cursor())
		}
	}
	_ = stream.Close()
	check(wrapped.cuts == 1 && wrapped.cursors[1] == cursors[0] && cursors[0] != cursors[1], "applied cursor reconnect")
	wait(ctx, submitted.Run, generated.RunStatusCompleted)
	fmt.Println("Verified HTTPS: submission/replay/canonical binding, SSE disconnect/applied cursor, exact Run wait passed")

	start := func(text, agentID string) *a13n.Submitted {
		return must(ws.Threads().Create(ctx, generated.NewThread{AgentId: agentID, Payload: a13n.TextPayload(text)}, a13n.ThreadsCreateOptions{IdempotencyKey: key()}))
	}
	source := start("[interruptible] Hold long enough to queue an inbox message.", agent)
	queued := must(source.Thread.Inbox().Create(ctx, generated.Message{AgentId: agent, Payload: a13n.TextPayload("Queued message.")}, a13n.InboxCreateOptions{IdempotencyKey: key()}))
	check(queued.Run == nil, "queued receipt must have null Run")
	wait(ctx, source.Run, generated.RunStatusCompleted)
	entry := must(queued.Entry.Wait(ctx, 100*time.Millisecond)).Value
	check(entry.Status == generated.EntryStatusConsumed, "queued entry not consumed")
	successor := ws.Runs().Ref(must(entry.AssignedRunId.Get()))
	wait(ctx, &successor, generated.RunStatusCompleted)
	interrupted := start("[interruptible] Hold until interrupt.", agent)
	_ = must(interrupted.Run.Interrupt(ctx))
	wait(ctx, interrupted.Run, generated.RunStatusCancelled)
	forked := must(submitted.Run.Fork(ctx, generated.Fork{AgentId: agent, Payload: a13n.TextPayload("Forked response.")}, a13n.RunForkOptions{IdempotencyKey: key()}))
	check(forked.Receipt.Value.Thread.Id != submitted.Receipt.Value.Thread.Id, "fork Thread identity")
	wait(ctx, forked.Run, generated.RunStatusCompleted)
	waiting := start("[client] Review local SDK scenario.", required("A13N_CLIENT_TOOL_AGENT"))
	pending := must(wait(ctx, waiting.Run, generated.RunStatusWaiting).Value.Pending.Get())
	check(len(pending.Items) == 1, "pending action count")
	var answer generated.Answer
	check(answer.FromComplete(generated.Complete{Action: "complete", ToolCallId: pending.Items[0].ToolCallId, Result: map[string]any{"decision": "approved"}}) == nil, "resume union")
	resumed := must(waiting.Run.Resume(ctx, generated.ResumeRequest{Answers: &[]generated.Answer{answer}}, a13n.RunResumeOptions{IdempotencyKey: key()}))
	check(resumed.Value.Id != waiting.Receipt.Value.Run.GetOrEmpty().Id, "resume creates successor")
	resumedRun := ws.Runs().Ref(resumed.Value.Id)
	wait(ctx, &resumedRun, generated.RunStatusCompleted)
	fmt.Println("Verified HTTPS: queued entry, interrupt, fork and client-tool resume passed")

	content := bytes.Repeat([]byte("asset\n"), 50000)
	upload := must(ws.Uploads().Create(ctx, a13n.UploadFile{Name: "acceptance.bin", ContentType: "application/octet-stream", Reader: bytes.NewReader(content)}, a13n.UploadsCreateOptions{IdempotencyKey: key()}))
	asset := must(ws.Assets().Create(ctx, generated.AssetCreate{Name: "Acceptance", UploadId: upload.Value.UploadId}))
	download := must(ws.Assets().Ref(asset.Value.Id).Content().Get(ctx))
	received := must(io.ReadAll(download.Body))
	_ = download.Close()
	check(len(received) == 300000 && bytes.Equal(received, content), "binary transfer mismatch")
	current := must(ws.Agents().Ref(agent).Get(ctx))
	changed := must(ws.Agents().Ref(agent).Update(ctx, generated.AgentUpdate{Description: nullable.NewNullableWithValue("Go acceptance")}, a13n.AgentUpdateOptions{IfMatch: current.ETag()}))
	_, err := ws.Agents().Ref(agent).Update(ctx, generated.AgentUpdate{}, a13n.AgentUpdateOptions{IfMatch: current.ETag()})
	var apiError *a13n.ApiError
	check(errors.As(err, &apiError) && apiError.Status == 412, "stale CAS must fail")
	check(changed.ETag() != current.ETag(), "ETag mutation")
	fmt.Println("Verified HTTPS: 300000-byte streamed upload/download and structured CAS conflict passed")
	memory(ctx, client, ws, agent)
}

func memory(ctx context.Context, client *a13n.Client, ws a13n.WorkspaceResource, agent string) {
	provider := client.Resources().Organizations().Ref(required("A13N_ORGANIZATION")).MemoryProviders().Ref(required("A13N_MEMORY_PROVIDER"))
	check(must(provider.Test(ctx)).Value.Status == generated.ProviderTestStatusSucceeded, "provider probe")
	created := must(ws.Memories().Create(ctx, generated.MemoryCreate{Key: key(), Name: "Go acceptance"}))
	memory := ws.Memories().Ref(created.Value.Id)
	path := "projects/计划 #1%.md"
	file := memory.Files().Ref(path)
	first := must(memory.Files().Create(ctx, generated.MemoryFileCreate{Path: path, Content: "first"}))
	check(must(file.Get(ctx)).Value.Content == "first", "Unicode path")
	_ = must(file.Replace(ctx, generated.MemoryFileReplace{Content: "second"}, a13n.MemoryFileReplaceOptions{IfMatch: first.ETag()}))
	revisions := []generated.MemoryRevision{}
	for page, err := range memory.Revisions().Pages(ctx, a13n.MemoryRevisionsListOptions{Path: &path, Limit: ptr(1)}) {
		check(err == nil, "revision pagination")
		revisions = append(revisions, page.Value.Items...)
	}
	check(len(revisions) == 2, "revision count")
	var original, updated int
	for _, r := range revisions {
		if r.Op == "create" {
			original = r.Seq
		}
		if r.Op == "update" {
			updated = r.Seq
		}
	}
	current := must(file.Get(ctx))
	_ = must(file.Delete(ctx, a13n.MemoryFileDeleteOptions{IfMatch: current.ETag()}))
	restored := must(memory.Revisions().Ref(updated).Restore(ctx, a13n.MemoryRevisionRestoreOptions{}))
	check(must(restored.Value.File.Get()).Content == "first", "numeric revision restore")
	undone := must(memory.Revisions().Ref(original).Restore(ctx, a13n.MemoryRevisionRestoreOptions{IfMatch: ptr(must(file.Get(ctx)).ETag())}))
	check(undone.Value.File.IsNull(), "restore creation null result")
	_ = must(memory.Revisions().Ref(updated).Restore(ctx, a13n.MemoryRevisionRestoreOptions{}))
	mounts := []generated.MemoryMount{{Name: "notes", MemoryId: created.Value.Id, Access: "read"}}
	submitted := must(ws.Threads().Create(ctx, generated.NewThread{AgentId: agent, Payload: a13n.TextPayload("Memory acceptance."), Memories: &mounts}, a13n.ThreadsCreateOptions{IdempotencyKey: key()}))
	wait(ctx, submitted.Run, generated.RunStatusCompleted)
	thread := must(submitted.Thread.Get(ctx))
	_ = must(submitted.Thread.Memories().Ref("notes").Update(ctx, generated.MemoryMountUpdate{Access: nullable.NewNullableWithValue(generated.MemoryAccessWrite)}, a13n.ThreadMemoryUpdateOptions{IfMatch: thread.ETag()}))
	check(must(submitted.Run.Get(ctx)).Value.MemoryMounts[0].Access == generated.MemoryAccessRead, "frozen Run mounts")
	recordMemory := must(ws.Memories().Create(ctx, generated.MemoryCreate{Key: key(), Name: "Records", Type: ptr("mem0_oss"), ProviderId: nullable.NewNullableWithValue(required("A13N_MEMORY_PROVIDER"))}))
	records := ws.Memories().Ref(recordMemory.Value.Id).Records()
	record := must(records.Create(ctx, generated.MemoryRecordText{Text: "prefers tea"}))
	_ = must(records.Ref(record.Value.Id).Replace(ctx, generated.MemoryRecordText{Text: "prefers coffee"}))
	found := must(records.Search(ctx, generated.MemoryRecordSearch{Query: "coffee", Limit: ptr(3)}))
	check(len(found.Value.Items) == 1 && found.Value.Items[0].Id == record.Value.Id, "record search")
	_ = must(records.Ref(record.Value.Id).Delete(ctx))
	check(len(must(records.List(ctx, a13n.MemoryRecordsListOptions{})).Value.Items) == 0, "record deletion")
	fmt.Println("Verified HTTPS: Unicode memory files, paged numeric history, nullable restore, frozen mounts, record CRUD/search and fixture provider passed")
}

func main() {
	offline()
	if len(os.Args) > 1 && os.Args[1] == "--offline" {
		return
	}
	live()
}
