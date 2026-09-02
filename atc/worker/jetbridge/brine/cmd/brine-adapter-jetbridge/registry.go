package main

import (
	"path/filepath"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

func buildAppRegistry() brine.StepRegistry {
	return brine.NewStepRegistry(steps.Definitions())
}

func buildAppResources(args []string) (*brine.ResourceRegistry, error) {
	definitions := steps.ResourceDefinitions()
	features, _, _ := parseRunFlags(args)
	if len(features) == 1 && filepath.Base(features[0]) == "db-team-remaining-strict.feature" {
		productionDBOnly := make([]brine.ResourceDefinition, 0, 2)
		for _, definition := range definitions {
			if definition.Name == "postgres" || definition.Name == "jetbridge-db" {
				productionDBOnly = append(productionDBOnly, definition)
			}
		}
		definitions = productionDBOnly
	}
	return brine.NewResourceRegistry(definitions)
}
