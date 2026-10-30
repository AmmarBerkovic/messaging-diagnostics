// Package agent runs the tool-calling loop: send the question, execute the
// tools the model asks for, send the results back, and repeat until the
// model reports a diagnosis or the step limit is reached.
package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/kb"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm"
)

// DefaultMaxSteps bounds the number of model turns per question. Each turn
// is one API request, which matters on a free-tier quota.
const DefaultMaxSteps = 8

// ErrMaxSteps is returned when the model keeps calling tools without
// reporting a diagnosis.
var ErrMaxSteps = errors.New("agent: step limit reached without a diagnosis")

// Executor runs tools. *tools.Registry implements it; tests may stub it.
type Executor interface {
	Specs() []llm.ToolSpec
	Call(ctx context.Context, call llm.ToolCall) map[string]any
}

// Diagnosis is the structured answer the model reports.
type Diagnosis struct {
	Summary     string
	RootCause   string // one of kb.RootCauses, or empty if the model answered in free text
	LikelyCause string
	NextSteps   []string
	Evidence    []string
	Steps       int // model turns used
}

// Agent answers delivery questions with a model and a set of tools.
type Agent struct {
	Model    llm.Model
	Tools    Executor
	Now      time.Time // the dataset's current time, given to the model
	MaxSteps int       // zero means DefaultMaxSteps

	// OnToolCall, if set, is called before each tool runs so the CLI can
	// show the model's reasoning as it happens.
	OnToolCall func(llm.ToolCall)
}

// Ask diagnoses one question.
func (a *Agent) Ask(ctx context.Context, question string) (Diagnosis, error) {
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}

	specs := append(a.Tools.Specs(), reportSpec)
	session, err := a.Model.StartSession(ctx, systemPrompt(a.Now), specs)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("start session: %w", err)
	}

	in := llm.Input{Text: question}
	for step := 1; step <= maxSteps; step++ {
		turn, err := session.Send(ctx, in)
		if err != nil {
			return Diagnosis{}, fmt.Errorf("model turn %d: %w", step, err)
		}

		// No tool calls means the model answered directly. Keep the text,
		// but there is no structured root cause to score.
		if len(turn.ToolCalls) == 0 {
			if strings.TrimSpace(turn.Text) == "" {
				return Diagnosis{}, fmt.Errorf("model turn %d: empty response", step)
			}
			return Diagnosis{Summary: turn.Text, Steps: step}, nil
		}

		var results []llm.ToolResult
		for _, call := range turn.ToolCalls {
			if a.OnToolCall != nil {
				a.OnToolCall(call)
			}
			if call.Name == reportToolName {
				d, err := parseReport(call.Args)
				if err == nil {
					d.Steps = step
					return d, nil
				}
				// Let the model fix a malformed report on the next turn.
				results = append(results, llm.ToolResult{CallID: call.ID, Name: call.Name,
					Output: map[string]any{"error": err.Error()}})
				continue
			}
			results = append(results, llm.ToolResult{CallID: call.ID, Name: call.Name,
				Output: a.Tools.Call(ctx, call)})
		}
		in = llm.Input{ToolResults: results}
	}
	return Diagnosis{}, ErrMaxSteps
}

const reportToolName = "report_diagnosis"

// reportSpec is the tool the model calls to finish. Asking for the answer as
// tool arguments gives us structured output, including a root cause chosen
// from a fixed enum, without a separate JSON-mode request.
var reportSpec = llm.ToolSpec{
	Name: reportToolName,
	Description: "Report the final diagnosis. Call this exactly once, after gathering evidence, " +
		"and do not call any other tool in the same turn.",
	Params: []llm.Param{
		{Name: "summary", Type: "string", Required: true,
			Description: "Two or three sentences: what is failing, since when, how much, citing numbers from the tools."},
		{Name: "root_cause_category", Type: "string", Required: true, Enum: kb.RootCauses,
			Description: "The single category that best explains the problem."},
		{Name: "likely_cause", Type: "string", Required: true,
			Description: "The specific cause, e.g. which sender, org, domain or credential is involved."},
		{Name: "next_steps", Type: "array", Required: true,
			Description: "Concrete actions for the operator, most important first."},
		{Name: "evidence", Type: "array", Required: true,
			Description: "Sample message ids and documentation links that support the diagnosis."},
	},
}

func parseReport(args map[string]any) (Diagnosis, error) {
	d := Diagnosis{
		Summary:     str(args["summary"]),
		RootCause:   str(args["root_cause_category"]),
		LikelyCause: str(args["likely_cause"]),
		NextSteps:   strList(args["next_steps"]),
		Evidence:    strList(args["evidence"]),
	}
	if d.Summary == "" {
		return Diagnosis{}, errors.New("summary is required")
	}
	if !slices.Contains(kb.RootCauses, d.RootCause) {
		return Diagnosis{}, fmt.Errorf("root_cause_category %q is not one of %v", d.RootCause, kb.RootCauses)
	}
	return d, nil
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// strList accepts a JSON array of strings (decoded as []any) or a single string.
func strList(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s := str(x); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t = strings.TrimSpace(t); t != "" {
			return []string{t}
		}
	}
	return nil
}

func systemPrompt(now time.Time) string {
	return `You are a messaging delivery diagnostics assistant for an SMS and email platform.
Operators ask why messages are failing. Answer only from the tools; never invent numbers, ids or error meanings.

The current time is ` + now.UTC().Format(time.RFC3339) + ` (UTC). Resolve phrases like "yesterday" or
"the last few hours" against this time and pass RFC 3339 UTC timestamps to search_logs.

How to work:
1. Start with a broad search_logs call that matches the question, then narrow it using the breakdowns
   (by error code, sender, org, country, recipient domain, hour) to find where failures concentrate.
2. Compare against a baseline, such as an earlier time range or other senders, to separate the incident
   from normal background failures of a few percent.
3. Call lookup_error_code for the dominant error code before explaining it.
4. Use get_message when the question names a message id.
5. Finish by calling report_diagnosis once. Cite sample message ids and the documentation link in evidence.
Keep tool calls few; each one costs quota.`
}
