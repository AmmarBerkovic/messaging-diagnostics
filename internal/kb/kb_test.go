package kb

import (
	"slices"
	"strings"
	"testing"
)

func TestEntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range All() {
		key := e.Provider + "/" + e.Code
		if seen[key] {
			t.Errorf("duplicate entry %s", key)
		}
		seen[key] = true
		if e.Title == "" || e.Summary == "" {
			t.Errorf("%s: missing title or summary", key)
		}
		if !strings.HasPrefix(e.DocURL, "https://") {
			t.Errorf("%s: doc url %q is not https", key, e.DocURL)
		}
		if !slices.Contains(RootCauses, e.Category) {
			t.Errorf("%s: category %q is not a known root cause", key, e.Category)
		}
	}
	if len(seen) < 20 {
		t.Errorf("knowledge base has %d entries, want about 20", len(seen))
	}
}

func TestLookup(t *testing.T) {
	if e, ok := Lookup(" TWILIO ", "30007"); !ok || e.Category != CauseCarrierFiltering {
		t.Errorf("Lookup(twilio, 30007) = %+v, %v", e, ok)
	}
	if _, ok := Lookup("telnyx", "30007"); ok {
		t.Error("codes must be matched per provider")
	}
}
