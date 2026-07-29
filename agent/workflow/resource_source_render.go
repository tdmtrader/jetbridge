package workflow

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/mitchellh/copystructure"
)

const resourceSourcePipelineHashDomain = "workflow-resource-source-pipeline/v1\x00"

// ResourceSourcePipelineTarget is the immutable authority for one standing
// resource selection pipeline.
type ResourceSourcePipelineTarget struct {
	TeamID               int
	WorkflowDefinitionID int
	WorkflowName         string
	WorkflowVersion      int
	Sources              []ResourceSource
	Resources            atc.ResourceConfigs
	ResourceTypes        atc.ResourceTypes
}

type RenderedResourceSourcePipeline struct {
	PipelineName  string
	Config        atc.Config
	CanonicalJSON []byte
	ConfigHash    string
}

func ResourceSourcePipelineTargetFor(definition Definition, teamID int) (ResourceSourcePipelineTarget, error) {
	if teamID <= 0 {
		return ResourceSourcePipelineTarget{}, fmt.Errorf("workflow: source pipeline team ID must be positive")
	}
	function, signature, err := cloneValidatedDefinitionFunction(definition)
	if err != nil {
		return ResourceSourcePipelineTarget{}, err
	}
	if len(function.ResourceSources) == 0 {
		return ResourceSourcePipelineTarget{}, fmt.Errorf("workflow: source pipeline requires resource sources")
	}
	if err := validateRenderableFunction(function, signature, definition.ID); err != nil {
		return ResourceSourcePipelineTarget{}, err
	}
	types, err := ResourceSourceResourceTypes(function.ResourceSources, function.Resources, function.ResourceTypes)
	if err != nil {
		return ResourceSourcePipelineTarget{}, err
	}
	return ResourceSourcePipelineTarget{TeamID: teamID, WorkflowDefinitionID: definition.ID, WorkflowName: definition.Name, WorkflowVersion: definition.Version, Sources: function.ResourceSources, Resources: function.Resources, ResourceTypes: types}, nil
}

// RenderResourceSourcePipeline produces exactly one non-template `admit` job
// using ordinary get selection semantics.
func RenderResourceSourcePipeline(target ResourceSourcePipelineTarget) (RenderedResourceSourcePipeline, error) {
	cloned, err := cloneResourceSourcePipelineTarget(target)
	if err != nil {
		return RenderedResourceSourcePipeline{}, fmt.Errorf("workflow: clone source pipeline target: %w", err)
	}
	if err := validateResourceSourcePipelineTarget(cloned); err != nil {
		return RenderedResourceSourcePipeline{}, err
	}
	gets := make([]atc.Step, len(cloned.Sources))
	for index, source := range cloned.Sources {
		gets[index] = atc.Step{Config: &atc.GetStep{Name: source.Name, Resource: source.Resource, Version: source.Version, Trigger: source.Trigger}}
	}
	config := atc.Config{Resources: cloned.Resources, ResourceTypes: cloned.ResourceTypes, Jobs: atc.JobConfigs{{Name: "admit", PlanSequence: gets}}}
	warnings, errs := configvalidate.Validate(config)
	if len(errs) > 0 {
		return RenderedResourceSourcePipeline{}, fmt.Errorf("workflow: invalid rendered resource-source Concourse config:\n%s", strings.Join(errs, "\n"))
	}
	if err := rejectIdentifierWarnings(warnings); err != nil {
		return RenderedResourceSourcePipeline{}, err
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return RenderedResourceSourcePipeline{}, fmt.Errorf("workflow: canonicalize resource-source pipeline config: %w", err)
	}
	payload, err := atc.CanonicalJSON(struct {
		CanonicalJSONVersion int              `json:"canonical_json_version"`
		TeamID               int              `json:"team_id"`
		WorkflowDefinitionID int              `json:"workflow_definition_id"`
		WorkflowName         string           `json:"workflow_name"`
		WorkflowVersion      int              `json:"workflow_version"`
		Sources              []ResourceSource `json:"sources"`
		Config               atc.Config       `json:"config"`
	}{atc.CanonicalJSONVersion, cloned.TeamID, cloned.WorkflowDefinitionID, cloned.WorkflowName, cloned.WorkflowVersion, cloned.Sources, config})
	if err != nil {
		return RenderedResourceSourcePipeline{}, fmt.Errorf("workflow: canonicalize resource-source pipeline identity: %w", err)
	}
	configHash := hashDomainSeparated(resourceSourcePipelineHashDomain, payload)
	name := fmt.Sprintf("agent-workflow-source-%s-v%d-%s", cloned.WorkflowName, cloned.WorkflowVersion, configHash[:12])
	if err := validateSafeIdentifier(name, "generated resource-source pipeline name"); err != nil {
		return RenderedResourceSourcePipeline{}, err
	}
	return RenderedResourceSourcePipeline{PipelineName: name, Config: config, CanonicalJSON: canonical, ConfigHash: configHash}, nil
}

func validateResourceSourcePipelineTarget(target *ResourceSourcePipelineTarget) error {
	if target == nil {
		return fmt.Errorf("workflow: resource-source pipeline target is required")
	}
	if target.TeamID <= 0 {
		return fmt.Errorf("workflow: source pipeline team ID must be positive")
	}
	if target.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("workflow: source pipeline definition ID must be positive")
	}
	if target.WorkflowVersion <= 0 {
		return fmt.Errorf("workflow: source pipeline workflow version must be positive")
	}
	if err := validateSafeIdentifier(target.WorkflowName, "workflow name"); err != nil {
		return err
	}
	if len(target.Sources) == 0 {
		return fmt.Errorf("workflow: source pipeline requires resource sources")
	}
	if err := ValidateResourceSources(target.Sources, target.Resources, target.ResourceTypes); err != nil {
		return err
	}
	closure, err := ResourceSourceResourceTypes(target.Sources, target.Resources, target.ResourceTypes)
	if err != nil {
		return err
	}
	if len(closure) != len(target.ResourceTypes) {
		return fmt.Errorf("workflow: source pipeline resource types must contain only the source-reachable closure")
	}
	for index := range closure {
		if !reflect.DeepEqual(closure[index], target.ResourceTypes[index]) {
			return fmt.Errorf("workflow: source pipeline resource types must equal the source-reachable closure")
		}
	}
	return nil
}

func cloneResourceSourcePipelineTarget(target ResourceSourcePipelineTarget) (*ResourceSourcePipelineTarget, error) {
	copied, err := copystructure.Copy(&target)
	if err != nil {
		return nil, err
	}
	cloned, ok := copied.(*ResourceSourcePipelineTarget)
	if !ok {
		return nil, fmt.Errorf("unexpected cloned source pipeline target type %T", copied)
	}
	return cloned, nil
}
