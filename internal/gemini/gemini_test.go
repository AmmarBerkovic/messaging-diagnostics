package gemini

import (
	"testing"

	"google.golang.org/genai"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm"
)

// These tests cover the conversion code only; they make no network calls.

func TestDeclarations(t *testing.T) {
	decls := declarations([]llm.ToolSpec{{
		Name:        "report",
		Description: "d",
		Params: []llm.Param{
			{Name: "category", Type: "string", Enum: []string{"a", "b"}, Required: true},
			{Name: "steps", Type: "array"},
		},
	}})
	if len(decls) != 1 || decls[0].Name != "report" {
		t.Fatalf("unexpected declarations: %+v", decls)
	}
	p := decls[0].Parameters
	if p.Type != genai.TypeObject || len(p.Required) != 1 || p.Required[0] != "category" {
		t.Errorf("unexpected schema: %+v", p)
	}
	if got := p.Properties["category"]; got.Type != genai.TypeString || len(got.Enum) != 2 {
		t.Errorf("category schema: %+v", got)
	}
	if got := p.Properties["steps"]; got.Type != genai.TypeArray || got.Items.Type != genai.TypeString {
		t.Errorf("steps schema: %+v", got)
	}
}

func TestToTurn(t *testing.T) {
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{Text: "private reasoning", Thought: true},
			{Text: "Checking logs."},
			{FunctionCall: &genai.FunctionCall{ID: "1", Name: "search_logs", Args: map[string]any{"country": "GB"}}},
		},
	}}}}
	turn, err := toTurn(resp)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != "Checking logs." {
		t.Errorf("text = %q, thought parts must be dropped", turn.Text)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Name != "search_logs" || turn.ToolCalls[0].Args["country"] != "GB" {
		t.Errorf("tool calls = %+v", turn.ToolCalls)
	}

	if _, err := toTurn(&genai.GenerateContentResponse{}); err == nil {
		t.Error("want an error for a response without candidates")
	}
}
