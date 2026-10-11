package a13n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicMultipartOptionsAndOptionalKeys(t *testing.T) {
	var keys []string
	var bodies []map[string]json.RawMessage
	client := coverageClient(t, func(req *http.Request) *http.Response {
		if req.Method != "POST" {
			t.Fatalf("constructor did I/O: %s", req.URL.Path)
		}
		keys = append(keys, req.Header.Get("Idempotency-Key"))
		var body map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		return coverageResponse(201, queuedReceipt)
	})
	agent := client.Agent("agent")
	for j := 0; j < 2; j++ {
		i, err := agent.Start(context.Background(), "hello")
		if err != nil {
			t.Fatal(err)
		}
		_ = i.Close()
		if i.Receipt().IdempotencyKey != keys[j] || len(keys[j]) != 32 {
			t.Fatal("generated key missing from receipt", i.Receipt())
		}
	}
	if keys[0] == keys[1] {
		t.Fatal("separate submissions reused a key")
	}
	options := StartOptions{IdempotencyKey: "caller-key", AgentRevisionId: Null[string](), SessionId: Value("session"),
		Delivery: Ptr(Delivery("next_run")), Kind: Ptr(NewThreadKind("message")),
		Environments: Ptr(InitialMounts{}), Memories: Ptr([]MemoryMount{}), McpHeaders: Ptr(McpHeaders{}), MessageHistory: Ptr(MessageHistory{}),
		Options: &RunOptions{Configuration: Value(Configuration{AllowedHosts: Value([]string{}), Extensions: Ptr(map[string]any{"false": false, "zero": 0, "empty": "", "null": nil, "array": []any{}})}),
			Overrides: Value(Overrides{ModelSettings: Value(map[string]any{"extra_body": map[string]any{}})}), Labels: Ptr(map[string]string{}), MaxUsage: Null[UsageLimit]()}}
	input := Input{Content: []Part{TextPart(""), JSONPart(map[string]any{"n": json.Number("9007199254740993")}), URLPart("https://example.invalid/a"), AssetPart("asset"), TextPart("tail")}}
	i, err := agent.StartInput(context.Background(), input, options)
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if keys[2] != "caller-key" || i.Receipt().IdempotencyKey != "caller-key" {
		t.Fatal("caller key replaced")
	}
	body := bodies[2]
	if string(body["payload"]) != `{"content":[{"text":"","type":"text"},{"type":"json","value":{"n":9007199254740993}},{"type":"url","url":"https://example.invalid/a"},{"asset_id":"asset","type":"asset"},{"text":"tail","type":"text"}]}` {
		t.Fatal(string(body["payload"]))
	}
	for _, name := range []string{"environments", "memories", "message_history"} {
		if string(body[name]) != "[]" {
			t.Fatalf("empty %s: %s", name, body[name])
		}
	}
	if string(body["agent_revision_id"]) != "null" || string(body["mcp_headers"]) != "{}" {
		t.Fatal(body)
	}
	var actual RunOptions
	_ = json.Unmarshal(body["options"], &actual)
	if !actual.MaxUsage.IsNull() || actual.Configuration.GetOrEmpty().AllowedHosts.IsNull() {
		t.Fatal(actual)
	}
	if _, err := agent.Start(context.Background(), "bad", StartOptions{}, StartOptions{}); err == nil || len(keys) != 3 {
		t.Fatal("multiple options dispatched", err)
	}
	if _, err := agent.StartInput(context.Background(), Input{Content: []Part{JSONPart(make(chan int))}}); err == nil || len(keys) != 3 {
		t.Fatal("invalid JSON dispatched", err)
	}
}

