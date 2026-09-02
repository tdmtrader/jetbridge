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
	var allowedResources map[string]bool
	if len(features) == 1 {
		switch filepath.Base(features[0]) {
		case "db-job-final-strict.feature", "migration-build-events-bigint-strict.feature", "migration-add-global-resource-versions-strict.feature":
			allowedResources = map[string]bool{"postgres": true}
		case "db-team-remaining-strict.feature":
			allowedResources = map[string]bool{"postgres": true, "jetbridge-db": true}
		}
	}
	if allowedResources != nil {
		selected := definitions[:0]
		for _, definition := range definitions {
			if allowedResources[definition.Name] {
				selected = append(selected, definition)
			}
		}
		definitions = selected
	}
	return brine.NewResourceRegistry(definitions)
}
