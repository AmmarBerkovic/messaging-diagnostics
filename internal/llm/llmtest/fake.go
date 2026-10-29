// Package llmtest provides a scripted fake llm.Model for tests.
package llmtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/ammarberkovic/messaging-diagnostics/internal/llm"
)

// RespondFunc decides the fake model's reply. question is the first user
// message of the session and turn counts from 0.
type RespondFunc func(question string, turn int, in llm.Input) (llm.Turn, error)

// Fake is an llm.Model whose replies come from a function instead of a
// network call. It records what it was sent so tests can assert on it.
type Fake struct {
	Respond RespondFunc

	mu     sync.Mutex
	System string
	Tools  []llm.ToolSpec
	Inputs [][]llm.Input // one slice per session
}

// Script returns a Fake that replays the same turns, in order, in every session.
func Script(turns ...llm.Turn) *Fake {
	return &Fake{Respond: func(_ string, turn int, _ llm.Input) (llm.Turn, error) {
		if turn >= len(turns) {
			return llm.Turn{}, fmt.Errorf("llmtest: script has %d turns, asked for turn %d", len(turns), turn+1)
		}
		return turns[turn], nil
	}}
}

// StartSession implements llm.Model.
func (f *Fake) StartSession(_ context.Context, system string, tools []llm.ToolSpec) (llm.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.System, f.Tools = system, tools
	f.Inputs = append(f.Inputs, nil)
	return &session{fake: f, index: len(f.Inputs) - 1}, nil
}

type session struct {
	fake     *Fake
	index    int
	question string
	turn     int
}

func (s *session) Send(_ context.Context, in llm.Input) (llm.Turn, error) {
	s.fake.mu.Lock()
	s.fake.Inputs[s.index] = append(s.fake.Inputs[s.index], in)
	s.fake.mu.Unlock()

	if s.turn == 0 {
		s.question = in.Text
	}
	t, err := s.fake.Respond(s.question, s.turn, in)
	s.turn++
	return t, err
}