func TestRecoveredEntryAndExactRunShareLazyObserver(t *testing.T) {
	var reads atomic.Int32
	client := coverageClient(t, func(req *http.Request) *http.Response {
		reads.Add(1)
		if req.Method != "GET" {
			t.Fatal("recovery mutated")
		}
		if strings.Contains(req.URL.Path, "/inbox/") {
			return coverageResponse(200, entryView("consumed", `"exact"`))
		}
		if req.URL.Path != "/proxy/api/v1/runs/exact" {
			t.Fatal("followed other Run", req.URL.Path)
		}
		return coverageResponse(200, `{"id":"exact","thread_id":"thr","status":"completed","output":"","pending":null,"failure":null}`)
	})
	ctx := context.Background()
	recovered, err := client.Entry("thr", "ent").Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	exact, err := client.Run("exact").Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer exact.Close()
	if reads.Load() != 0 {
		t.Fatal("observe constructor did I/O")
	}
	if recovered.ReceiptEvidence().Submission != nil || exact.ReceiptEvidence().Resume != nil || recovered.Receipt().Origin != "recovered_entry" || exact.Entry() != nil {
		t.Fatal("fabricated receipt/Entry")
	}
	if _, bound := recovered.Run(); bound {
		t.Fatal("Entry recovery bound before consumption")
	}
	for _, i := range []*Interaction{recovered, exact} {
		result, err := i.Result(ctx)
		if err != nil || result.Status != RunCompleted || result.RunID != "exact" || result.ThreadID != "thr" || result.Pending != nil || result.Failure != nil {
			t.Fatal(result, err)
		}
		text, present := result.Text()
		if !present || text != "" {
			t.Fatal("empty output text absent")
		}
		again, err := i.Result(ctx)
		if err != nil || again.Evidence().RequestID() != result.Evidence().RequestID() {
			t.Fatal(again, err)
		}
		if i.Thread().ID != "thr" {
			t.Fatal("exact observer did not resolve Thread")
		}
	}
	if reads.Load() != 3 {
		t.Fatal("duplicate polling kernel", reads.Load())
	}
}

func TestOutcomePreservesNumbersAndFalsyOutput(t *testing.T) {
	for _, output := range []string{`{"integer":9007199254740993,"decimal":1.234567890123456789,"enabled":false,"empty":[],"null":null}`, `false`, `0`, `null`, `[]`, `{}`, `""`} {
		t.Run(output, func(t *testing.T) {
			client := coverageClient(t, func(req *http.Request) *http.Response {
				return coverageResponse(200, `{"id":"run","thread_id":"thr","status":"completed","output":`+output+`}`)
			})
			result, err := client.Run("run").Result(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result.Output)
			if err != nil {
				t.Fatal(err)
			}
			var wanted, actual any
			d := json.NewDecoder(strings.NewReader(output))
			d.UseNumber()
			_ = d.Decode(&wanted)
			d = json.NewDecoder(strings.NewReader(string(encoded)))
			d.UseNumber()
			_ = d.Decode(&actual)
			if !reflect.DeepEqual(wanted, actual) {
				t.Fatal("output precision/value changed", string(encoded))
			}
			if strings.HasPrefix(output, `{"integer"`) {
				typed, err := DecodeOutput[struct {
					Integer int64
					Decimal json.Number
				}](result)
				if err != nil || typed.Integer != 9007199254740993 || typed.Decimal.String() != "1.234567890123456789" {
					t.Fatal(typed, err)
				}
			}
		})
	}
}

