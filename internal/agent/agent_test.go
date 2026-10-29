package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ammarberkovic/messaging-diagnostics/internal/llm"
	"github.com/ammarberkovic/messaging-diagnostics/internal/llm/llmtest"
)

// stubTools records calls and returns a fixed result per tool name.
type stubTools struct {
	calls []llm.ToolCall
}

func (s *stubTools) Specs() []llm.ToolSpec {
	return []llm.ToolSpec{{Name: "search_logs"}, {Name: "lookup_error_code"}}
}

func (s *stubTools) Call(_ context.Context, call llm.ToolCall) map[string]any {
	s.calls = append(s.calls, call)
	return map[string]any{"tool": call.Name, "ok": true}
}

func toolCall(id, name string, args map[string]any) llm.Turn {
	return llm.Turn{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
}

var validReport = map[string]any{
	"summary":             "75% of UK SMS since 14:00 failed with 30007.",
	"root_cause_category": "carrier_filtering",
	"likely_cause":        "Carrier filtering on sender +447700900500.",
	"next_steps":          []any{"Review message content", "Check sender registration"},
	"evidence":            []any{"msg_000001", "https://www.twilio.com/docs/api/errors/30007"},
}

func newAgent(m llm.Model, tools Executor) *Agent {
	return &Agent{Model: m, Tools: tools, Now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
}

func TestAskRunsToolsAndReturnsReport(t *testing.T) {
	fake := llmtest.Script(
		toolCall("c1", "search_logs", map[string]any{"country": "GB"}),
		toolCall("c2", "lookup_error_code", map[string]any{"provider": "twilio", "code": "30007"}),
		toolCall("c3", "report_diagnosis", validReport),
	)
	tools := &stubTools{}
	a := newAgent(fake, tools)
	var seen []string
	a.OnToolCall = func(c llm.ToolCall) { seen = append(seen, c.Name) }

	d, err := a.Ask(context.Background(), "Why are UK SMS failing?")
	if err != nil {
		t.Fatal(err)
	}

	if d.RootCause != "carrier_filtering" || d.Steps != 3 || len(d.NextSteps) != 2 || len(d.Evidence) != 2 {
		t.Errorf("unexpected diagnosis: %+v", d)
	}
	if got := strings.Join(seen, ","); got != "search_logs,lookup_error_code,report_diagnosis" {
		t.Errorf("OnToolCall saw %s", got)
	}
	if len(tools.calls) != 2 {
		t.Errorf("executor ran %d tools, want 2 (report is handled by the agent)", len(tools.calls))
	}

	// The question goes first; each later turn carries the previous tool result.
	inputs := fake.Inputs[0]
	if inputs[0].Text != "Why are UK SMS failing?" {
		t.Errorf("first input = %+v", inputs[0])
	}
	r := inputs[1].ToolResults[0]
	if r.CallID != "c1" || r.Name != "search_logs" || r.Output["tool"] != "search_logs" {
		t.Errorf("tool result not passed back correctly: %+v", r)
	}

	// The model is told the dataset time and offered the report tool.
	if !strings.Contains(fake.System, "2026-10-29T12:00:00Z") {
		t.Error("system prompt does not contain the dataset time")
	}
	if last := fake.Tools[len(fake.Tools)-1]; last.Name != "report_diagnosis" {
		t.Errorf("last tool = %s, want report_diagnosis", last.Name)
	}
}

func TestAskStopsAtMaxSteps(t *testing.T) {
	// A model that never stops calling tools.
	fake := &llmtest.Fake{Respond: func(_ string, turn int, _ llm.Input) (llm.Turn, error) {
		return toolCall("", "search_logs", map[string]any{"turn": turn}), nil
	}}
	tools := &stubTools{}
	a := newAgent(fake, tools)
	a.MaxSteps = 3

	_, err := a.Ask(context.Background(), "loop forever")
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if len(fake.Inputs[0]) != 3 || len(tools.calls) != 3 {
		t.Errorf("model turns = %d, tool calls = %d, want 3 each", len(fake.Inputs[0]), len(tools.calls))
	}
}

func TestAskDefaultMaxSteps(t *testing.T) {
	fake := &llmtest.Fake{Respond: func(string, int, llm.Input) (llm.Turn, error) {
		return toolCall("", "search_logs", nil), nil
	}}
	_, err := newAgent(fake, &stubTools{}).Ask(context.Background(), "q")
	if !errors.Is(err, ErrMaxSteps) || len(fake.Inputs[0]) != DefaultMaxSteps {
		t.Errorf("err = %v after %d turns, want ErrMaxSteps after %d", err, len(fake.Inputs[0]), DefaultMaxSteps)
	}
}

func TestAskRejectsInvalidReportThenAcceptsFix(t *testing.T) {
	bad := map[string]any{"summary": "something", "root_cause_category": "aliens"}
	fake := llmtest.Script(
		toolCall("r1", "report_diagnosis", bad),
		toolCall("r2", "report_diagnosis", validReport),
	)
	d, err := newAgent(fake, &stubTools{}).Ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if d.RootCause != "carrier_filtering" || d.Steps != 2 {
		t.Errorf("unexpected diagnosis: %+v", d)
	}
	feedback := fake.Inputs[0][1].ToolResults[0].Output["error"]
	if msg, _ := feedback.(string); !strings.Contains(msg, "aliens") {
		t.Errorf("model was not told why the report was rejected: %v", feedback)
	}
}

func TestAskAcceptsFreeTextAnswer(t *testing.T) {
	fake := llmtest.Script(llm.Turn{Text: "Everything looks normal."})
	d, err := newAgent(fake, &stubTools{}).Ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary != "Everything looks normal." || d.RootCause != "" {
		t.Errorf("unexpected diagnosis: %+v", d)
	}
}

func TestAskPropagatesModelError(t *testing.T) {
	fake := &llmtest.Fake{Respond: func(string, int, llm.Input) (llm.Turn, error) {
		return llm.Turn{}, errors.New("quota exceeded")
	}}
	_, err := newAgent(fake, &stubTools{}).Ask(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("err = %v", err)
	}
}

func TestAskEmptyResponse(t *testing.T) {
	fake := llmtest.Script(llm.Turn{})
	if _, err := newAgent(fake, &stubTools{}).Ask(context.Background(), "q"); err == nil {
		t.Error("want an error for an empty model response")
	}
}
