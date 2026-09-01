package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type ResourceClientObservation struct{ Value string }

type resourceClientFixture struct {
	api      *PipelineAPI
	client   clientapi.Team
	ref      atc.PipelineRef
	pipeline db.Pipeline
	resource db.Resource
	scope    db.ResourceConfigScope
	versions map[string]db.ResourceConfigVersion
}

func ResourceClientJourneyDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ResourceClientObservation](
			"the production resource client completes journey {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (ResourceClientObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ResourceClientObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeResourceClientJourney(database, profile, rec)
				return ResourceClientObservation{Value: value}, err
			},
		),
		CheckString[ResourceClientObservation]("the resource journey result is {string}", "resource journey result", func(in ResourceClientObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func newResourceClientFixture(database JetbridgeDB, rec *brine.Recorder) (*resourceClientFixture, error) {
	api, err := newPipelineAPI(database, rec)
	if err != nil {
		return nil, err
	}
	ref := atc.PipelineRef{Name: "resource-pipeline", InstanceVars: atc.InstanceVars{"branch": "main"}}
	pipeline, _, err := api.Team.SavePipeline(
		ref,
		atc.Config{
			Resources: atc.ResourceConfigs{{
				Name: "image", Type: "registry-image", Public: true,
				Source: atc.Source{"repository": "example/image"},
			}},
			ResourceTypes: atc.ResourceTypes{{
				Name: "custom", Type: "registry-image", Source: atc.Source{"repository": "example/type"},
			}},
			Prototypes: atc.Prototypes{{
				Name: "prototype", Type: "registry-image", Source: atc.Source{"repository": "example/prototype"},
			}},
			Jobs: atc.JobConfigs{{Name: "consume", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "image"}}}}},
		}, 0, false,
	)
	if err != nil {
		return nil, err
	}
	resource, found, err := pipeline.Resource("image")
	if err != nil || !found {
		return nil, fmt.Errorf("load journey resource: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return nil, err
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return nil, err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return nil, err
	}
	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{
		{"ref": "1"}, {"ref": "2"}, {"ref": "3"}, {"ref": "4"}, {"ref": "5"},
	}); err != nil {
		return nil, err
	}
	versions := map[string]db.ResourceConfigVersion{}
	for i := 1; i <= 5; i++ {
		key := fmt.Sprint(i)
		version, found, err := scope.FindVersion(atc.Version{"ref": key})
		if err != nil || !found {
			return nil, fmt.Errorf("find journey version %s: found=%t: %w", key, found, err)
		}
		versions[key] = version
	}
	client := clientapi.NewClient(api.Server.URL, api.Client, false).Team("api-team")
	return &resourceClientFixture{api: api, client: client, ref: ref, pipeline: pipeline, resource: resource, scope: scope, versions: versions}, nil
}

func observeResourceClientJourney(database JetbridgeDB, profile string, rec *brine.Recorder) (string, error) {
	fixture, err := newResourceClientFixture(database, rec)
	if err != nil {
		return "", err
	}
	switch profile {
	case "identity":
		resources, err := fixture.client.ListResources(fixture.ref)
		if err != nil {
			return "", err
		}
		resource, found, err := fixture.client.Resource(fixture.ref, "image")
		if err != nil {
			return "", err
		}
		_, missing, err := fixture.client.Resource(fixture.ref, "missing")
		if err != nil {
			return "", err
		}
		resourceTypes, typesFound, err := fixture.client.ResourceTypes(fixture.ref)
		return fmt.Sprintf("list=%d;found=%t;name=%s;missing=%t;types-found=%t;types=%d", len(resources), found, resource.Name, missing, typesFound, len(resourceTypes)), err
	case "pages":
		return observeResourceVersionPages(fixture)
	case "mutations":
		return observeResourceVersionMutations(fixture)
	case "clear":
		return observeResourceVersionClear(database, fixture)
	case "shared":
		return observeSharedResources(database, fixture)
	case "checks":
		return observeManualResourceChecks(fixture)
	case "public":
		return observeResourcePublicFlags(fixture)
	default:
		return "", fmt.Errorf("unknown resource client journey %q", profile)
	}
}

