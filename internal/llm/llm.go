// Package llm defines the small interface the agent needs from a language
// model. The Gemini adapter implements it for real runs; tests use a
// scripted fake, so nothing outside the adapter depends on a vendor SDK.
package llm

import "context"

// Model starts conversations. One Session is one question being diagnosed.
type Model interface {
	StartSession(ctx context.Context, systemPrompt string, tools []ToolSpec) (Session, error)
}

// Session is a multi-turn conversation. The implementation owns the history,
// which lets the Gemini adapter replay provider-specific data (such as
// thought signatures) without the agent knowing about it.
type Session interface {
	Send(ctx context.Context, in Input) (Turn, error)
}

// Input is what we send on one turn: the user's question on the first turn,
// tool results on every turn after that.
type Input struct {
	Text        string
	ToolResults []ToolResult
}

// Turn is the model's reply: either tool calls to execute, or final text.
type Turn struct {
	Text      string
	ToolCalls []ToolCall
}

// ToolCall is a request from the model to run one tool.
type ToolCall struct {
	ID   string // may be empty; echoed back in the result when set
	Name string
	Args map[string]any
}

// ToolResult is the output of one tool call, sent back to the model.
type ToolResult struct {
	CallID string
	Name   string
	Output map[string]any
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Name        string
	Description string
	Params      []Param
}

// Param is one argument of a tool. Every argument is a top-level field of a
// JSON object, which is all these tools need.
type Param struct {
	Name        string
	Type        string // "string" or "array" (of strings)
	Description string
	Enum        []string
	Required    bool
}
