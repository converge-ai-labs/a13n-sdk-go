package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	a13n "github.com/converge-ai-labs/a13n-sdk-go"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// ZIP-installed dispatch retains the whole mutable tail and each explicit
// ordinal window. This controlled fixture does not assert Service internals.
func pagedDisplayOffline() {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reads++
		check(req.Method == "GET" && req.URL.Path == "/api/v1/runs/paged/items", "installed ordinal operation")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "paged")
		query := req.URL.Query().Encode()
		first, last, baseline, complete := 1, 10, true, false
		switch query {
		case "limit=2":
		case "after=2&limit=2", "before=5&limit=2":
			first, last, baseline, complete = 3, 4, false, true
		case "after=0&limit=2":
			first, last, baseline, complete = 1, 2, false, true
		case "":
			first, last, complete = 9, 10, true
		case "after=0&before=1", "before=0", "after=-1", "limit=501":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_argument","message":"invalid ordinal window","details":{"field":"before"}}}`)
			return
		default:
			panic("unexpected ordinal query " + query)
		}
		status, state := "running", "in_progress"
		if complete {
			status, state = "completed", "interrupted"
		}
		items := []map[string]any{}
		for ordinal := first; ordinal <= last; ordinal++ {
			items = append(items, map[string]any{"id": fmt.Sprint(ordinal), "ordinal": ordinal, "kind": "text_message", "state": state, "content": map[string]any{"text": "active"}})
		}
		value := map[string]any{"run": map[string]any{"id": "paged", "thread_id": "thr", "status": status, "display_position": nil}, "items": items, "baseline": baseline, "complete": complete, "position": nil, "continuation": nil, "resume_after": nil}
		if baseline {
			value["position"] = "1-8"
			value["continuation"] = json.RawMessage(`{"run_id":"paged","position":{"attempt":1,"sequence":8},"next_ordinal":11,"fragments":{"pending":{"x":{"count":2,"parts":["partial"]}}},"observer":{"state":{"children":{"child":{"children":{"grandchild":{"request_index":0}}}}}}}`)
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	defer server.Close()
	client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
	defer client.Close()
	ctx := context.Background()
	two, five, zero, one, negative, large := 2, 5, 0, 1, -1, 501
	baseline := must(client.Run("paged").Items(ctx, a13n.RunItemsOptions{Limit: &two}))
	check(baseline.Value.Baseline && !baseline.Value.Complete && len(baseline.Value.Items) == 10 && baseline.Value.Items[9].Ordinal == 10 && baseline.RequestID() == "paged", "installed whole mutable tail beyond limit")
	continuation := must(baseline.Value.Continuation.Get())
	check(continuation.NextOrdinal == 11 && continuation.Position.Sequence == 8 && baseline.Value.Run.DisplayPosition.IsNull(), "installed continuation and nullable display position")
	check(strings.Contains(string(must(json.Marshal(continuation))), `"grandchild":{"request_index":0}`), "installed recursive normalization cursor")
	for _, options := range []a13n.RunItemsOptions{{Before: &five, Limit: &two}, {After: &two, Limit: &two}, {After: &zero, Limit: &two}} {
		window := must(client.Run("paged").Items(ctx, options))
		check(!window.Value.Baseline && window.Value.Complete && len(window.Value.Items) == 2 && window.Value.Position.IsNull() && window.Value.Continuation.IsNull() && window.Value.ResumeAfter.IsNull(), "installed historical windows never claim coverage")
	}
	recent := must(client.Run("paged").Items(ctx))
	check(recent.Value.Baseline && recent.Value.Complete && recent.Value.Items[0].Ordinal == 9 && reads == 5, "installed sealed recent window does not auto-load all history")
	for _, options := range []a13n.RunItemsOptions{{Before: &one, After: &zero}, {Before: &zero}, {After: &negative}, {Limit: &large}} {
		_, err := client.Run("paged").Items(ctx, options)
		var failure *a13n.ApiError
		check(errors.As(err, &failure) && failure.Status == 400 && failure.Code == "invalid_argument" && failure.RequestID == "paged", "installed ordinal Service error metadata")
	}
	fmt.Println("Installed module: whole-tail baseline > limit, recursive continuation, explicit ordinal history windows, sealed recent != all history and query errors passed (local mock)")
}

