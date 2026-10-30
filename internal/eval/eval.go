// Package eval scores the agent against the incidents planted by the seeder.
// Each incident has a question and a known root cause category; a diagnosis
// is correct when the model reports that same category.
package eval

import (
	"context"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/agent"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/store"
)

// Result is the outcome for one incident.
type Result struct {
	Incident  store.Incident
	Diagnosis agent.Diagnosis
	Err       error
	Correct   bool
}

// Report is the outcome for the whole run.
type Report struct {
	Results []Result
	Correct int
}

// Score returns the share of correct answers, from 0 to 1.
func (r Report) Score() float64 {
	if len(r.Results) == 0 {
		return 0
	}
	return float64(r.Correct) / float64(len(r.Results))
}

// Run asks the agent one question per incident. delay is waited between
// questions to stay under free-tier requests-per-minute limits. onResult,
// if set, is called after each question so progress can be printed.
func Run(ctx context.Context, a *agent.Agent, incidents []store.Incident, delay time.Duration, onResult func(Result)) (Report, error) {
	var rep Report
	for i, inc := range incidents {
		if i > 0 && delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return rep, ctx.Err()
			}
		}
		d, err := a.Ask(ctx, inc.Question)
		res := Result{Incident: inc, Diagnosis: d, Err: err}
		res.Correct = err == nil && d.RootCause == inc.Category
		if res.Correct {
			rep.Correct++
		}
		rep.Results = append(rep.Results, res)
		if onResult != nil {
			onResult(res)
		}
	}
	return rep, nil
}
