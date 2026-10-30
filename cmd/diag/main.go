// Command diag is the Messaging Diagnostics Assistant CLI.
//
//	diag seed                  generate synthetic delivery logs
//	diag ask "<question>"      diagnose a delivery problem with Gemini
//	diag eval                  score the assistant on the planted incidents
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/agent"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/eval"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/gemini"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/seed"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/store"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/tools"
)

const usage = `Messaging Diagnostics Assistant (all data is synthetic)

Usage:
  diag seed [--db diag.db] [--seed 42] [--now 2026-10-29T12:00:00Z] [--baseline 3000]
  diag ask  [--db diag.db] [--model NAME] [--max-steps 8] "<question>"
  diag eval [--db diag.db] [--model NAME] [--max-steps 8] [--delay 15s]

Environment:
  GEMINI_API_KEY   required for ask and eval (free key: https://aistudio.google.com/apikey)
  GEMINI_MODEL     model name, default ` + gemini.DefaultModel + `
  DIAG_DB          database path, default diag.db
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	switch args[0] {
	case "seed":
		return runSeed(ctx, args[1:], out)
	case "ask":
		return runAsk(ctx, args[1:], out)
	case "eval":
		return runEval(ctx, args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func runSeed(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("DIAG_DB", "diag.db"), "SQLite database path")
	seedVal := fs.Uint64("seed", 42, "random seed; the same seed and --now give identical data")
	nowStr := fs.String("now", "", "dataset's current time, RFC 3339 (default: now, rounded down to the hour)")
	baseline := fs.Int("baseline", 3000, "number of normal background messages")
	if err := fs.Parse(args); err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Hour)
	if *nowStr != "" {
		t, err := time.Parse(time.RFC3339, *nowStr)
		if err != nil {
			return fmt.Errorf("--now: %w", err)
		}
		now = t
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	n, err := seed.Load(ctx, st, seed.Config{Seed: *seedVal, Now: now, Baseline: *baseline})
	if err != nil {
		return err
	}
	incidents, err := st.Incidents(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %d synthetic messages to %s (dataset time %s)\n", n, *dbPath, now.Format(time.RFC3339))
	fmt.Fprintf(out, "Planted %d incidents:\n", len(incidents))
	for _, inc := range incidents {
		fmt.Fprintf(out, "  %-26s %s\n", inc.ID, inc.Question)
	}
	return nil
}

// common holds the flags shared by ask and eval.
type common struct {
	dbPath   *string
	model    *string
	maxSteps *int
}

func commonFlags(fs *flag.FlagSet) common {
	return common{
		dbPath:   fs.String("db", envOr("DIAG_DB", "diag.db"), "SQLite database path"),
		model:    fs.String("model", envOr("GEMINI_MODEL", gemini.DefaultModel), "Gemini model name"),
		maxSteps: fs.Int("max-steps", agent.DefaultMaxSteps, "maximum model turns per question"),
	}
}

// newAgent opens the database and wires the agent to Gemini. The caller
// must close the returned store.
func (c common) newAgent(ctx context.Context, out io.Writer) (*agent.Agent, *store.Store, error) {
	st, err := store.Open(*c.dbPath)
	if err != nil {
		return nil, nil, err
	}
	now, err := st.DatasetTime(ctx)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	model, err := gemini.New(ctx, os.Getenv("GEMINI_API_KEY"), *c.model)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	a := &agent.Agent{
		Model:      model,
		Tools:      tools.New(st),
		Now:        now,
		MaxSteps:   *c.maxSteps,
		OnToolCall: func(call llm.ToolCall) { fmt.Fprintln(out, formatCall(call)) },
	}
	return a, st, nil
}

func runAsk(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	c := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		return errors.New(`ask needs a question, e.g. diag ask "Why are UK SMS failing since yesterday?"`)
	}

	a, st, err := c.newAgent(ctx, out)
	if err != nil {
		return err
	}
	defer st.Close()

	d, err := a.Ask(ctx, question)
	if err != nil {
		return err
	}
	printDiagnosis(out, d)
	return nil
}

func runEval(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	c := commonFlags(fs)
	delay := fs.Duration("delay", 15*time.Second, "pause between questions, to respect free-tier rate limits")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, st, err := c.newAgent(ctx, out)
	if err != nil {
		return err
	}
	defer st.Close()

	incidents, err := st.Incidents(ctx)
	if err != nil {
		return err
	}
	rep, err := eval.Run(ctx, a, incidents, *delay, func(r eval.Result) {
		printResult(out, r)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nScore: %d/%d (%.0f%%)\n", rep.Correct, len(rep.Results), rep.Score()*100)
	return nil
}

// formatCall renders a tool call as "  → name key=value ..." with keys sorted
// so the output is stable.
func formatCall(call llm.ToolCall) string {
	keys := make([]string, 0, len(call.Args))
	for k := range call.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("  → " + call.Name)
	if call.Name == "report_diagnosis" {
		return b.String() // its arguments are printed as the diagnosis
	}
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, call.Args[k])
	}
	return b.String()
}

func printDiagnosis(out io.Writer, d agent.Diagnosis) {
	fmt.Fprintln(out, "\nDiagnosis")
	fmt.Fprintln(out, d.Summary)
	if d.RootCause == "" {
		fmt.Fprintln(out, "\n(The model answered in free text without a structured report.)")
		return
	}
	fmt.Fprintf(out, "\nLikely cause [%s]\n%s\n", d.RootCause, d.LikelyCause)
	if len(d.NextSteps) > 0 {
		fmt.Fprintln(out, "\nSuggested next steps")
		for i, s := range d.NextSteps {
			fmt.Fprintf(out, "  %d. %s\n", i+1, s)
		}
	}
	if len(d.Evidence) > 0 {
		fmt.Fprintln(out, "\nEvidence")
		for _, e := range d.Evidence {
			fmt.Fprintf(out, "  - %s\n", e)
		}
	}
}

func printResult(out io.Writer, r eval.Result) {
	mark := "PASS"
	if !r.Correct {
		mark = "FAIL"
	}
	got := r.Diagnosis.RootCause
	if r.Err != nil {
		got = "error: " + r.Err.Error()
	} else if got == "" {
		got = "(no structured answer)"
	}
	fmt.Fprintf(out, "%s %-26s expected=%s got=%s\n\n", mark, r.Incident.ID, r.Incident.Category, got)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
