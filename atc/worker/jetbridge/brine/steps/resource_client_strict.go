package steps

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type strictResourceClientObservation struct {
	Profile string
	Failure string
}

type strictResourceClientBoundary struct {
	database JetbridgeDB
	team     db.Team
	pipeline db.Pipeline
	resource db.Resource
	ref      atc.PipelineRef
	client   clientapi.Team
	scenario *dbtest.Scenario
}

func ResourceClientStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictResourceClientObservation](
			"the strict production resource client behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (strictResourceClientObservation, error) {
				profile, err := paramAt("the strict production resource client behavior {string} is exercised", p, 0)
				if err != nil {
					return strictResourceClientObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictResourceClientObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictResourceClientBoundary(database, rec, profile)
				if err != nil {
					return strictResourceClientObservation{}, err
				}
				return strictResourceClientObservation{Profile: profile, Failure: boundary.observe(profile)}, nil
			},
		),
		brine.DefineCheck[strictResourceClientObservation](
			"the strict resource client behavior exactly matches {string}",
			func(in strictResourceClientObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the strict resource client behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func newStrictResourceClientBoundary(database JetbridgeDB, rec *brine.Recorder, profile string) (*strictResourceClientBoundary, error) {
	previousGlobalResources := atc.EnableGlobalResources
	atc.EnableGlobalResources = true
	rec.RegisterDisposer(func() { atc.EnableGlobalResources = previousGlobalResources })

	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: resourceStrictTeam})
	if err != nil {
		return nil, err
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {resourceStrictConnector + ":" + resourceStrictUser}},
	}); err != nil {
		return nil, err
	}
	ref := atc.PipelineRef{Name: resourceStrictPipeline, InstanceVars: atc.InstanceVars{"branch": "master"}}
	config := atc.Config{Resources: atc.ResourceConfigs{{
		Name: "resource-name", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "strict/shared"}, Icon: "git",
	}}}
	pipeline, _, err := team.SavePipeline(ref, config, 0, false)
	if err != nil {
		return nil, err
	}
	if _, _, err := team.SavePipeline(atc.PipelineRef{Name: resourceStrictPipeline}, atc.Config{Resources: atc.ResourceConfigs{{
		Name: "decoy-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "strict/decoy"},
	}}}, 0, false); err != nil {
		return nil, err
	}
	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
	scenario.Run(database.Builder.WithResourceVersions("resource-name", atc.Version{"ref": "one"}))
	resource := scenario.Resource("resource-name")

	if profile == "list-shared" {
		sharedPipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "shared-pipeline"}, atc.Config{Resources: atc.ResourceConfigs{{
			Name: "shared-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "strict/shared"},
		}}}, 0, false)
		if err != nil {
			return nil, err
		}
		sharedScenario := &dbtest.Scenario{Team: team, Pipeline: sharedPipeline}
		sharedScenario.Run(database.Builder.WithResourceVersions("shared-resource", atc.Version{"ref": "two"}))
	}

	boundary := &strictResourceClientBoundary{
		database: database, team: team, pipeline: pipeline, resource: resource, ref: ref, scenario: scenario,
	}
	if profile == "clear-cache" {
		if err := boundary.seedCache(); err != nil {
			return nil, err
		}
	}
	owner, err := strictResourceTokenClient(database, resourceStrictUser, nil)
	if err != nil {
		return nil, err
	}
	url, err := startAPIResourcesServer(database, rec, owner)
	if err != nil {
		return nil, err
	}
	boundary.client = clientapi.NewClient(url, owner, false).Team(team.Name())
	return boundary, nil
}

func (b *strictResourceClientBoundary) seedCache() error {
	b.scenario.Run(b.database.Builder.WithBaseWorker())
	if len(b.scenario.Workers) != 1 {
		return fmt.Errorf("workers got %d, want 1", len(b.scenario.Workers))
	}
	build, err := b.team.CreateOneOffBuild()
	if err != nil {
		return err
	}
	cache, err := db.NewResourceCacheFactory(b.database.Conn, b.database.LockFactory).FindOrCreateResourceCache(
		db.ForBuild(build.ID()), b.resource.Type(), atc.Version{"ref": "cached"}, b.resource.Source(), nil, nil,
	)
	if err != nil {
		return err
	}
	creating, err := b.database.VolumeRepository.CreateVolumeWithHandle(
		"resource-client-cache", b.team.ID(), b.scenario.Workers[0].Name(), db.VolumeTypeContainer,
	)
	if err != nil {
		return err
	}
	created, err := creating.Created()
	if err != nil {
		return err
	}
	_, err = created.InitializeResourceCache(cache)
	return err
}

func (b *strictResourceClientBoundary) observe(profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	switch profile {
	case "list-resources":
		resources, err := b.client.ListResources(b.ref)
		if err != nil || len(resources) != 1 || resources[0].Name != "resource-name" || resources[0].Type != dbtest.BaseResourceType {
			return fail("resources=%#v err=%v", resources, err)
		}
	case "get-resource":
		resource, found, err := b.client.Resource(b.ref, "resource-name")
		if err != nil || !found || resource.Name != "resource-name" || resource.Type != dbtest.BaseResourceType || resource.PipelineID != b.pipeline.ID() || resource.Icon != "git" {
			return fail("resource=%#v found=%t err=%v", resource, found, err)
		}
	case "get-not-found":
		_, found, err := b.client.Resource(b.ref, "missing")
		if err != nil || found {
			return fail("missing found=%t err=%v", found, err)
		}
	case "clear-cache":
		removed, err := b.client.ClearResourceCache(b.ref, "resource-name", nil)
		if err != nil || removed != 1 {
			return fail("removed=%d err=%v", removed, err)
		}
		var remaining int
		if err := b.database.Conn.QueryRow(`SELECT count(*) FROM worker_resource_caches`).Scan(&remaining); err != nil || remaining != 0 {
			return fail("remaining=%d err=%v", remaining, err)
		}
	case "list-shared":
		shared, found, err := b.client.ListSharedForResource(b.ref, "resource-name")
		if err != nil || !found {
			return fail("shared=%#v found=%t err=%v", shared, found, err)
		}
		identifiers := make([]string, len(shared.Resources))
		for i, resource := range shared.Resources {
			identifiers[i] = resource.PipelineName + "/" + resource.Name
		}
		sort.Strings(identifiers)
		want := []string{"resource-pipeline/resource-name", "shared-pipeline/shared-resource"}
		if !reflect.DeepEqual(identifiers, want) {
			return fail("shared identifiers=%v want=%v", identifiers, want)
		}
	case "shared-not-found":
		_, found, err := b.client.ListSharedForResource(b.ref, "missing")
		if err != nil || found {
			return fail("missing shared found=%t err=%v", found, err)
		}
	default:
		return fail("unknown profile %q", profile)
	}
	return ""
}
