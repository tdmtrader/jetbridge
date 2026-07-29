package resourcecapture_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

type resolverResource struct {
	db.Resource
	id      int
	config  atc.ResourceConfig
	version db.ResourceConfigVersion
	found   bool
	err     error
	queries []atc.Version
}

func (resource *resolverResource) ID() int                    { return resource.id }
func (resource *resolverResource) Config() atc.ResourceConfig { return resource.config }
func (resource *resolverResource) FindVersion(version atc.Version) (db.ResourceConfigVersion, bool, error) {
	resource.queries = append(resource.queries, version)
	return resource.version, resource.found, resource.err
}

type resolverVersion struct {
	db.ResourceConfigVersion
	id      int
	version db.Version
}

func (version *resolverVersion) ID() int             { return version.id }
func (version *resolverVersion) Version() db.Version { return version.version }

func TestATCResolverScopesExactVersionAndPreservesEnabledState(t *testing.T) {
	factory := &dbfakes.FakeTeamFactory{}
	team := &dbfakes.FakeTeam{}
	pipeline := &dbfakes.FakePipeline{}
	team.IDReturns(7)
	team.NameReturns("main")
	pipeline.IDReturns(17)
	pipeline.TeamIDReturns(7)
	pipeline.TeamNameReturns("main")
	pipeline.NameReturns("delivery")
	pipeline.InstanceVarsReturns(atc.InstanceVars{"branch": "main"})
	pipeline.ConfigVersionReturns(db.ConfigVersion(11))
	pipeline.ArchivedReturns(false)
	version := &resolverVersion{id: 31, version: db.Version{"ref": "abc123"}}
	resource := &resolverResource{
		id:      23,
		config:  atc.ResourceConfig{Name: "repository", Type: "git", Source: atc.Source{"private_key": "secret"}},
		version: version, found: true,
	}
	requestedVersion := atc.Version{"ref": "abc123"}
	factory.FindTeamReturns(team, true, nil)
	team.PipelineReturns(pipeline, true, nil)
	pipeline.ResourceReturns(resource, true, nil)
	pipeline.ResourceVersionReturns(atc.ResourceVersion{ID: 31, Version: requestedVersion, Enabled: false}, true, nil)
	pipeline.ResourceTypesReturns(nil, nil)

	resolver, err := resourcecapture.NewATCResolver(factory)
	if err != nil {
		t.Fatal(err)
	}
	resolved, found, err := resolver.Resolve(context.Background(), resourcecapture.ResolveRequest{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{Name: "delivery", InstanceVars: atc.InstanceVars{"branch": "main"}},
		Resource: "repository", Version: requestedVersion,
	})
	if err != nil || !found {
		t.Fatalf("Resolve() = %#v, %v, %v", resolved, found, err)
	}
	if resolved.TeamID != 7 || resolved.PipelineConfigVersion != 11 || resolved.ResourceID != 23 || resolved.ResourceConfigVersionID != 31 || resolved.Enabled ||
		!reflect.DeepEqual(resolved.Version, requestedVersion) || resolved.Resource.Source["private_key"] != "secret" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if got := resource.queries[0]; !reflect.DeepEqual(got, requestedVersion) {
		t.Fatalf("FindVersion(%#v)", got)
	}
}

func TestATCResolverFailsClosedForTeamMismatchMissingVersionAndCancellation(t *testing.T) {
	factory := &dbfakes.FakeTeamFactory{}
	team := &dbfakes.FakeTeam{}
	team.IDReturns(99)
	team.NameReturns("main")
	factory.FindTeamReturns(team, true, nil)
	resolver, err := resourcecapture.NewATCResolver(factory)
	if err != nil {
		t.Fatal(err)
	}
	request := resourcecapture.ResolveRequest{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{Name: "delivery"}, Resource: "repository", Version: atc.Version{"ref": "abc123"},
	}
	if _, found, err := resolver.Resolve(context.Background(), request); err != nil || found {
		t.Fatalf("team mismatch = found %v, err %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := resolver.Resolve(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Resolve() error = %v", err)
	}
}
