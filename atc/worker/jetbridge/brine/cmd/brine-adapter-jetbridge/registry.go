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
	postgresOnlyFeature := false
	if len(features) == 1 {
		switch filepath.Base(features[0]) {
		case "db-job-final-strict.feature", "migration-build-events-bigint-strict.feature":
			postgresOnlyFeature = true
		}
	}
	if postgresOnlyFeature {
		postgresOnly := definitions[:0]
		for _, definition := range definitions {
			if definition.Name == "postgres" {
				postgresOnly = append(postgresOnly, definition)
			}
		}
		definitions = postgresOnly
	}
	return brine.NewResourceRegistry(definitions)
}