func TestUnknownOutcomeKeepsKeyAndNativeCauseWithoutRetry(t *testing.T) {
	for _, mode := range []string{"transport", "body", "malformed", "identity", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			client, err := NewClient("https://service.test", NewSecret("secret"), roundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if mode == "transport" {
					return nil, errors.New("secret transport cause")
				}
				if mode == "rejected" {
					return coverageResponse(409, `{"error":{"code":"conflict","message":"rejected","details":{},"request_id":null}}`), nil
				}
				if mode == "body" {
					response := coverageResponse(201, "")
					response.Body = &errorBody{}
					return response, nil
				}
				if mode == "identity" {
					return coverageResponse(201, `{"thread":{"id":"other"},"entry":{"id":"entry","thread_id":"wrong"},"run":null}`), nil
				}
				return coverageResponse(201, `{"thread":`), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Agent("agent").Send(context.Background(), "thread", "hi", SendOptions{IdempotencyKey: "persisted-key"})
			if calls != 1 {
				t.Fatal("automatic retry", calls)
			}
			var unknown *UnknownOutcomeError
			if mode == "rejected" {
				var rejection *ApiError
				if !errors.As(err, &rejection) || errors.As(err, &unknown) {
					t.Fatal(err)
				}
				return
			}
			if !errors.As(err, &unknown) || unknown.IdempotencyKey != "persisted-key" || unknown.ThreadID != "thread" || unknown.Operation != "send" || unknown.Cause == nil || strings.Contains(fmt.Sprintf("%v %#v", err, err), "secret") {
				t.Fatal(err)
			}
			if (mode == "body" || mode == "transport") && !errors.Is(err, ErrTransport) {
				t.Fatal("transport cause lost", err)
			}
		})
	}
	client := coverageClient(t, func(req *http.Request) *http.Response { t.Fatal("predispatch failure dispatched"); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Agent("agent").Start(ctx, "hi")
	var unknown *UnknownOutcomeError
	if !errors.Is(err, context.Canceled) || errors.As(err, &unknown) {
		t.Fatal(err)
	}
}

type errorBody struct{}

func (*errorBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (*errorBody) Close() error             { return nil }

func TestResumeAnswersAndSharedBudget(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "POST" {
			posts.Add(1)
			var body map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if string(body["approvals"]) != `{"approve":{"action":"approve"},"deny":{"action":"deny","reason":null}}` || string(body["calls"]) != `{"failed":{"message":"failed","status":"failed"},"returned":{"status":"returned","value":null}}` || string(body["input"]) != "null" {
				t.Error("answer translation", body)
			}
			time.Sleep(70 * time.Millisecond)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, runView("successor", "accepted", "thr"))
			return
		}
		<-req.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, NewSecret("key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	i, err := client.Run("waiting").Resume(ctx, Answers{"approve": Approve(), "deny": Deny(Null[string]()), "failed": Failed("failed"), "returned": Returned(nil)}, ResumeOptions{Input: Null[Input]()})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	if i.Entry() != nil || i.Receipt().Origin != "resumed_run" || i.Receipt().IdempotencyKey == "" {
		t.Fatal(i.Receipt())
	}
	_, err = i.Result(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || posts.Load() != 1 {
		t.Fatal("resume budget reset", err)
	}
}

func TestUnacknowledgedGatewayFailureKeepsGeneratedKey(t *testing.T) {
	for _, mode := range []string{"html", "malformed", "lost_body", "api_rejection"} {
		t.Run(mode, func(t *testing.T) {
			var key string
			var calls int
			client := coverageClient(t, func(req *http.Request) *http.Response {
				calls++
				key = req.Header.Get("Idempotency-Key")
				response := coverageResponse(504, "<html>gateway timeout</html>")
				response.Header.Set("X-Request-ID", "gateway-request")
				switch mode {
				case "malformed":
					response.Body = io.NopCloser(strings.NewReader(`{"error":`))
				case "lost_body":
					response.Body = &errorBody{}
				case "api_rejection":
					response.Body = io.NopCloser(strings.NewReader(`{"error":{"code":"unavailable","message":"rejected","details":{},"request_id":null}}`))
				}
				return response
			})
			_, err := client.Agent("agent").Start(context.Background(), "hi")
			var unknown *UnknownOutcomeError
			if mode == "api_rejection" {
				var rejection *ApiError
				if !errors.As(err, &rejection) || errors.As(err, &unknown) {
					t.Fatal("known Service rejection misclassified", err)
				}
			} else if !errors.As(err, &unknown) || key == "" || unknown.IdempotencyKey != key || unknown.StatusCode != 504 || unknown.Header.Get("X-Request-ID") != "gateway-request" || unknown.Cause == nil {
				t.Fatal("unacknowledged mutation lost evidence", err)
			}
			if calls != 1 {
				t.Fatal("gateway failure replayed", calls)
			}
		})
	}
}

func TestInvalidHTTPHeaderIsDefinitelyPreDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, key, token, csrf string
		invalid                bool
	}{
		{name: "key_newline", key: "bad\nkey", token: "secret", invalid: true},
		{name: "key_nul", key: "bad\x00key", token: "secret", invalid: true},
		{name: "key_delete", key: "bad\x7fkey", token: "secret", invalid: true},
		{name: "bearer", token: "bad\rsecret", invalid: true},
		{name: "session_csrf", csrf: "bad\ncsrf", invalid: true},
		{name: "allowed_tab", key: "key\tvalue", token: "secret"},
		{name: "allowed_obs_text", key: "key\x80value", token: "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("local dial failure")
			}}
			var options []ClientOption
			if tc.csrf != "" {
				jar, err := cookiejar.New(nil)
				if err != nil {
					t.Fatal(err)
				}
				options = append(options, WithSession(jar, func() string { return tc.csrf }))
			}
			client, err := NewClient("http://service.test", NewSecret(tc.token), transport, options...)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Agent("agent").Start(context.Background(), "hi", StartOptions{IdempotencyKey: tc.key})
			var unknown *UnknownOutcomeError
			if tc.invalid {
				if err == nil || errors.As(err, &unknown) || dials.Load() != 0 || strings.Contains(err.Error(), "bad") {
					t.Fatal("local invalid header reported as dispatch or leaked value", err, dials.Load())
				}
			} else if !errors.As(err, &unknown) || dials.Load() != 1 {
				t.Fatal("valid header rejected before transport", err, dials.Load())
			}
		})
	}
}

