package a13n_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/converge-ai-labs/a13n-sdk-go/generated"
)

func TestScopedPasswordResetAndTraceFilters(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1/organizations/org%20%2F1/members/user%20%2F2/password-reset" {
				t.Errorf("reset: %s %s", r.Method, r.URL.EscapedPath())
			}
			_, _ = w.Write([]byte(`{"email_queued":false,"reset_url":null}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/traces/search" {
			t.Errorf("search: %s %s", r.Method, r.URL.Path)
		}
		for key, want := range map[string]string{"min_duration_seconds": "0", "max_duration_seconds": "10.25", "min_cost_usd": "0.000000000000001", "max_cost_usd": "1"} {
			if calls == 2 {
				if got := r.URL.Query().Get(key); got != want {
					t.Errorf("%s = %q; want %q", key, got, want)
				}
			} else if r.URL.Query().Has(key) {
				t.Errorf("unexpected %s", key)
			}
		}
		_, _ = w.Write([]byte(`{"items":[],"next_cursor":null,"total":0,"collected_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	client, err := generated.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	reset, err := client.ResetMemberPasswordWithResponse(context.Background(), "org /1", "user /2")
	if err != nil || reset.JSON200 == nil || reset.JSON200.EmailQueued {
		t.Fatalf("reset: %#v %v", reset, err)
	}
	minDuration, maxDuration, minCost, maxCost := "0", "10.25", "0.000000000000001", "1"
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := client.SearchTracesWithResponse(context.Background(), &generated.SearchTracesParams{
		StartedAfter: start, StartedBefore: start.Add(time.Hour), MinDurationSeconds: &minDuration,
		MaxDurationSeconds: &maxDuration, MinCostUsd: &minCost, MaxCostUsd: &maxCost,
	})
	if err != nil || result.JSON200 == nil || result.JSON200.Total != 0 {
		t.Fatalf("search: %#v %v", result, err)
	}
	_, err = client.SearchTracesWithResponse(context.Background(), &generated.SearchTracesParams{StartedAfter: start, StartedBefore: start.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("got %d requests", calls)
	}
}
