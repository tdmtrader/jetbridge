package main

import (
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// A document has already resolved selection; inspect its sources, never read or
// filter feature files again. Unknown paths conservatively select the local tier.
func planNeedsRealCluster(plan brine.ExecutionPlan) bool {
	for _, feature := range plan.Features {
		source := filepath.ToSlash(filepath.Clean(feature.Source))
		if !strings.HasPrefix(source, "features/live/") && !strings.Contains(source, "/features/live/") {
			return true
		}
	}
	return false
}

func prepareRun(plan brine.ExecutionPlan, state *brine.ResourceState, start func(brine.Resources) error) error {
	// Acquire the sweep first so it stays last in LIFO disposal, even when the
	// control plane is acquired before RunPlan. Acquisition does no network I/O.
	if err := state.Require("live-namespace-sweep"); err != nil {
		return err
	}
	if !planNeedsRealCluster(plan) {
		return nil
	}
	if err := state.Require("real-cluster"); err != nil {
		return err
	}
	return start(state)
}
