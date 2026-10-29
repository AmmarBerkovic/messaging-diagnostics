package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ammarberkovic/messaging-diagnostics/internal/llm"
)

func TestFormatCall(t *testing.T) {
	got := formatCall(llm.ToolCall{Name: "search_logs", Args: map[string]any{"since": "2026-10-28T14:00:00Z", "country": "GB", "channel": "sms"}})
	want := "  → search_logs channel=sms country=GB since=2026-10-28T14:00:00Z"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestSeedCommand(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"seed", "--db", db, "--now", "2026-10-29T12:00:00Z"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Planted 6 incidents") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestAskWithoutKeyFailsClearly(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()
	if err := run(ctx, []string{"seed", "--db", db}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "")
	err := run(ctx, []string{"ask", "--db", db, "why?"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("err = %v, want a message about GEMINI_API_KEY", err)
	}
}
