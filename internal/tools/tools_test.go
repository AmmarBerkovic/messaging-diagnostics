package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/seed"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/store"
)

var testNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// newRegistry seeds an in-memory database and returns tools backed by it.
func newRegistry(t *testing.T) (*Registry, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := seed.Load(context.Background(), st, seed.Config{Seed: 42, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	return New(st), st
}

func call(t *testing.T, r *Registry, name string, args map[string]any) map[string]any {
	t.Helper()
	out := r.Call(context.Background(), llm.ToolCall{Name: name, Args: args})
	if e, ok := out["error"]; ok {
		t.Fatalf("%s returned error: %v", name, e)
	}
	return out
}

// topError returns the most frequent error code in a search_logs result.
func topError(t *testing.T, out map[string]any) string {
	t.Helper()
	codes, _ := out["top_error_codes"].([]any)
	if len(codes) == 0 {
		t.Fatal("no error codes in result")
	}
	return codes[0].(map[string]any)["error_code"].(string)
}

func TestSearchLogsFindsEachPlantedIncident(t *testing.T) {
	r, _ := newRegistry(t)
	tests := []struct {
		name     string
		args     map[string]any
		wantCode string
	}{
		{"carrier filtering", map[string]any{"channel": "sms", "country": "gb", "since": "2026-10-28T14:00:00Z"}, "30007"},
		{"opt out", map[string]any{"org_id": "org_cedar", "status": "failed"}, "21610"},
		{"invalid numbers", map[string]any{"org_id": "org_driftwood", "since": "2026-10-28T06:00:00Z", "until": "2026-10-28T07:00:00Z"}, "40310"},
		{"rate limit", map[string]any{"org_id": "org_acme", "provider": "telnyx", "since": "2026-10-29T04:00:00Z", "until": "2026-10-29T04:20:00Z"}, "10011"},
		{"dead domain", map[string]any{"org_id": "org_evergreen", "channel": "email", "since": "2026-10-28T00:00:00Z"}, "5.1.2"},
		{"expired credentials", map[string]any{"org_id": "org_foxglove", "since": "2026-10-29T07:00:00Z", "channel": "sms"}, "20003"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := call(t, r, "search_logs", tt.args)
			if got := topError(t, out); got != tt.wantCode {
				t.Errorf("top error code = %s, want %s", got, tt.wantCode)
			}
		})
	}
}

func TestSearchLogsAggregatesNotRows(t *testing.T) {
	r, _ := newRegistry(t)
	out := call(t, r, "search_logs", map[string]any{"channel": "sms", "country": "GB", "since": "2026-10-28T14:00:00Z"})

	total := out["total"].(float64)
	if total < 100 {
		t.Fatalf("total = %v, want a meaningful sample", total)
	}
	if rate := out["failure_rate_pct"].(float64); rate < 40 {
		t.Errorf("failure rate %.1f%%, want the incident to dominate", rate)
	}
	if n := len(out["sample_messages"].([]any)); n > 5 {
		t.Errorf("returned %d samples, want at most 5", n)
	}
	senders := out["by_sender"].([]any)
	if top := senders[0].(map[string]any)["key"]; top != "+447700900500" {
		t.Errorf("top failing sender = %v, want the filtered sender", top)
	}
}

func TestSearchLogsBaselineIsHealthy(t *testing.T) {
	r, _ := newRegistry(t)
	// Before the carrier filtering started, UK SMS were mostly delivered.
	out := call(t, r, "search_logs", map[string]any{"channel": "sms", "country": "GB",
		"since": "2026-10-27T00:00:00Z", "until": "2026-10-28T00:00:00Z", "org_id": "org_brightpath"})
	if rate := out["failure_rate_pct"].(float64); rate > 15 {
		t.Errorf("baseline failure rate %.1f%%, want low", rate)
	}
}

func TestSearchLogsRejectsBadTime(t *testing.T) {
	r, _ := newRegistry(t)
	out := r.Call(context.Background(), llm.ToolCall{Name: "search_logs", Args: map[string]any{"since": "yesterday"}})
	if msg, _ := out["error"].(string); !strings.Contains(msg, "RFC 3339") {
		t.Errorf("want an RFC 3339 error, got %v", out)
	}
}

func TestSearchLogsNoMatches(t *testing.T) {
	r, _ := newRegistry(t)
	out := call(t, r, "search_logs", map[string]any{"org_id": "org_missing"})
	if out["total"].(float64) != 0 {
		t.Errorf("want zero total, got %v", out["total"])
	}
}

func TestGetMessage(t *testing.T) {
	r, st := newRegistry(t)
	sum, err := st.Search(context.Background(), store.Filter{ErrorCode: "30007"})
	if err != nil || len(sum.Samples) == 0 {
		t.Fatalf("need a sample message: %v", err)
	}
	id := sum.Samples[0].ID

	out := call(t, r, "get_message", map[string]any{"message_id": id})
	if out["found"] != true || out["error_code"] != "30007" || out["message_id"] != id {
		t.Errorf("unexpected result: %v", out)
	}

	out = call(t, r, "get_message", map[string]any{"message_id": "msg_nope"})
	if out["found"] != false {
		t.Errorf("want found=false, got %v", out)
	}
}

func TestLookupErrorCode(t *testing.T) {
	r, _ := newRegistry(t)

	out := call(t, r, "lookup_error_code", map[string]any{"provider": "Twilio", "code": "30007"})
	if out["found"] != true || out["category"] != "carrier_filtering" {
		t.Errorf("unexpected result: %v", out)
	}
	if url, _ := out["doc_url"].(string); !strings.HasPrefix(url, "https://www.twilio.com/") {
		t.Errorf("doc_url = %q", url)
	}

	// Models sometimes send codes as numbers.
	out = call(t, r, "lookup_error_code", map[string]any{"provider": "twilio", "code": float64(21610)})
	if out["category"] != "opt_out" {
		t.Errorf("numeric code lookup failed: %v", out)
	}

	out = call(t, r, "lookup_error_code", map[string]any{"provider": "twilio", "code": "99999"})
	if out["found"] != false {
		t.Errorf("want found=false, got %v", out)
	}
}

func TestUnknownTool(t *testing.T) {
	r, _ := newRegistry(t)
	out := r.Call(context.Background(), llm.ToolCall{Name: "drop_tables"})
	if _, ok := out["error"]; !ok {
		t.Errorf("want error for unknown tool, got %v", out)
	}
}
