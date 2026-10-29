// Package gemini adapts the official Google Gen AI SDK to the llm.Model
// interface. It is the only package that imports the SDK.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/ammarberkovic/messaging-diagnostics/internal/llm"
)

// DefaultModel is a stable Flash model that the Gemini API offers on its
// free tier. Override it with --model or GEMINI_MODEL.
const DefaultModel = "gemini-3.8-flash"

// Model talks to the Gemini API.
type Model struct {
	client *genai.Client
	name   string
	// MaxRetries is how many times a rate-limited (HTTP 429) request is
	// retried, with exponential backoff starting at RetryDelay.
	MaxRetries int
	RetryDelay time.Duration
}

// New creates a client for the Gemini API using an API key.
func New(ctx context.Context, apiKey, modelName string) (*Model, error) {
	if apiKey == "" {
		return nil, errors.New("GEMINI_API_KEY is not set (get a free key at https://aistudio.google.com/apikey)")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &Model{client: client, name: modelName, MaxRetries: 3, RetryDelay: 10 * time.Second}, nil
}

// StartSession implements llm.Model. The SDK's Chat keeps the history,
// including the model's own turns verbatim, which preserves the thought
// signatures Gemini requires when function calling across turns.
func (m *Model) StartSession(ctx context.Context, systemPrompt string, tools []llm.ToolSpec) (llm.Session, error) {
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		Tools:             []*genai.Tool{{FunctionDeclarations: declarations(tools)}},
	}
	chat, err := m.client.Chats.Create(ctx, m.name, cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("create chat: %w", err)
	}
	return &session{model: m, chat: chat}, nil
}

type session struct {
	model *Model
	chat  *genai.Chat
}

// Send implements llm.Session.
func (s *session) Send(ctx context.Context, in llm.Input) (llm.Turn, error) {
	var parts []*genai.Part
	if in.Text != "" {
		parts = append(parts, genai.NewPartFromText(in.Text))
	}
	for _, r := range in.ToolResults {
		parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:       r.CallID,
			Name:     r.Name,
			Response: r.Output,
		}})
	}

	resp, err := s.sendWithRetry(ctx, parts)
	if err != nil {
		return llm.Turn{}, err
	}
	return toTurn(resp)
}

func (s *session) sendWithRetry(ctx context.Context, parts []*genai.Part) (*genai.GenerateContentResponse, error) {
	delay := s.model.RetryDelay
	for attempt := 0; ; attempt++ {
		resp, err := s.chat.Send(ctx, parts...)
		var apiErr genai.APIError
		if err == nil || !errors.As(err, &apiErr) || apiErr.Code != 429 || attempt >= s.model.MaxRetries {
			return resp, err
		}
		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// toTurn extracts text and function calls from the first candidate. It reads
// the parts directly instead of using resp.Text(), which logs a warning
// whenever a response also contains function calls.
func toTurn(resp *genai.GenerateContentResponse) (llm.Turn, error) {
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		reason := "no candidates"
		if resp.PromptFeedback != nil {
			reason = fmt.Sprintf("prompt blocked: %s", resp.PromptFeedback.BlockReason)
		}
		return llm.Turn{}, fmt.Errorf("gemini returned no content (%s)", reason)
	}
	var turn llm.Turn
	var text strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		switch {
		case p.FunctionCall != nil:
			turn.ToolCalls = append(turn.ToolCalls, llm.ToolCall{
				ID:   p.FunctionCall.ID,
				Name: p.FunctionCall.Name,
				Args: p.FunctionCall.Args,
			})
		case p.Text != "" && !p.Thought:
			text.WriteString(p.Text)
		}
	}
	turn.Text = text.String()
	return turn, nil
}

// declarations converts our tool specs to Gemini function declarations.
func declarations(tools []llm.ToolSpec) []*genai.FunctionDeclaration {
	out := make([]*genai.FunctionDeclaration, len(tools))
	for i, t := range tools {
		schema := &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}}
		for _, p := range t.Params {
			ps := &genai.Schema{Type: genai.TypeString, Description: p.Description, Enum: p.Enum}
			if p.Type == "array" {
				ps = &genai.Schema{Type: genai.TypeArray, Description: p.Description,
					Items: &genai.Schema{Type: genai.TypeString}}
			}
			schema.Properties[p.Name] = ps
			if p.Required {
				schema.Required = append(schema.Required, p.Name)
			}
		}
		out[i] = &genai.FunctionDeclaration{Name: t.Name, Description: t.Description, Parameters: schema}
	}
	return out
}
