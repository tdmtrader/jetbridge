package workflowrun

import (
	"bytes"
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const templateVersionReadAttempts = 3

//counterfeiter:generate -o workflowrunfakes/fake_template_team_finder.go . TemplateTeamFinder
type TemplateTeamFinder interface {
	FindTeam(string) (db.Team, bool, error)
}

type TemplateSaver struct{ teams TemplateTeamFinder }

func NewTemplateSaver(teams TemplateTeamFinder) (*TemplateSaver, error) {
	if nilInterface(teams) {
		return nil, fmt.Errorf("%w: template team finder is required", ErrInvalidRequest)
	}
	return &TemplateSaver{teams: teams}, nil
}

func (s *TemplateSaver) SaveOrReuse(
	ctx context.Context,
	admission AdmissionContext,
	spec ImmutableTemplateSpec,
) (WorkflowRunTemplateRef, error) {
	if err := ctx.Err(); err != nil {
		return WorkflowRunTemplateRef{}, err
	}
	canonical, err := spec.Config.CanonicalJSON()
	if err != nil {
		return WorkflowRunTemplateRef{}, fmt.Errorf("%w: invalid immutable template config", ErrImmutableTemplateCollision)
	}
	hash, err := workflow.TargetConfigHash(spec.Config)
	if err != nil || hash != spec.FullHash || !bytes.Equal(canonical, spec.CanonicalJSON) {
		return WorkflowRunTemplateRef{}, fmt.Errorf("%w: template spec hash or canonical bytes mismatch", ErrImmutableTemplateCollision)
	}

	team, found, err := s.teams.FindTeam(admission.TeamName)
	if err != nil {
		return WorkflowRunTemplateRef{}, fmt.Errorf("%w: resolve template team: %v", ErrPlatformFailure, err)
	}
	if !found || team.ID() != admission.TeamID || team.Name() != admission.TeamName {
		return WorkflowRunTemplateRef{}, fmt.Errorf("%w: template team identity mismatch", ErrImmutableTemplateCollision)
	}
	pipelineRef := atc.PipelineRef{Name: spec.Name}
	pipeline, found, err := team.Pipeline(pipelineRef)
	if err != nil {
		return WorkflowRunTemplateRef{}, fmt.Errorf("%w: read immutable template: %v", ErrPlatformFailure, err)
	}
	if !found {
		saved, created, saveErr := team.SavePipeline(pipelineRef, cloneConfig(spec.Config), db.ConfigVersion(0), false)
		pipeline = saved
		if saveErr != nil || !created {
			winner, winnerFound, readErr := team.Pipeline(pipelineRef)
			if readErr != nil {
				return WorkflowRunTemplateRef{}, fmt.Errorf("%w: reread concurrent immutable template: %v", ErrPlatformFailure, readErr)
			}
			if !winnerFound {
				if saveErr != nil {
					return WorkflowRunTemplateRef{}, fmt.Errorf("%w: create immutable template: %v", ErrPlatformFailure, saveErr)
				}
				return WorkflowRunTemplateRef{}, ErrImmutableTemplateCollision
			}
			pipeline = winner
		}
	}

	for range templateVersionReadAttempts {
		ref, drifted, err := validateImmutableTemplate(team, pipelineRef, pipeline, admission.TeamID, spec)
		if err != nil {
			return WorkflowRunTemplateRef{}, err
		}
		if !drifted {
			return ref, nil
		}
		pipeline, found, err = team.Pipeline(pipelineRef)
		if err != nil {
			return WorkflowRunTemplateRef{}, fmt.Errorf("%w: reread drifting immutable template: %v", ErrPlatformFailure, err)
		}
		if !found {
			return WorkflowRunTemplateRef{}, ErrImmutableTemplateCollision
		}
	}
	return WorkflowRunTemplateRef{}, fmt.Errorf("%w: template version changed during reconstruction", ErrImmutableTemplateCollision)
}

func validateImmutableTemplate(
	team db.Team,
	pipelineRef atc.PipelineRef,
	pipeline db.Pipeline,
	teamID int,
	spec ImmutableTemplateSpec,
) (WorkflowRunTemplateRef, bool, error) {
	if nilInterface(pipeline) || pipeline.ID() <= 0 || pipeline.TeamID() != teamID || pipeline.Name() != spec.Name ||
		!pipeline.Template() || pipeline.InstanceVars() != nil || pipeline.Archived() {
		return WorkflowRunTemplateRef{}, false, ErrImmutableTemplateCollision
	}
	before := pipeline.ConfigVersion()
	if before <= 0 {
		return WorkflowRunTemplateRef{}, false, ErrImmutableTemplateCollision
	}
	config, err := pipeline.Config()
	if err != nil {
		return WorkflowRunTemplateRef{}, false, fmt.Errorf("%w: reconstruct immutable template: %v", ErrPlatformFailure, err)
	}
	afterPipeline, found, err := team.Pipeline(pipelineRef)
	if err != nil {
		return WorkflowRunTemplateRef{}, false, fmt.Errorf("%w: verify immutable template version: %v", ErrPlatformFailure, err)
	}
	if !found || nilInterface(afterPipeline) {
		return WorkflowRunTemplateRef{}, false, ErrImmutableTemplateCollision
	}
	if afterPipeline.ConfigVersion() != before {
		return WorkflowRunTemplateRef{}, true, nil
	}
	if afterPipeline.ID() != pipeline.ID() || afterPipeline.TeamID() != teamID || afterPipeline.Name() != spec.Name ||
		!afterPipeline.Template() || afterPipeline.InstanceVars() != nil || afterPipeline.Archived() {
		return WorkflowRunTemplateRef{}, false, ErrImmutableTemplateCollision
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return WorkflowRunTemplateRef{}, false, fmt.Errorf("%w: canonicalize immutable template: %v", ErrPlatformFailure, err)
	}
	hash, err := workflow.TargetConfigHash(config)
	if err != nil || hash != spec.FullHash || !bytes.Equal(canonical, spec.CanonicalJSON) {
		return WorkflowRunTemplateRef{}, false, ErrImmutableTemplateCollision
	}
	return WorkflowRunTemplateRef{
		PipelineID: pipeline.ID(), TeamID: pipeline.TeamID(), Name: pipeline.Name(),
		ConfigVersion: int(before), FullHash: spec.FullHash,
	}, false, nil
}
