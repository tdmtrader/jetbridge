package main

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

func buildAppRegistry() brine.StepRegistry {
	return brine.NewStepRegistry(steps.Definitions())
}

// buildAppResources freezes the step package's resource plan with every
// disposer wrapped, so a teardown that fails is recorded rather than dropped
// into the report the pipeline discards.
func buildAppResources() (*brine.ResourceRegistry, error) {
	declared := steps.ResourceDefinitions()
	watched := make([]brine.ResourceDefinition, len(declared))
	for i, definition := range declared {
		watched[i] = definition
		watched[i].Factory = refusingFactory(definition.Name, definition.Factory)
		if definition.Disposer != nil {
			watched[i].Disposer = recordingDisposer(definition.Name, definition.Disposer)
		}
	}
	return brine.NewResourceRegistry(watched)
}

// refusingFactory wraps a resource factory so that nothing is created once
// the process is leaving. ResourceState.Require recreates any resource not in
// its instance map, and DisposeAll empties that map: a scenario boundary
// reached while the signal goroutine is disposing would otherwise start a
// fresh postmaster or control plane for the os.Exit behind it to orphan.
func refusingFactory(name string, factory brine.ResourceFactory) brine.ResourceFactory {
	return func(deps map[string]any) (any, error) {
		if steps.Leaving() {
			return nil, fmt.Errorf("%s: process is exiting; refusing to create %s", adapterName, name)
		}
		return factory(deps)
	}
}
