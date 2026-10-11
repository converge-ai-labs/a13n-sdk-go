package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func previewDelta(sequence int, event string) DeltaFrame {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(event), &object); err != nil {
		panic(err)
	}
	return DeltaFrame{BoundaryFrame: BoundaryFrame{RunID: "run", Attempt: 1, Sequence: sequence}, Event: object}
}
func TestTextProjectionAttributionDiscontinuityAndBound(t *testing.T) {
	p := textPreview{active: make(map[string]bool)}
	events := []string{
		`{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`,
		`{"type":"TEXT_MESSAGE_START","messageId":"child","role":"assistant","subagentRunId":"child"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"child","delta":"never","subagentRunId":"child"}`,
		`{"type":"CUSTOM","name":"a13n.stream.fragment","subagentRunId":"child","value":{"id":"opaque","index":0,"count":2,"data":"irrelevant"}}`,
		`{"type":"TEXT_MESSAGE_START","messageId":"user","role":"user"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"user","delta":"user"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"hidden","metadata":{"display":false}}`,
		`{"type":"TEXT_MESSAGE_START","messageId":"nested","role":"assistant","parentSubagentRunId":"child"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"nested","delta":"nested","parentSubagentRunId":"child"}`,
		`{"type":"CUSTOM","name":"future.unknown","value":null}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":""}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"root"}`,
	}
	var chunks []string
	for j, event := range events {
		chunk, emit, err := p.project(previewDelta(j+1, event))
		if err != nil {
			t.Fatal(err)
		}
		if emit {
			chunks = append(chunks, chunk)
		}
	}
	if len(chunks) != 2 || chunks[0] != "" || chunks[1] != "root" {
		t.Fatal(chunks)
	}
	cases := []struct {
		name   string
		frame  ThreadFrame
		reason string
	}{
		{"gap", GapFrame{RunID: "run"}, "gap"},
		{"reset", ResetFrame{RunID: "run"}, "reset"},
		{"missing", previewDelta(1, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"late"}`), "missing_start"},
		{"fragment", previewDelta(1, `{"type":"CUSTOM","name":"a13n.stream.fragment","value":{"id":"TEXT_MESSAGE_CONTENT_not_an_identity","index":0,"count":2,"data":"not a text suffix"}}`), "fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh := textPreview{active: make(map[string]bool)}
			_, _, err := fresh.project(tc.frame)
			var interrupted *PreviewInterruptedError
			if !errors.As(err, &interrupted) || interrupted.Reason != tc.reason {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		frame  ThreadFrame
		reason string
	}{
		{"hole", previewDelta(14, `{"type":"CUSTOM","name":"ignored","value":null}`), "sequence_discontinuity"},
		{"attempt", BoundaryFrame{RunID: "run", Attempt: 2, Sequence: 1}, "attempt_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := p
			_, _, err := copy.project(tc.frame)
			var interrupted *PreviewInterruptedError
			if !errors.As(err, &interrupted) || interrupted.Reason != tc.reason {
				t.Fatal(err)
			}
		})
	}
	bounded := textPreview{active: make(map[string]bool)}
	for j := 1; j <= 9; j++ {
		_, _, err := bounded.project(previewDelta(j, fmt.Sprintf(`{"type":"TEXT_MESSAGE_START","messageId":"%d","role":"assistant"}`, j)))
		if j <= 8 && err != nil {
			t.Fatal(err)
		}
		if j == 9 {
			var interrupted *PreviewInterruptedError
			if !errors.As(err, &interrupted) || interrupted.Reason != "active_message_limit" {
				t.Fatal(err)
			}
		}
	}
}

// All origins reuse the same finite SSE reader; interruption never seals Result.
func TestTextInterruptionRawContinuationAndResultAllOrigins(t *testing.T) {
	for _, origin := range []string{"start", "entry", "run", "resume"} {
		t.Run(origin, func(t *testing.T) {
			var sealed atomic.Bool
			var posts, streams atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case req.Method == "POST":
					posts.Add(1)
					w.WriteHeader(201)
					if strings.HasSuffix(req.URL.Path, "/resume") {
						_, _ = io.WriteString(w, runView("run", "accepted", "thr"))
					} else {
						_, _ = io.WriteString(w, queuedReceipt)
					}
				case strings.Contains(req.URL.Path, "/inbox/ent"):
					_, _ = io.WriteString(w, entryView("consumed", `"run"`))
				case strings.HasSuffix(req.URL.Path, "/runs/run"):
					status := "running"
					if sealed.Load() {
						status = "completed"
					}
					_, _ = io.WriteString(w, `{"id":"run","thread_id":"thr","status":"`+status+`","output":"authoritative"}`)
				case strings.HasSuffix(req.URL.Path, "/stream"):
					streams.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					// A foreign envelope is its own sequence domain and must not create a hole.
					_, _ = io.WriteString(w, `id: 70-90`+"\n"+`event: delta`+"\n"+`data: {"run_id":"foreign","attempt":9,"sequence":90,"event":{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"foreign"},"item":null}`+"\n\n")
					for j, event := range []string{
						`{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`,
						`{"type":"CUSTOM","name":"a13n.stream.fragment","subagentRunId":"child","value":{"id":"same","index":0,"count":2,"data":"irrelevant"}}`,
						`{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"preview"}`,
						`{"type":"CUSTOM","name":"a13n.stream.fragment","value":{"id":"opaque","index":0,"count":2,"data":"unsupported custom framing"}}`,
						`{"type":"CUSTOM","name":"future.unknown","value":{"raw":true}}`,
					} {
						_, _ = io.WriteString(w, nativeFrame(j+1, event))
					}
					w.(http.Flusher).Flush()
					<-req.Context().Done()
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := NewClient(server.URL, NewSecret("key"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var i *Interaction
			switch origin {
			case "start":
				i, err = client.Agent("agent").Start(ctx, "hi")
			case "entry":
				i, err = client.Entry("thr", "ent").Observe(ctx)
			case "run":
				i, err = client.Run("run").Observe(ctx)
			case "resume":
				i, err = client.Run("predecessor").Resume(ctx, Answers{})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close()
			var chunks []string
			var interrupted *PreviewInterruptedError
			for chunk, err := range i.Text() {
				if err != nil {
					if !errors.As(err, &interrupted) {
						t.Fatal(err)
					}
					break
				}
				chunks = append(chunks, chunk)
			}
			if strings.Join(chunks, "") != "preview" || interrupted == nil || interrupted.Reason != "fragment" {
				t.Fatal(chunks, interrupted)
			}
			for _, err := range i.Text() {
				if err != interrupted {
					t.Fatal("preview interruption not sticky", err)
				}
			}
			frame, err := i.Next()
			if err != nil || string(frame.(DeltaFrame).Event["name"]) != `"future.unknown"` {
				t.Fatal("raw continuation inaccessible", frame, err)
			}
			sealed.Store(true)
			outcome, err := i.Result(ctx)
			if err != nil || outcome.Output != "authoritative" || outcome.RunID != "run" || outcome.Status != RunCompleted {
				t.Fatal(outcome, err)
			}
			if streams.Load() != 1 {
				t.Fatal("second subscription", streams.Load())
			}
			expectedPosts := int32(0)
			if origin == "start" || origin == "resume" {
				expectedPosts = 1
			}
			if posts.Load() != expectedPosts {
				t.Fatal(posts.Load())
			}
		})
	}
}

func TestTextPausedYieldCloseDoesNotDeadlockOrAllowConcurrentReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(req.URL.Path, "/runs/run") {
			_, _ = io.WriteString(w, runView("run", "running", "thr"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeFrame(1, `{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`)+nativeFrame(2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"once"}`))
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	i, err := client.Run("run").Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		i.Text()(func(chunk string, err error) bool {
			if err != nil || chunk != "once" {
				t.Error(chunk, err)
				return false
			}
			if _, err := i.Next(); err == nil {
				t.Error("concurrent raw reader accepted")
			}
			for _, err := range i.Text() {
				if err == nil {
					t.Error("concurrent preview accepted")
				}
			}
			if err := i.Close(); err != nil {
				t.Error(err)
			}
			return false
		})
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("Close depends on paused iterator progress")
	}
	if _, err := i.Result(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTextBoundariesDoNotCoverMissingDeltasOrRegress(t *testing.T) {
	for _, sequence := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint(sequence), func(t *testing.T) {
			p := textPreview{active: make(map[string]bool)}
			_, _, err := p.project(previewDelta(1, `{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = p.project(previewDelta(2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"before"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = p.project(BoundaryFrame{RunID: "run", Attempt: 1, Sequence: sequence})
			if sequence > 2 {
				var interrupted *PreviewInterruptedError
				if !errors.As(err, &interrupted) || interrupted.Reason != "sequence_discontinuity" {
					t.Fatal("ahead boundary hid missing delta", err)
				}
				return
			}
			if err != nil || p.sequence != 2 {
				t.Fatal("late/equal boundary changed coverage", p.sequence, err)
			}
			chunk, emit, err := p.project(previewDelta(3, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"after"}`))
			if err != nil || !emit || chunk != "after" {
				t.Fatal(chunk, emit, err)
			}
		})
	}
}

func TestHiddenStartsStaySuppressedUntilEndWithinSameBound(t *testing.T) {
	p := textPreview{active: make(map[string]bool)}
	events := []string{
		`{"type":"TEXT_MESSAGE_START","messageId":"hidden","role":"assistant","metadata":{"display":false}}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"hidden","delta":"never"}`,
		`{"type":"TEXT_MESSAGE_END","messageId":"hidden"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"unseen","delta":"never","metadata":{"display":false}}`,
		`{"type":"TEXT_MESSAGE_END","messageId":"unseen","metadata":{"display":false}}`,
		`{"type":"CUSTOM","name":"a13n.stream.fragment","metadata":{"display":false},"value":{"id":"opaque","index":0,"count":2,"data":"hidden"}}`,
		`{"type":"TEXT_MESSAGE_START","messageId":"child","role":"assistant","subagentRunId":"child"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"child","delta":"never","subagentRunId":"child"}`,
		`{"type":"TEXT_MESSAGE_END","messageId":"child","subagentRunId":"child"}`,
		`{"type":"TEXT_MESSAGE_START","messageId":"visible","role":"assistant"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"visible","delta":"visible"}`,
		`{"type":"TEXT_MESSAGE_END","messageId":"visible"}`,
	}
	var chunks string
	for j, event := range events {
		chunk, emit, err := p.project(previewDelta(j+1, event))
		if err != nil {
			t.Fatal(err)
		}
		if emit {
			chunks += chunk
		}
	}
	if chunks != "visible" || len(p.active) != 0 {
		t.Fatal(chunks, p.active)
	}
	for j := 1; j <= 9; j++ {
		_, _, err := p.project(previewDelta(len(events)+j, fmt.Sprintf(`{"type":"TEXT_MESSAGE_START","messageId":"h%d","role":"assistant","metadata":{"display":false}}`, j)))
		if j <= 8 && err != nil {
			t.Fatal(err)
		}
		if j == 9 {
			var interrupted *PreviewInterruptedError
			if !errors.As(err, &interrupted) || interrupted.Reason != "active_message_limit" {
				t.Fatal("suppressed identities escaped bound", err)
			}
		}
	}
}

func TestTextProtocolInterruptionIsStickyAndKeepsResult(t *testing.T) {
	var sealed atomic.Bool
	var streams atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(req.URL.Path, "/runs/run") {
			status := "running"
			if sealed.Load() {
				status = "completed"
			}
			_, _ = io.WriteString(w, `{"id":"run","thread_id":"thr","status":"`+status+`","output":"authoritative"}`)
			return
		}
		streams.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: invalid-cursor\nevent: delta\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	i, err := client.Run("run").Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	var interruption *PreviewInterruptedError
	for _, err := range i.Text() {
		if !errors.As(err, &interruption) || interruption.Reason != "protocol" || !errors.Is(err, ErrProtocol) {
			t.Fatal("protocol error not a preview interruption", err)
		}
	}
	if interruption == nil {
		t.Fatal("protocol failure hidden")
	}
	for _, err := range i.Text() {
		if err != interruption {
			t.Fatal("protocol interruption not sticky", err)
		}
	}
	sealed.Store(true)
	outcome, err := i.Result(ctx)
	if err != nil || outcome.Output != "authoritative" || streams.Load() != 1 {
		t.Fatal("preview protocol error poisoned Result", outcome, err, streams.Load())
	}
}

func TestInitialBoundarySetsCoordinateWithoutReconstructingMessages(t *testing.T) {
	for _, event := range []string{
		`{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`,
		`{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"orphan"}`,
		`{"type":"TEXT_MESSAGE_END","messageId":"root"}`,
	} {
		t.Run(event, func(t *testing.T) {
			p := textPreview{active: make(map[string]bool)}
			_, _, err := p.project(BoundaryFrame{RunID: "run", Attempt: 1, Sequence: 42})
			if err != nil || p.sequence != 42 || len(p.active) != 0 {
				t.Fatal("initial coordinate invented state or interrupted", err, p)
			}
			_, _, err = p.project(previewDelta(43, event))
			if strings.Contains(event, "TEXT_MESSAGE_START") {
				if err != nil {
					t.Fatal(err)
				}
				chunk, emit, err := p.project(previewDelta(44, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"visible"}`))
				if err != nil || !emit || chunk != "visible" {
					t.Fatal(chunk, emit, err)
				}
			} else {
				var interrupted *PreviewInterruptedError
				if !errors.As(err, &interrupted) || interrupted.Reason != "missing_start" {
					t.Fatal("boundary repaired missing START", err)
				}
			}
		})
	}
}

func TestTextDuplicateDescendingAndUnmatchedEndStayInterrupted(t *testing.T) {
	for _, mode := range []string{"duplicate", "descending", "unmatched_end", "initial_orphan"} {
		t.Run(mode, func(t *testing.T) {
			var sealed atomic.Bool
			var streams atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(req.URL.Path, "/runs/run") {
					status := "running"
					if sealed.Load() {
						status = "completed"
					}
					_, _ = io.WriteString(w, `{"id":"run","thread_id":"thr","status":"`+status+`","output":"authoritative"}`)
					return
				}
				streams.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "initial_orphan" {
					_, _ = io.WriteString(w, "id: 1-1\nevent: boundary\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":42}\n\n")
					_, _ = io.WriteString(w, nativeFrame(43, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"orphan"}`))
				} else {
					_, _ = io.WriteString(w, nativeFrame(1, `{"type":"TEXT_MESSAGE_START","messageId":"root","role":"assistant"}`))
					_, _ = io.WriteString(w, nativeFrame(2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"before"}`))
					sequence, event := 2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"root","delta":"duplicate"}`
					if mode == "descending" {
						sequence = 1
					}
					if mode == "unmatched_end" {
						sequence, event = 3, `{"type":"TEXT_MESSAGE_END","messageId":"never_started"}`
					}
					_, _ = fmt.Fprintf(w, "id: 1-3\nevent: delta\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":%d,\"event\":%s,\"item\":null}\n\n", sequence, event)
				}
				_, _ = io.WriteString(w, nativeFrame(44, `{"type":"CUSTOM","name":"future.unknown","value":null}`))
				w.(http.Flusher).Flush()
				<-req.Context().Done()
			}))
			defer server.Close()
			client, err := NewClient(server.URL, NewSecret("key"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			i, err := client.Run("run").Observe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close()
			var interruption *PreviewInterruptedError
			var chunks string
			for chunk, err := range i.Text() {
				if err != nil {
					if !errors.As(err, &interruption) {
						t.Fatal(err)
					}
					break
				}
				chunks += chunk
			}
			wantedReason, wantedChunks := "sequence_discontinuity", "before"
			if mode == "unmatched_end" {
				wantedReason = "missing_start"
			}
			if mode == "initial_orphan" {
				wantedReason, wantedChunks = "missing_start", ""
			}
			if interruption == nil || interruption.Reason != wantedReason || chunks != wantedChunks {
				t.Fatal(chunks, interruption)
			}
			for _, err := range i.Text() {
				if err != interruption {
					t.Fatal("interruption not sticky", err)
				}
			}
			frame, err := i.Next()
			if err != nil || string(frame.(DeltaFrame).Event["name"]) != `"future.unknown"` {
				t.Fatal("raw continuation lost", frame, err)
			}
			sealed.Store(true)
			outcome, err := i.Result(ctx)
			if err != nil || outcome.Output != "authoritative" || streams.Load() != 1 {
				t.Fatal(outcome, err, streams.Load())
			}
		})
	}
}
