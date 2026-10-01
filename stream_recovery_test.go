package a13n

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func deltaAt(run string, attempt, sequence int, cursor string) string {
	return fmt.Sprintf("id: %s\nevent: delta\ndata: {\"run_id\":%q,\"attempt\":%d,\"sequence\":%d,\"event\":{\"type\":\"CUSTOM\",\"name\":\"a13n.test\",\"value\":null},\"item\":null}\n\n", cursor, run, attempt, sequence)
}

func TestGapPositionNativeNullableAndCanonical(t *testing.T) {
	for _, field := range []string{"", `,"position":null`, `,"position":"1-12"`, `,"position":"99999999999999999999-99999999999999999999"`} {
		frame, err := parseThreadFrame("gap", "", false, []byte(`{"run_id":"run"`+field+`}`))
		if err != nil {
			t.Fatal(err)
		}
		gap := frame.(GapFrame)
		if strings.Contains(field, `"1-12"`) && (gap.Position == nil || *gap.Position != "1-12") {
			t.Fatal(gap)
		}
		if (field == "" || strings.Contains(field, "null")) && gap.Position != nil {
			t.Fatal(gap)
		}
	}
	for _, position := range []string{`"01-1"`, `"1-01"`, `"-1-2"`, `"1-"`, `"100000000000000000000-1"`, `12`, `{}`} {
		_, err := parseThreadFrame("gap", "", false, []byte(`{"run_id":"run","position":`+position+`}`))
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted %s: %v", position, err)
		}
	}
}

func TestScopedStreamReconnectClaimsOnlyContiguousAppliedOutput(t *testing.T) {
	var requests []string
	client := coverageClient(t, func(req *http.Request) *http.Response {
		requests = append(requests, req.URL.Query().Encode()+" after="+req.Header.Get("Last-Event-ID"))
		body := deltaAt("other", 9, 1, "100-1") + deltaAt("run", 1, 3, "100-2") +
			"event: gap\ndata: {\"run_id\":\"run\",\"position\":\"1-5\"}\n\n" +
			deltaAt("run", 1, 6, "100-3") +
			"id: 100-4\nevent: boundary\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":6}\n\n"
		if len(requests) > 1 {
			body = gapEvent
		}
		response := coverageResponse(200, body)
		response.Header.Set("Content-Type", "text/event-stream")
		return response
	})
	stream, err := client.Thread("thr").events(context.Background(), StreamOptions{RunID: "run", Position: "1-2", After: "99-1", MaxReconnects: 1, ReconnectDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for range 5 {
		if _, err := stream.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if stream.AppliedPosition() != "1-3" || stream.AppliedCursor() != "100-2" || stream.LastReceivedCursor() != "100-4" {
		t.Fatal(stream.AppliedPosition(), stream.AppliedCursor(), stream.LastReceivedCursor())
	}
	frame, err := stream.Next()
	if err != nil || frame.EventType() != "gap" || len(requests) != 2 {
		t.Fatal(frame, err, requests)
	}
	if requests[0] != "position=1-2&run=run after=99-1" || requests[1] != "position=1-3&run=run after=100-2" {
		t.Fatal(requests)
	}
}

func TestScopedCoverageFreezesUntilExplicitSnapshotReopen(t *testing.T) {
	parse := func(text string) ThreadFrame {
		p := sseParser{reader: bufio.NewReader(strings.NewReader(text)), limit: 1 << 20, first: true}
		frame, err := p.next()
		if err != nil {
			t.Fatal(err)
		}
		return frame
	}
	for name, signal := range map[string]string{
		"reset":         "event: reset\ndata: {\"run_id\":\"run\"}\n\n",
		"unknown_gap":   gapEvent,
		"known_gap":     "event: gap\ndata: {\"run_id\":\"run\",\"position\":\"1-4\"}\n\n",
		"new_attempt":   deltaAt("run", 2, 1, "12-1"),
		"delta_hole":    deltaAt("run", 1, 4, "11-4"),
		"boundary_hole": "id: 11-4\nevent: boundary\ndata: {\"run_id\":\"run\",\"attempt\":1,\"sequence\":4}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			stream := &ThreadStream{options: StreamOptions{RunID: "run"}, position: "1-2", applied: "10-2"}
			stream.acknowledge(parse(signal))
			stream.acknowledge(parse(deltaAt("run", 1, 3, "11-3")))
			stream.acknowledge(parse(deltaAt("run", 2, 1, "12-1")))
			if !stream.frozen || stream.position != "1-2" || stream.applied != "10-2" {
				t.Fatal("output resumed without applying a display baseline", stream)
			}
		})
	}
	// Applying a new snapshot is explicit: a new reader starts from that
	// snapshot's position, not a reset-derived empty position or Redis hint.
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.URL.Query().Get("run") != "run" || req.URL.Query().Get("position") != "2-4" {
			t.Fatal(req.URL)
		}
		response := coverageResponse(200, deltaAt("run", 2, 5, "12-5")+gapEvent)
		response.Header.Set("Content-Type", "text/event-stream")
		return response
	})
	stream, err := client.Thread("thr").events(context.Background(), StreamOptions{RunID: "run", Position: "2-4"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	if stream.AppliedPosition() != "2-4" {
		t.Fatal("unapplied frame advanced coverage")
	}
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	if stream.AppliedPosition() != "2-5" || stream.AppliedCursor() != "12-5" {
		t.Fatal("explicit snapshot reader did not advance its contiguous tail")
	}
}

func TestCoverageOptionsValidatePairButDoNotInferFromHint(t *testing.T) {
	calls := 0
	client := coverageClient(t, func(req *http.Request) *http.Response {
		calls++
		response := coverageResponse(200, gapEvent)
		response.Header.Set("Content-Type", "text/event-stream")
		return response
	})
	for _, opts := range []StreamOptions{{RunID: "run"}, {Position: "1-2"}, {RunID: "run", Position: "01-2"}} {
		if _, err := client.Thread("thr").events(context.Background(), opts); err == nil {
			t.Fatal("invalid coverage accepted")
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
	for _, after := range []string{"", "999-999"} {
		stream, err := client.Thread("thr").events(context.Background(), StreamOptions{RunID: "run", Position: "1-2", After: after})
		if err != nil {
			t.Fatal(err)
		}
		if stream.AppliedPosition() != "1-2" {
			t.Fatal("position derived from hint")
		}
		_ = stream.Close()
	}
}