func observeResourceVersionPages(fixture *resourceClientFixture) (string, error) {
	all, emptyPagination, found, err := fixture.client.ResourceVersions(fixture.ref, "image", clientapi.Page{}, nil)
	if err != nil || !found {
		return "", fmt.Errorf("list all resource versions: found=%t: %w", found, err)
	}
	limited, limitedPagination, found, err := fixture.client.ResourceVersions(fixture.ref, "image", clientapi.Page{Limit: 2}, nil)
	if err != nil || !found {
		return "", fmt.Errorf("list limited resource versions: found=%t: %w", found, err)
	}
	oldest := fixture.versions["1"].ID()
	newest := fixture.versions["5"].ID()
	pages := []clientapi.Page{
		{From: oldest}, {From: oldest, Limit: 2}, {To: newest}, {To: newest, Limit: 2}, {From: oldest, To: newest},
	}
	for _, page := range pages {
		_, _, found, err := fixture.client.ResourceVersions(fixture.ref, "image", page, nil)
		if err != nil || !found {
			return "", fmt.Errorf("resource version page %+v: found=%t: %w", page, found, err)
		}
	}
	filtered, _, found, err := fixture.client.ResourceVersions(fixture.ref, "image", clientapi.Page{}, atc.Version{"ref": "3"})
	if err != nil || !found {
		return "", fmt.Errorf("filter resource versions: found=%t: %w", found, err)
	}
	_, _, missing, err := fixture.client.ResourceVersions(fixture.ref, "missing", clientapi.Page{}, nil)
	return fmt.Sprintf("all=%d;empty-pages=%t;limited=%d;next=%t;filter=%d;missing=%t", len(all), emptyPagination.Next == nil && emptyPagination.Previous == nil, len(limited), limitedPagination.Next != nil, len(filtered), missing), err
}

func observeResourceVersionMutations(fixture *resourceClientFixture) (string, error) {
	id := fixture.versions["3"].ID()
	disabled, err := fixture.client.DisableResourceVersion(fixture.ref, "image", id)
	if err != nil {
		return "", err
	}
	afterDisable, _, versionFound, err := fixture.client.ResourceVersions(fixture.ref, "image", clientapi.Page{}, atc.Version{"ref": "3"})
	if err != nil || !versionFound || len(afterDisable) != 1 {
		return "", fmt.Errorf("read disabled version: found=%t count=%d: %w", versionFound, len(afterDisable), err)
	}
	enabled, err := fixture.client.EnableResourceVersion(fixture.ref, "image", id)
	if err != nil {
		return "", err
	}
	pinned, err := fixture.client.PinResourceVersion(fixture.ref, "image", id)
	if err != nil {
		return "", err
	}
	commented, err := fixture.client.SetPinComment(fixture.ref, "image", "release candidate")
	if err != nil {
		return "", err
	}
	resource, found, err := fixture.client.Resource(fixture.ref, "image")
	if err != nil || !found {
		return "", fmt.Errorf("read pinned resource: found=%t: %w", found, err)
	}
	unpinned, err := fixture.client.UnpinResource(fixture.ref, "image")
	if err != nil {
		return "", err
	}
	missingDisable, err := fixture.client.DisableResourceVersion(fixture.ref, "missing", id)
	if err != nil {
		return "", err
	}
	missingEnable, err := fixture.client.EnableResourceVersion(fixture.ref, "missing", id)
	if err != nil {
		return "", err
	}
	missingPin, err := fixture.client.PinResourceVersion(fixture.ref, "missing", id)
	if err != nil {
		return "", err
	}
	missingUnpin, err := fixture.client.UnpinResource(fixture.ref, "missing")
	if err != nil {
		return "", err
	}
	missingComment, err := fixture.client.SetPinComment(fixture.ref, "missing", "nope")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("disable=%t;disabled-state=%t;enable=%t;pin=%t;pinned-ref=%s;comment=%s;unpin=%t;missing=%t", disabled, !afterDisable[0].Enabled, enabled, pinned, resource.PinnedVersion["ref"], resource.PinComment, unpinned, !missingDisable && !missingEnable && !missingPin && !missingUnpin && !missingComment && commented), nil
}