func historicalWindow(value generated.RunItems) {
	check(!value.Baseline && value.Position.IsNull() && value.Continuation.IsNull() && value.ResumeAfter.IsNull(), "historical window has no live baseline or coverage")
}

// The Service's deterministic HTTPS fixture owns the actual page/tail size.
// Limit=1 alone does not prove that sealed page history exists; inspect ordinals.
func pagedDisplayLive(ctx context.Context, run a13n.Run) {
	one, zero := 1, 0
	recent := must(run.Items(ctx, a13n.RunItemsOptions{Limit: &one}))
	check(recent.Value.Baseline && recent.Value.Complete && recent.Value.Run.Id == run.ID && recent.Value.Position.GetOrEmpty() != "", "live sealed default display baseline")
	check(recent.Value.Continuation.IsSpecified() && !recent.Value.Continuation.IsNull(), "live baseline normalization continuation")
	for index, item := range recent.Value.Items {
		check(item.Ordinal >= 1, "live required positive ordinal")
		if index > 0 {
			check(item.Ordinal == recent.Value.Items[index-1].Ordinal+1, "live dense ordered window")
		}
	}
	first := must(run.Items(ctx, a13n.RunItemsOptions{After: &zero, Limit: &one}))
	historicalWindow(first.Value)
	check(first.Value.Complete && len(first.Value.Items) == 1 && first.Value.Items[0].Ordinal == 1, "live first historical ordinal")
	check(len(recent.Value.Items) > 0, "live sealed baseline has display items")
	lastOrdinal := recent.Value.Items[len(recent.Value.Items)-1].Ordinal
	check(lastOrdinal > 1, "live ordinal probe has multiple items")
	before := must(run.Items(ctx, a13n.RunItemsOptions{Before: &lastOrdinal, Limit: &one}))
	historicalWindow(before.Value)
	check(len(before.Value.Items) == 1 && before.Value.Items[0].Ordinal == lastOrdinal-1, "live exclusive before ordinal")
	_, err := run.Items(ctx, a13n.RunItemsOptions{Before: &one, After: &zero})
	var failure *a13n.ApiError
	check(errors.As(err, &failure) && failure.Status == 400 && failure.Code == "invalid_argument", "live mutually exclusive ordinal error")
	fmt.Println("Verified HTTPS: sealed default baseline/continuation, historical before/after with null coverage and ordinal query errors passed")
	if recent.Value.Items[0].Ordinal > 1 {
		fmt.Println("Verified HTTPS: sealed recent window excludes earlier ordinals (not all history)")
	} else {
		fmt.Println("NOT EXERCISED against HTTPS: sealed recent window excluding earlier pages; whole mutable tail retained this short Run (covered by installed mock)")
	}
}

func sealedHistoryLive(ctx context.Context, client *a13n.Client, agentID string) {
	// The fixture must fail only the latest user prompt, not every later Run
	// whose inherited history contains the marker. Do not use the old [fail].
	failurePrompt := required("A13N_FAILURE_PROMPT")
	failed := must(client.Agent(agentID).Start(ctx, failurePrompt, a13n.StartOptions{RequestKey: key()}))
	defer failed.Close()
	failure := must(failed.Result(ctx))
	check(failure.Status() == generated.RunStatusFailed, "live deterministic failed Run")
	continueSealedLive(ctx, client, agentID, failed.Thread, failure)

	active := must(client.Agent(agentID).Start(ctx, "[interruptible] [slow] [long] Interrupt then explicitly continue.", a13n.StartOptions{RequestKey: key()}))
	defer active.Close()
	ready, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var run a13n.Run
	for {
		entry := must(active.Entry.Get(ready))
		if id := entry.Value.AssignedRunId.GetOrEmpty(); id != "" {
			run = client.Run(id)
			view := must(run.Get(ready))
			thread := must(active.Thread.Get(ready))
			if entry.Value.Status == generated.EntryStatusConsumed && view.Value.Status == generated.RunStatusRunning && thread.Value.CurrentRunId.GetOrEmpty() == run.ID {
				break
			}
			check(view.Value.Status == generated.RunStatusAccepted || view.Value.Status == generated.RunStatusRunning, "cancel probe sealed before interrupt")
		}
		select {
		case <-ready.Done():
			panic("cancel probe did not reach active exact Run")
		case <-ticker.C:
		}
	}
	_ = must(run.Interrupt(ctx))
	cancelled := must(active.Result(ctx))
	check(cancelled.Run.ID == run.ID && cancelled.Status() == generated.RunStatusCancelled, "live exact cancelled outcome")
	continueSealedLive(ctx, client, agentID, active.Thread, cancelled)
	fmt.Println("Verified HTTPS: failed/cancelled latest sealed history, normal explicit Send and exact completed successor parent passed")
}

