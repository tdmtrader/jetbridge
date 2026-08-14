package exec_test

import (
	"context"

	"github.com/concourse/concourse/atc/exec"
)

type stepFunc func(context.Context, exec.RunState) (bool, error)

func (f stepFunc) Run(ctx context.Context, state exec.RunState) (bool, error) {
	return f(ctx, state)
}