func TestHugeOutputPreservesTypedRunAndSubmissionEvidence(t *testing.T) {
	huge := "1" + strings.Repeat("0", 309)
	for _, output := range []string{huge, `{"exact":9007199254740993,"huge":` + huge + `}`} {
		t.Run(output[:12], func(t *testing.T) {
			runJSON := `{"id":"run","thread_id":"thr","status":"completed","output":` + output + `}`
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if req.Method == "POST" {
					if strings.HasSuffix(req.URL.Path, "/resume") {
						return coverageResponse(201, runJSON)
					}
					return coverageResponse(201, `{"thread":{"id":"thr"},"entry":{"id":"ent","thread_id":"thr","status":"consumed","assigned_run_id":"run"},"run":`+runJSON+`}`)
				}
				if strings.Contains(req.URL.Path, "/inbox/") {
					return coverageResponse(200, entryView("consumed", `"run"`))
				}
				return coverageResponse(200, runJSON)
			})
			checkOutput := func(value any) {
				t.Helper()
				encoded, err := json.Marshal(value)
				if err != nil || string(encoded) != output {
					t.Fatal("number changed in typed evidence", string(encoded), err)
				}
			}
			ctx := context.Background()
			snapshot, err := client.Run("run").Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			checkOutput(snapshot.Value.Output.GetOrEmpty())
			if string(snapshot.RawJSON()) != runJSON || snapshot.StatusCode != 200 {
				t.Fatal("HTTP evidence changed")
			}
			started, err := client.Agent("agent").Start(ctx, "hi")
			if err != nil {
				t.Fatal(err)
			}
			defer started.Close()
			checkOutput(started.ReceiptEvidence().Submission.Value.Run.GetOrEmpty().Output.GetOrEmpty())
			resumed, err := client.Run("waiting").Resume(ctx, Answers{})
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			checkOutput(resumed.ReceiptEvidence().Resume.Value.Output.GetOrEmpty())
			for _, interaction := range []*Interaction{started, resumed} {
				result, err := interaction.Result(ctx)
				if err != nil {
					t.Fatal(err)
				}
				checkOutput(result.Output)
				checkOutput(result.Evidence().Value.Output.GetOrEmpty())
				if output == huge {
					number, err := DecodeOutput[json.Number](result)
					if err != nil || number.String() != huge {
						t.Fatal(number, err)
					}
					integer, err := DecodeOutput[*big.Int](result)
					if err != nil || integer.String() != huge {
						t.Fatal(integer, err)
					}
				} else {
					value, err := DecodeOutput[struct {
						Huge  json.Number
						Exact int64
					}](result)
					if err != nil || value.Huge.String() != huge || value.Exact != 9007199254740993 {
						t.Fatal(value, err)
					}
				}
			}
		})
	}
}

