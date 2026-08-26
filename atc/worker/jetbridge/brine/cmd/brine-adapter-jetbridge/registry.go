package main

import (
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

func buildAppRegistry() brine.StepRegistry {
	defs := steps.Definitions()
	defs = append(defs, steps.IntegrationDefinitions()...)
	return brine.NewStepRegistry(defs)
}

func buildAppResources() (*brine.ResourceRegistry, error) {
	return brine.NewResourceRegistry(steps.ResourceDefinitions())
}
