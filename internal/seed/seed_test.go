package seed

import (
	"reflect"
	"testing"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/kb"
)

var testNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func TestGenerateIsDeterministic(t *testing.T) {
	cfg := Config{Seed: 7, Now: testNow}
	a, incA := Generate(cfg)
	b, incB := Generate(cfg)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(incA, incB) {
		t.Fatal("same config produced different data")
	}
	c, _ := Generate(Config{Seed: 8, Now: testNow})
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds produced identical data")
	}
}

func TestGenerateShape(t *testing.T) {
	msgs, incidents := Generate(Config{Seed: 42, Now: testNow})

	if len(msgs) < 3000 {
		t.Errorf("got %d messages, want at least 3000", len(msgs))
	}
	if len(incidents) != 6 {
		t.Errorf("got %d incidents, want 6", len(incidents))
	}

	ids := map[string]bool{}
	for _, m := range msgs {
		if ids[m.ID] {
			t.Fatalf("duplicate id %s", m.ID)
		}
		ids[m.ID] = true
		if m.Timestamp >= "2026-10-29T12:00:00Z" || m.Timestamp < "2026-10-26T12:00:00Z" {
			t.Fatalf("%s timestamp %s outside the 72h window", m.ID, m.Timestamp)
		}
		if m.Channel == "email" && m.RecipientDomain == "" {
			t.Fatalf("%s: email without recipient domain", m.ID)
		}
		if m.ErrorCode != "" {
			if _, ok := kb.Lookup(m.Provider, m.ErrorCode); !ok {
				t.Fatalf("%s uses %s/%s, which is not in the knowledge base", m.ID, m.Provider, m.ErrorCode)
			}
		}
	}
	for _, inc := range incidents {
		if inc.Question == "" || inc.Category == "" {
			t.Errorf("incident %s is missing a question or category", inc.ID)
		}
	}
}
