package exec_test

import (
	"context"
	"sync"

	"github.com/concourse/concourse/atc/exec"
)

// scriptedStep is a Step whose behavior the spec writes itself. Steps that run
// substeps concurrently call Run from several goroutines at once, so the record
// of those calls is guarded.
type scriptedStep struct {
	RunStub func(context.Context, exec.RunState) (bool, error)

	mu   sync.Mutex
	runs []scriptedStepRun
}

type scriptedStepRun struct {
	ctx   context.Context
	state exec.RunState
}

func (s *scriptedStep) Run(ctx context.Context, state exec.RunState) (bool, error) {
	s.mu.Lock()
	s.runs = append(s.runs, scriptedStepRun{ctx: ctx, state: state})
	s.mu.Unlock()

	if s.RunStub == nil {
		return false, nil
	}
	return s.RunStub(ctx, state)
}

func (s *scriptedStep) RunReturns(ok bool, err error) {
	s.RunStub = func(context.Context, exec.RunState) (bool, error) {
		return ok, err
	}
}

func (s *scriptedStep) RunCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *scriptedStep) RunArgsForCall(i int) (context.Context, exec.RunState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[i].ctx, s.runs[i].state
}
