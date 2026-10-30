package eval

import (
	"context"
	"testing"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/agent"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm/llmtest"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/seed"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/store"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/tools"
)

func TestRunScoresAgainstGroundTruth(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if _, err := seed.Load(ctx, st, seed.Config{Seed: 42, Now: now}); err != nil {
		t.Fatal(err)
	}
	incidents, err := st.Incidents(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// The fake model searches the real seeded data once, then reports the
	// correct category for every question except the opt-out one.
	answers := map[string]string{}
	for _, inc := range incidents {
		answers[inc.Question] = inc.Category
	}
	optOut := incidents[1]
	answers[optOut.Question] = "invalid_number"

	fake := &llmtest.Fake{Respond: func(question string, turn int, in llm.Input) (llm.Turn, error) {
		if turn == 0 {
			return llm.Turn{ToolCalls: []llm.ToolCall{{Name: "search_logs", Args: map[string]any{"status": "failed"}}}}, nil
		}
		if _, isErr := in.ToolResults[0].Output["error"]; isErr {
			t.Errorf("search_logs failed: %v", in.ToolResults[0].Output)
		}
		return llm.Turn{ToolCalls: []llm.ToolCall{{Name: "report_diagnosis", Args: map[string]any{
			"summary":             "test",
			"root_cause_category": answers[question],
			"likely_cause":        "test",
			"next_steps":          []any{"x"},
			"evidence":            []any{"y"},
		}}}}, nil
	}}

	a := &agent.Agent{Model: fake, Tools: tools.New(st), Now: now}
	var progress int
	rep, err := Run(ctx, a, incidents, 0, func(Result) { progress++ })
	if err != nil {
		t.Fatal(err)
	}

	if rep.Correct != 5 || len(rep.Results) != 6 || progress != 6 {
		t.Fatalf("correct=%d results=%d progress=%d, want 5/6/6", rep.Correct, len(rep.Results), progress)
	}
	for _, r := range rep.Results {
		if wantPass := r.Incident.ID != optOut.ID; r.Correct != wantPass {
			t.Errorf("%s: correct=%v, want %v", r.Incident.ID, r.Correct, wantPass)
		}
	}
	if got := rep.Score(); got < 0.83 || got > 0.84 {
		t.Errorf("score = %.3f, want 5/6", got)
	}
}

func TestRunCountsErrorsAsWrong(t *testing.T) {
	fake := llmtest.Script() // no turns: every question errors
	a := &agent.Agent{Model: fake, Tools: emptyTools{}}
	rep, err := Run(context.Background(), a, []store.Incident{{ID: "x", Category: "opt_out", Question: "q"}}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Correct != 0 || rep.Results[0].Err == nil {
		t.Errorf("want one failed result with an error, got %+v", rep.Results[0])
	}
}

func TestEmptyReportScoresZero(t *testing.T) {
	if (Report{}).Score() != 0 {
		t.Error("empty report should score 0")
	}
}

type emptyTools struct{}

func (emptyTools) Specs() []llm.ToolSpec                             { return nil }
func (emptyTools) Call(context.Context, llm.ToolCall) map[string]any { return nil }