func observeResourceVersionClear(database JetbridgeDB, fixture *resourceClientFixture) (string, error) {
	otherConfig, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig("registry-image", atc.Source{"repository": "example/other"}, nil)
	if err != nil {
		return "", err
	}
	otherScope, err := otherConfig.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if err := otherScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "preserved"}}); err != nil {
		return "", err
	}
	removed, err := fixture.client.ClearResourceVersions(fixture.ref, "image")
	if err != nil {
		return "", err
	}
	remaining, _, found, err := fixture.client.ResourceVersions(fixture.ref, "image", clientapi.Page{}, nil)
	if err != nil || !found {
		return "", fmt.Errorf("list cleared resource versions: found=%t: %w", found, err)
	}
	_, preserved, err := otherScope.FindVersion(atc.Version{"ref": "preserved"})
	if err != nil {
		return "", err
	}
	resourceType, found, err := fixture.pipeline.ResourceType("custom")
	if err != nil || !found {
		return "", fmt.Errorf("load clear resource type: found=%t: %w", found, err)
	}
	typeConfig, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resourceType.Type(), resourceType.Source(), nil)
	if err != nil {
		return "", err
	}
	typeScope, err := typeConfig.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if err := resourceType.SetResourceConfigScope(typeScope); err != nil {
		return "", err
	}
	if err := typeScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "a"}, {"ref": "b"}}); err != nil {
		return "", err
	}
	typeRemoved, err := fixture.client.ClearResourceTypeVersions(fixture.ref, "custom")
	return fmt.Sprintf("removed=%d;remaining=%d;preserved=%t;type-removed=%d", removed, len(remaining), preserved, typeRemoved), err
}

func observeSharedResources(database JetbridgeDB, fixture *resourceClientFixture) (string, error) {
	secondPipeline, _, err := fixture.api.Team.SavePipeline(
		atc.PipelineRef{Name: "resource-pipeline-two"},
		atc.Config{Resources: atc.ResourceConfigs{{Name: "image-two", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}}},
		0, false,
	)
	if err != nil {
		return "", err
	}
	second, found, err := secondPipeline.Resource("image-two")
	if err != nil || !found {
		return "", fmt.Errorf("load second shared resource: found=%t: %w", found, err)
	}
	if err := second.SetResourceConfigScope(fixture.scope); err != nil {
		return "", err
	}
	shared, found, err := fixture.client.ListSharedForResource(fixture.ref, "image")
	if err != nil || !found {
		return "", fmt.Errorf("list shared resources: found=%t: %w", found, err)
	}
	_, missing, err := fixture.client.ListSharedForResource(fixture.ref, "missing")
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(shared.Resources))
	for _, resource := range shared.Resources {
		names = append(names, resource.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf("found=true;resources=%s;types=%d;missing=%t", strings.Join(names, ","), len(shared.ResourceTypes), missing), nil
}

func observeManualResourceChecks(fixture *resourceClientFixture) (string, error) {
	resource, resourceFound, err := fixture.client.CheckResource(fixture.ref, "image", atc.Version{"ref": "5"}, false)
	if err != nil {
		return "", err
	}
	resourceType, typeFound, err := fixture.client.CheckResourceType(fixture.ref, "custom", nil, true)
	if err != nil {
		return "", err
	}
	prototype, prototypeFound, err := fixture.client.CheckPrototype(fixture.ref, "prototype", nil, false)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("resource=%t:%s;type=%t:%s;prototype=%t:%s", resourceFound, resource.Status, typeFound, resourceType.Status, prototypeFound, prototype.Status), nil
}

func observeResourcePublicFlags(fixture *resourceClientFixture) (string, error) {
	configured := fixture.resource.Public()
	team := fixture.api.Team
	falsePipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "public-flags"},
		atc.Config{Resources: atc.ResourceConfigs{
			{Name: "default", Type: "registry-image"},
			{Name: "false", Type: "registry-image", Public: false},
		}}, 0, false,
	)
	if err != nil {
		return "", err
	}
	defaultResource, found, err := falsePipeline.Resource("default")
	if err != nil || !found {
		return "", fmt.Errorf("load default public resource: found=%t: %w", found, err)
	}
	falseResource, found, err := falsePipeline.Resource("false")
	if err != nil || !found {
		return "", fmt.Errorf("load false public resource: found=%t: %w", found, err)
	}
	defaultPublic := defaultResource.Public()
	explicitFalse := falseResource.Public()
	return fmt.Sprintf("true=%t;default=%t;false=%t", configured, defaultPublic, explicitFalse), nil
}