func TestCanonicalServiceErrorEnvelopeControlsUnknownOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		rejection  bool
	}{
		{"canonical_null_id", `{"error":{"code":"conflict","message":"rejected","details":{},"request_id":null}}`, true},
		{"canonical_string_id", `{"error":{"code":"conflict","message":"","details":{"n":9007199254740993},"request_id":"body-request"}}`, true},
		{"code_only_gateway", `{"error":{"code":"gateway_timeout"}}`, false},
		{"missing_code", `{"error":{"message":"rejected","details":{},"request_id":null}}`, false},
		{"missing_message", `{"error":{"code":"conflict","details":{},"request_id":null}}`, false},
		{"missing_details", `{"error":{"code":"conflict","message":"rejected","request_id":null}}`, false},
		{"missing_request_id", `{"error":{"code":"conflict","message":"rejected","details":{}}}`, false},
		{"null_code", `{"error":{"code":null,"message":"rejected","details":{},"request_id":null}}`, false},
		{"null_message", `{"error":{"code":"conflict","message":null,"details":{},"request_id":null}}`, false},
		{"null_details", `{"error":{"code":"conflict","message":"rejected","details":null,"request_id":null}}`, false},
		{"wrong_code", `{"error":{"code":42,"message":"rejected","details":{},"request_id":null}}`, false},
		{"wrong_message", `{"error":{"code":"conflict","message":[],"details":{},"request_id":null}}`, false},
		{"wrong_details", `{"error":{"code":"conflict","message":"rejected","details":[],"request_id":null}}`, false},
		{"wrong_request_id", `{"error":{"code":"conflict","message":"rejected","details":{},"request_id":42}}`, false},
		{"null_error", `{"error":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts int
			var key string
			client := coverageClient(t, func(req *http.Request) *http.Response {
				if req.Method == "POST" {
					posts++
					key = req.Header.Get("Idempotency-Key")
				}
				response := coverageResponse(504, tc.body)
				response.Header.Set("X-Request-ID", "header-request")
				response.Header.Set("Retry-After", "2")
				return response
			})
			_, lowError := healthCheck(client)
			_, mutationError := client.Agent("agent").Start(context.Background(), "hi")
			var lowAPI, api *ApiError
			var unknown *UnknownOutcomeError
			if tc.rejection {
				if !errors.As(lowError, &lowAPI) || !errors.As(mutationError, &api) || errors.As(mutationError, &unknown) || api.Status != 504 || api.RetryAfter != "2" || api.Details == nil {
					t.Fatal("canonical Service rejection lost", lowError, mutationError)
				}
				wantedID := "header-request"
				if tc.name == "canonical_string_id" {
					wantedID = "body-request"
				}
				if api.RequestID != wantedID {
					t.Fatal(api.RequestID)
				}
			} else {
				if !errors.Is(lowError, ErrProtocol) || errors.As(lowError, &lowAPI) || !errors.As(mutationError, &unknown) || !errors.Is(mutationError, ErrProtocol) || errors.As(mutationError, &api) || unknown.IdempotencyKey != key || key == "" || unknown.StatusCode != 504 || unknown.Header.Get("X-Request-ID") != "header-request" {
					t.Fatal("noncanonical error body claimed known rejection", lowError, mutationError)
				}
			}
			if posts != 1 {
				t.Fatal("mutation replayed", posts)
			}
		})
	}
}
