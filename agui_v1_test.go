package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

// These are native AG-UI 1.0 contents inside the Service's unchanged envelope.
// The SDK neither reduces these payloads nor interprets them as Service authority.
var aguiNativeEvents = []string{
	`{"type":"RUN_STARTED","threadId":"harness-thread","runId":"harness-root","protocolVersion":"1.0"}`,
	`{"type":"TEXT_MESSAGE_CONTENT","messageId":"same","subagentRunId":"child","delta":"child-only"}`,
	`{"type":"TOOL_CALL_RESULT","messageId":"result","toolCallId":"call","subagentRunId":"child","role":"tool","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"url","value":"https://example.invalid/image.png"}},{"type":"image","source":{"type":"url","value":"https://example.invalid/image.png"}},{"type":"text","text":"after"}]}`,
	`{"type":"CUSTOM","name":"a13n.input.media","metadata":{"display":true,"media":true,"image_object_id":"asset-ref"},"value":{"thread_id":"harness-thread","run_id":"harness-root","sequence":4,"event":{"kind":"video-url","content":{"kind":"video-url","url":"https://example.invalid/video.mp4","media_type":"video/mp4"}}}}`,
	`{"type":"SUBAGENT_STARTED","subagentRunId":"grandchild","parentSubagentRunId":"child","parentToolCallId":"call","subagentName":"reviewer"}`,
	`{"type":"RUN_FINISHED","threadId":"child-thread","runId":"child","outcome":{"type":"success"},"result":{"enabled":false},"usage":[{"cacheWriteInputTokens":11}]}`,
	`{"type":"CUSTOM","name":"a13n.test","value":null,"metadata":{"display":false,"media":false},"nativeExtension":{"empty":[],"zero":0}}`,
}

func nativeFrame(sequence int, event string) string {
	return fmt.Sprintf("id: 77-%d\nevent: delta\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":%d,\"event\":%s,\"item\":null}\n\n", sequence, sequence, event)
}

func TestAGUI1NativeMediaAttributionAndNullPayloadAreLossless(t *testing.T) {
	for seq, event := range aguiNativeEvents {
		frame, err := parseThreadFrame("delta", fmt.Sprintf("77-%d", seq+1), true, []byte(fmt.Sprintf(`{"run_id":"run","attempt":1,"sequence":%d,"event":%s,"item":null}`, seq+1, event)))
		if err != nil {
			t.Fatal(err)
		}
		delta := frame.(DeltaFrame)
		encoded, err := json.Marshal(delta.Event)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		_ = json.Unmarshal(encoded, &got)
		_ = json.Unmarshal([]byte(event), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("native media/attribution lost: %s", encoded)
		}
		if delta.RunID != "run" {
			t.Fatal("native runId changed Service authority")
		}
	}
	content := `{"messageId":"same","subagentRunId":"child","parentSubagentRunId":"root-child","result_parts":[{"type":"image","source":{"type":"url","value":"https://example.invalid/image.png"}},{"type":"text","text":"{\"payload_omitted\":true,\"size_bytes\":19}"}],"metadata":{"display":false,"media":true},"truncated":true}`
	client := coverageClient(t, func(req *http.Request) *http.Response {
		return coverageResponse(200, `{"run":{"id":"run","thread_id":"thr","status":"completed"},"items":[{"id":"root-text","kind":"text_message","state":"completed","first_stream_id":"1-1","last_stream_id":"1-1","content":{"messageId":"same","text":"root-only"}},{"id":"child-tool","kind":"tool_call","state":"completed","first_stream_id":"1-2","last_stream_id":"1-3","content":`+content+`}],"position":"1-3","resume_after":null,"dropped":2,"complete":true}`)
	})
	items, err := client.Run("run").Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Value.Items) != 2 || items.Value.Dropped != 2 || !items.Value.Complete || items.Value.Items[0].Content["text"] != "root-only" {
		t.Fatal(items)
	}
	encoded, _ := json.Marshal(items.Value.Items[1].Content)
	var got, want any
	_ = json.Unmarshal(encoded, &got)
	_ = json.Unmarshal([]byte(content), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("committed display flattened: %s", encoded)
	}
}

func TestChildRunFinishedCannotCompleteRootInteraction(t *testing.T) {
	var sealed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "POST":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, queuedReceipt)
		case strings.HasSuffix(req.URL.Path, "/inbox/ent"):
			_, _ = io.WriteString(w, entryView("consumed", `"run"`))
		case strings.HasSuffix(req.URL.Path, "/runs/run"):
			status := "running"
			if sealed.Load() {
				status = "completed"
			}
			_, _ = io.WriteString(w, runView("run", status, "thr"))
		case strings.HasSuffix(req.URL.Path, "/stream"):
			w.Header().Set("Content-Type", "text/event-stream")
			// A child terminal precedes more root output and an authoritative root seal.
			_, _ = io.WriteString(w, nativeFrame(1, aguiNativeEvents[5])+nativeFrame(2, `{"type":"TEXT_MESSAGE_CONTENT","messageId":"same","delta":"root-only"}`))
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
	interaction, err := client.Agent("agent").Start(ctx, "Root task", StartOptions{RequestKey: "child"})
	if err != nil {
		t.Fatal(err)
	}
	defer interaction.Close()
	frame, err := interaction.Next()
	if err != nil {
		t.Fatal(err)
	}
	if frame.(DeltaFrame).RunID != "run" || string(frame.(DeltaFrame).Event["runId"]) != `"child"` {
		t.Fatal(frame)
	}
	frame, err = interaction.Next()
	if err != nil || string(frame.(DeltaFrame).Event["delta"]) != `"root-only"` {
		t.Fatal(frame, err)
	}
	short, stop := context.WithCancel(ctx)
	stop()
	if _, err := interaction.Result(short); !errors.Is(err, context.Canceled) {
		t.Fatal("child terminal completed root", err)
	}
	sealed.Store(true)
	outcome, err := interaction.Result(ctx)
	if err != nil || outcome.Run.ID != "run" || outcome.Status() != generated.RunStatusCompleted {
		t.Fatal(outcome, err)
	}
	if _, err := interaction.Next(); !errors.Is(err, io.EOF) {
		t.Fatal("root seal did not end finite stream", err)
	}
}
