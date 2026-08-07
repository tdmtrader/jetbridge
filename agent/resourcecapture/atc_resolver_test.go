package resourcecapture_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestATCResolverScopesExactVersionAndPreservesEnabledState(t *testing.T) {
	fixture := persistResolverFixture(t)

	resolver, err := resourcecapture.NewATCResolver(fixture.teams)
	if err != nil {
		t.Fatal(err)
	}
	resolved, found, err := resolver.Resolve(context.Background(), fixture.request)
	if err != nil || !found {
		t.Fatalf("Resolve() = %#v, %v, %v", resolved, found, err)
	}
	if resolved.TeamID != fixture.team.ID() || resolved.PipelineID != fixture.pipeline.ID() ||
		resolved.PipelineConfigVersion != int(fixture.pipeline.ConfigVersion()) || resolved.ResourceID != fixture.resource.ID() ||
		resolved.ResourceConfigVersionID != fixture.version.ID() || resolved.ResourceVersionID != fixture.version.ID() || resolved.Enabled ||
		!reflect.DeepEqual(resolved.Pipeline, fixture.request.Pipeline) || !reflect.DeepEqual(resolved.Version, fixture.request.Version) ||
		resolved.Resource.Source["private_key"] != "secret" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestATCResolverFailsClosedForTeamMismatchMissingVersionAndCancellation(t *testing.T) {
	fixture := persistResolverFixture(t)
	resolver, err := resourcecapture.NewATCResolver(fixture.teams)
	if err != nil {
		t.Fatal(err)
	}
	teamMismatch := fixture.request
	teamMismatch.TeamID++
	if _, found, err := resolver.Resolve(context.Background(), teamMismatch); err != nil || found {
		t.Fatalf("team mismatch = found %v, err %v", found, err)
	}
	missingVersion := fixture.request
	missingVersion.Version = atc.Version{"ref": "missing"}
	if _, found, err := resolver.Resolve(context.Background(), missingVersion); err != nil || found {
		t.Fatalf("missing version = found %v, err %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := resolver.Resolve(ctx, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Resolve() error = %v", err)
	}
}

type resolverFixture struct {
	teams    db.TeamFactory
	team     db.Team
	pipeline db.Pipeline
	resource db.Resource
	version  db.ResourceConfigVersion
	request  resourcecapture.ResolveRequest
}

func persistResolverFixture(t *testing.T) resolverFixture {
	t.Helper()
	database := useRealResourceCaptureDB(t)
	team, err := database.Teams.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	pipelineRef := atc.PipelineRef{Name: "delivery", InstanceVars: atc.InstanceVars{"branch": "main"}}
	config := atc.Config{Resources: atc.ResourceConfigs{{
		Name: "repository", Type: "git", Source: atc.Source{"private_key": "secret"},
	}}}
	pipeline, created, err := team.SavePipeline(pipelineRef, config, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("SavePipeline() did not create the resolver fixture pipeline")
	}
	resource, found, err := pipeline.Resource("repository")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("persisted resolver fixture resource was not found")
	}
	resourceConfig, err := database.ResourceConfigs.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resourceID := resource.ID()
	scope, err := resourceConfig.FindOrCreateScope(&resourceID)
	if err != nil {
		t.Fatal(err)
	}
	requestedVersion := atc.Version{"ref": "abc123"}
	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{requestedVersion, {"ref": "def456"}}); err != nil {
		t.Fatal(err)
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		t.Fatal(err)
	}
	if found, err := resource.Reload(); err != nil {
		t.Fatal(err)
	} else if !found {
		t.Fatal("persisted resolver fixture resource disappeared")
	}
	version, found, err := resource.FindVersion(requestedVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("persisted resolver fixture version was not found")
	}
	if err := resource.DisableVersion(version.ID()); err != nil {
		t.Fatal(err)
	}

	return resolverFixture{
		teams: database.Teams, team: team, pipeline: pipeline, resource: resource, version: version,
		request: resourcecapture.ResolveRequest{
			TeamID: team.ID(), TeamName: team.Name(), Pipeline: pipelineRef,
			Resource: resource.Name(), Version: requestedVersion,
		},
	}
}