func continueSealedLive(ctx context.Context, client *a13n.Client, agentID string, thread a13n.Thread, predecessor a13n.RunOutcome) {
	before := must(thread.Get(ctx))
	check(before.Value.CurrentRunId.IsNull() && before.Value.LastRunId.GetOrEmpty() == predecessor.Run.ID, "all sealed outcomes retained as last_run_id")
	followup := must(client.Agent(agentID).Send(ctx, thread.ID, "Continue normally from the saved history.", a13n.SendOptions{RequestKey: key()}))
	defer followup.Close()
	outcome := must(followup.Result(ctx))
	check(outcome.Status() == generated.RunStatusCompleted && outcome.Run.ID != predecessor.Run.ID && outcome.Snapshot.Value.ParentRunId.GetOrEmpty() == predecessor.Run.ID, "normal Send continues failed/cancelled checkpoint history")
	entry := must(followup.Entry.Get(ctx))
	check(entry.Value.Status == generated.EntryStatusConsumed && entry.Value.AssignedRunId.GetOrEmpty() == outcome.Run.ID, "exact successor consumed Entry")
}

func sealedHistoryOffline() {
	for _, status := range []string{"failed", "cancelled"} {
		posts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch req.URL.Path {
			case "/api/v1/threads/thr":
				_, _ = fmt.Fprintf(w, `{"id":"thr","current_run_id":null,"last_run_id":%q}`, status)
			case "/api/v1/runs/" + status:
				_, _ = fmt.Fprintf(w, `{"id":%q,"thread_id":"thr","status":%q,"failure":{"code":%q,"message":"ended"}}`, status, status, status)
			case "/api/v1/threads/thr/inbox":
				check(req.Method == "POST" && req.Header.Get("Idempotency-Key") != "", "installed ordinary explicit Send after seal")
				posts++
				w.WriteHeader(201)
				_, _ = io.WriteString(w, `{"thread":{"id":"thr"},"entry":{"id":"next","thread_id":"thr"},"run":null}`)
			case "/api/v1/threads/thr/inbox/next":
				_, _ = io.WriteString(w, `{"id":"next","thread_id":"thr","status":"consumed","assigned_run_id":"successor"}`)
			case "/api/v1/runs/successor":
				_, _ = fmt.Fprintf(w, `{"id":"successor","thread_id":"thr","status":"completed","parent_run_id":%q}`, status)
			default:
				panic("unexpected resume/retry request: " + req.URL.Path)
			}
		}))
		client := must(a13n.NewClient(server.URL, a13n.NewSecret("offline"), nil))
		ctx := context.Background()
		predecessor := must(client.Run(status).Wait(ctx))
		continueSealedLive(ctx, client, "agent", client.Thread("thr"), predecessor)
		check(posts == 1, "installed failed/cancelled history sends once, without automatic resubmission")
		_ = client.Close()
		server.Close()
	}
	fmt.Println("Installed module: failed/cancelled last-Run history, ordinary explicit Send and exact successor Entry/parent passed (local mock)")
}
