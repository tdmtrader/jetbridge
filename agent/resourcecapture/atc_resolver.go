package resourcecapture

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type TeamFinder interface {
	FindTeam(string) (db.Team, bool, error)
}

// ATCResolver resolves only through the trusted team -> pipeline -> resource
// ownership chain. The subsequent ID lookup is safe because the exact version
// ID was first obtained from that scoped resource.
type ATCResolver struct {
	teams TeamFinder
	now   func() time.Time
}

func NewATCResolver(teams TeamFinder) (*ATCResolver, error) {
	if isNil(teams) {
		return nil, fmt.Errorf("resource capture: team finder is required")
	}
	return &ATCResolver{teams: teams, now: time.Now}, nil
}

func (resolver *ATCResolver) Resolve(ctx context.Context, request ResolveRequest) (ResolvedResource, bool, error) {
	if err := contextError(ctx); err != nil {
		return ResolvedResource{}, false, err
	}
	if request.TeamID <= 0 || strings.TrimSpace(request.TeamName) == "" || strings.TrimSpace(request.Pipeline.Name) == "" ||
		strings.TrimSpace(request.Resource) == "" || len(request.Version) == 0 {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: invalid resolve request")
	}
	team, found, err := resolver.teams.FindTeam(request.TeamName)
	if err != nil {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: find team: %w", err)
	}
	if !found || isNil(team) || team.ID() != request.TeamID || team.Name() != request.TeamName {
		return ResolvedResource{}, false, nil
	}
	pipeline, found, err := team.Pipeline(request.Pipeline)
	if err != nil {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: find pipeline: %w", err)
	}
	if !found || isNil(pipeline) || pipeline.Archived() || pipeline.TeamID() != request.TeamID || pipeline.TeamName() != request.TeamName ||
		pipeline.Name() != request.Pipeline.Name || !instanceVarsEqual(pipeline.InstanceVars(), request.Pipeline.InstanceVars) {
		return ResolvedResource{}, false, nil
	}
	resource, found, err := pipeline.Resource(request.Resource)
	if err != nil {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: find resource: %w", err)
	}
	if !found || isNil(resource) {
		return ResolvedResource{}, false, nil
	}
	version, found, err := resource.FindVersion(cloneVersion(request.Version))
	if err != nil {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: find exact resource version: %w", err)
	}
	if !found || isNil(version) || version.ID() <= 0 || !versionsEqual(atc.Version(version.Version()), request.Version) {
		return ResolvedResource{}, false, nil
	}
	publicVersion, found, err := pipeline.ResourceVersion(version.ID())
	if err != nil {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: read resource version state: %w", err)
	}
	if !found || publicVersion.ID != version.ID() || !versionsEqual(publicVersion.Version, request.Version) {
		return ResolvedResource{}, false, nil
	}
	resourceTypes, err := pipeline.ResourceTypes()
	if err != nil {
		return ResolvedResource{}, false, fmt.Errorf("resource capture: resolve resource types: %w", err)
	}
	resourceConfig := resource.Config()
	if resourceConfig.Name != request.Resource || strings.TrimSpace(resourceConfig.Type) == "" {
		return ResolvedResource{}, false, nil
	}
	return ResolvedResource{
		TeamID: request.TeamID, TeamName: request.TeamName,
		PipelineID:            pipeline.ID(),
		Pipeline:              atc.PipelineRef{Name: pipeline.Name(), InstanceVars: cloneInstanceVars(pipeline.InstanceVars())},
		PipelineConfigVersion: int(pipeline.ConfigVersion()), ResourceID: resource.ID(), Resource: resourceConfig,
		ResourceTypes: resourceTypes.Deserialize(), ResourceConfigVersionID: version.ID(), ResourceVersionID: version.ID(),
		Version: cloneVersion(publicVersion.Version), Enabled: publicVersion.Enabled, CapturedAt: resolver.now().UTC(),
	}, true, nil
}

func versionsEqual(left, right atc.Version) bool {
	return reflect.DeepEqual(map[string]string(left), map[string]string(right))
}

func instanceVarsEqual(left, right atc.InstanceVars) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return reflect.DeepEqual(left, right)
}
