package main

import (
	"context"
	"fmt"
)

// StepPods is the sweeper's view of the step pods on this node;
// outputplane.NodePods is the in-cluster one.
type StepPods interface {
	// Live returns the handles of this node's step pods that have not
	// terminated: a container of each may still read its inputs or write its
	// outputs under steps/<handle>.
	Live(ctx context.Context) (map[string]struct{}, error)
}

// ProbeStepPods lists the view once so a daemon that cannot read its node's
// step pods says so at startup instead of sparing every step directory forever.
func ProbeStepPods(ctx context.Context, pods StepPods) error {
	if _, err := pods.Live(ctx); err != nil {
		return fmt.Errorf("the sweeper cannot read this node's step pods: %w", err)
	}
	return nil
}
