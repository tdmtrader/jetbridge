package workflowrun

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/workflow"
)

// ExperimentResourceSourcePreparer performs the only live source admission
// used by an experiment. Start persists its result at the experiment parent;
// candidate repetitions, evaluator cells, and retries only load that ready
// identity.
type ExperimentResourceSourcePreparer struct {
	definitions DefinitionResolver
	sources     ResourceSourceAdmitter
}

func NewExperimentResourceSourcePreparer(
	definitions DefinitionResolver,
	sources ResourceSourceAdmitter,
) (*ExperimentResourceSourcePreparer, error) {
	if nilInterface(definitions) || nilInterface(sources) {
		return nil, fmt.Errorf("workflow run: experiment resource source preparer dependencies are required")
	}
	return &ExperimentResourceSourcePreparer{
		definitions: definitions,
		sources:     sources,
	}, nil
}

func (preparer *ExperimentResourceSourcePreparer) PrepareResourceSources(
	ctx context.Context,
	request experiment.ResourceSourcePreparation,
) ([]experiment.PreparedResourceSourceAdmission, error) {
	if ctx == nil || request.TeamID <= 0 ||
		strings.TrimSpace(request.TeamName) == "" ||
		strings.TrimSpace(request.Actor) == "" ||
		request.ExperimentID.Validate() != nil {
		return nil, fmt.Errorf("%w: experiment resource source preparation identity is invalid", ErrInvalidRequest)
	}
	targets := make([]experiment.Target, 0, len(request.Definition.Variants)+1)
	for _, variant := range request.Definition.Variants {
		targets = append(targets, variant.Target)
	}
	if request.Definition.Evaluator.Target.DefinitionID > 0 {
		targets = append(targets, request.Definition.Evaluator.Target)
	}

	type preparedKey struct {
		definitionID int
		configHash   string
	}
	prepared := make(map[preparedKey]experiment.PreparedResourceSourceAdmission)
	for _, requested := range targets {
		if err := requested.Validate(); err != nil {
			return nil, fmt.Errorf("%w: experiment resource source target is invalid: %v", ErrInvalidRequest, err)
		}
		definition, found, err := preparer.definitions.Get(
			ctx, requested.WorkflowName, requested.Version,
		)
		if err != nil {
			return nil, err
		}
		if !found || definition.ID <= 0 ||
			int64(definition.ID) != requested.DefinitionID ||
			definition.Name != requested.WorkflowName ||
			definition.Version != requested.Version {
			return nil, fmt.Errorf("%w: experiment resource source definition is unavailable or drifted", ErrDefinitionOrTargetNotFound)
		}
		if definition.Compiled.Function == nil {
			return nil, fmt.Errorf("%w: experiment resource source definition has no function", ErrInvalidRequest)
		}
		if len(definition.Compiled.Function.ResourceSources) == 0 {
			continue
		}
		target, err := workflow.ResourceSourcePipelineTargetFor(definition, request.TeamID)
		if err != nil {
			return nil, fmt.Errorf("%w: derive experiment resource source target: %v", ErrInvalidRequest, err)
		}
		rendered, err := workflow.RenderResourceSourcePipeline(target)
		if err != nil {
			return nil, fmt.Errorf("%w: render experiment resource source target: %v", ErrInvalidRequest, err)
		}
		key := preparedKey{definitionID: definition.ID, configHash: rendered.ConfigHash}
		if _, exists := prepared[key]; exists {
			continue
		}
		ready, err := preparer.sources.AdmitManual(
			ctx,
			AdmissionContext{
				TeamID: request.TeamID, TeamName: request.TeamName, CreatedBy: request.Actor,
				Origin: Origin{
					Kind: "experiment-resource-source",
					Reference: fmt.Sprintf(
						"experiment:%s:definition:%d", request.ExperimentID.String(), definition.ID,
					),
				},
			},
			target,
			fmt.Sprintf(
				"experiment:%s:resource-source:%d:%s",
				request.ExperimentID.String(), definition.ID, rendered.ConfigHash,
			),
		)
		if err != nil {
			return nil, err
		}
		if err := validateReadySourceAdmission(ready, request.TeamID, target); err != nil ||
			ready.WorkflowName != definition.Name ||
			ready.WorkflowVersion != definition.Version {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: prepared experiment resource source identity drifted", ErrInvalidRequest)
		}
		prepared[key] = experiment.PreparedResourceSourceAdmission{
			WorkflowDefinitionID: int64(definition.ID),
			SourceConfigHash:     rendered.ConfigHash,
			AdmissionID:          ready.AdmissionID,
		}
	}

	keys := make([]preparedKey, 0, len(prepared))
	for key := range prepared {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].definitionID != keys[right].definitionID {
			return keys[left].definitionID < keys[right].definitionID
		}
		return keys[left].configHash < keys[right].configHash
	})
	result := make([]experiment.PreparedResourceSourceAdmission, 0, len(keys))
	for _, key := range keys {
		result = append(result, prepared[key])
	}
	return result, nil
}

var _ experiment.ResourceSourcePreparer = (*ExperimentResourceSourcePreparer)(nil)
