package steps

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type ResourceTypeObservation struct{ Value string }

func ResourceTypeDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ResourceTypeObservation](
			"the real resource type domain evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (ResourceTypeObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ResourceTypeObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeResourceTypeDomain(database, profile)
				return ResourceTypeObservation{Value: value}, err
			},
		),
		CheckString[ResourceTypeObservation]("the resource type domain result is {string}", "resource type domain result", func(in ResourceTypeObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func resourceTypePipeline(database JetbridgeDB, name string, config atc.Config) (db.Pipeline, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: name + "-team"})
	if err != nil {
		return nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, config, 0, false)
	return pipeline, err
}

func strictResourceTypeConfig() atc.Config {
	return atc.Config{ResourceTypes: atc.ResourceTypes{
		{
			Name:     "some-type",
			Type:     "registry-image",
			Source:   atc.Source{"some": "repository"},
			Defaults: atc.Source{"some-default-k1": "some-default-v1"},
		},
		{
			Name:       "some-other-type",
			Type:       "some-type",
			Privileged: true,
			Source:     atc.Source{"some": "other-repository"},
		},
		{
			Name:   "some-type-with-params",
			Type:   "s3",
			Source: atc.Source{"some": "repository"},
			Params: atc.Params{"unpack": "true"},
		},
		{
			Name:       "some-type-with-custom-check",
			Type:       "registry-image",
			Source:     atc.Source{"some": "repository"},
			CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond},
		},
	}}
}

func strictResourceTypes(database JetbridgeDB, name string) (db.Pipeline, db.ResourceTypes, error) {
	pipeline, err := resourceTypePipeline(database, name, strictResourceTypeConfig())
	if err != nil {
		return nil, nil, err
	}
	types, err := pipeline.ResourceTypes()
	return pipeline, types, err
}

func observeStrictResourceTypeCollection(database JetbridgeDB) (string, error) {
	_, types, err := strictResourceTypes(database, "strict-type-collection")
	if err != nil {
		return "", err
	}
	ids := map[int]struct{}{}
	for _, resourceType := range types {
		ids[resourceType.ID()] = struct{}{}
	}
	exact := equalResourceTypesByName(types.Configs(), strictResourceTypeConfig().ResourceTypes)
	return fmt.Sprintf("exact=%t;unique-ids=%t", exact, len(ids) == 4), nil
}

func observeStrictResourceTypeInactive(database JetbridgeDB) (string, error) {
	pipeline, _, err := strictResourceTypes(database, "strict-type-inactive")
	if err != nil {
		return "", err
	}
	team, found, err := database.TeamFactory.FindTeam("strict-type-inactive-team")
	if err != nil || !found {
		return "", fmt.Errorf("reload inactive team: found=%t: %w", found, err)
	}
	pipeline, _, err = team.SavePipeline(
		atc.PipelineRef{Name: "strict-type-inactive"},
		atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "some-type", Type: "registry-image", Source: atc.Source{"some": "repository"}}}},
		pipeline.ConfigVersion(),
		false,
	)
	if err != nil {
		return "", err
	}
	types, err := pipeline.ResourceTypes()
	if err != nil {
		return "", err
	}
	names := make([]string, len(types))
	for i, resourceType := range types {
		names[i] = resourceType.Name()
	}
	return "names=" + strings.Join(names, ","), nil
}

func observeStrictResourceTypeFilterSameName(database JetbridgeDB) (string, error) {
	pipeline, err := resourceTypePipeline(database, "strict-type-filter-same", atc.Config{
		Resources: atc.ResourceConfigs{{Name: "some-name", Type: "some-name", Source: atc.Source{}}},
		ResourceTypes: atc.ResourceTypes{{
			Name: "some-name", Type: "some-custom-type", Source: atc.Source{"some": "repository"},
			CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond},
		}},
	})
	if err != nil {
		return "", err
	}
	resource, found, err := pipeline.Resource("some-name")
	if err != nil || !found {
		return "", fmt.Errorf("load same-name resource: found=%t: %w", found, err)
	}
	types, err := pipeline.ResourceTypes()
	if err != nil {
		return "", err
	}
	return "tree=" + strictResourceTypeTree(types.Filter(resource)), nil
}

func observeStrictResourceTypeFilterDependency(database JetbridgeDB) (string, error) {
	if _, err := resourceTypePipeline(database, "strict-type-filter-other", atc.Config{ResourceTypes: atc.ResourceTypes{{
		Name: "some-custom-type", Type: "some-different-foo-type", Source: atc.Source{"some": "repository"},
	}}}); err != nil {
		return "", err
	}
	pipeline, err := resourceTypePipeline(database, "strict-type-filter-dependency", atc.Config{
		Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "some-custom-type", Source: atc.Source{}}},
		ResourceTypes: atc.ResourceTypes{
			{Name: "registry-image", Type: "registry-image", Source: atc.Source{"some": "repository"}},
			{Name: "some-other-type", Type: "registry-image", Privileged: true, Source: atc.Source{"some": "other-repository"}},
			{Name: "some-type-with-params", Type: "s3", Source: atc.Source{"some": "repository"}, Params: atc.Params{"unpack": "true"}},
			{Name: "some-type-with-custom-check", Type: "registry-image", Source: atc.Source{"some": "repository"}, CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond}},
			{Name: "some-custom-type", Type: "some-other-foo-type", Source: atc.Source{"some": "repository"}, CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond}},
			{Name: "some-other-foo-type", Type: "some-other-type", Source: atc.Source{"some": "repository"}, CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond}},
		},
	})
	if err != nil {
		return "", err
	}
	resource, found, err := pipeline.Resource("some-resource")
	if err != nil || !found {
		return "", fmt.Errorf("load dependency resource: found=%t: %w", found, err)
	}
	types, err := pipeline.ResourceTypes()
	if err != nil {
		return "", err
	}
	return "tree=" + strictResourceTypeTree(types.Filter(resource)), nil
}

func strictResourceTypeTree(types db.ResourceTypes) string {
	items := make([]string, len(types))
	for i, resourceType := range types {
		items[i] = resourceType.Name() + ":" + resourceType.Type()
	}
	return strings.Join(items, ",")
}

func observeStrictResourceTypeDeserialize(database JetbridgeDB, baseDefaults bool) (string, error) {
	_, types, err := strictResourceTypes(database, fmt.Sprintf("strict-type-deserialize-%t", baseDefaults))
	if err != nil {
		return "", err
	}
	if baseDefaults {
		atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{"s3": {"default-s3-key": "some-value"}})
		defer atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
	}
	expected := atc.ResourceTypes{
		{Name: "some-type", Type: "registry-image", Source: atc.Source{"some": "repository"}, Defaults: atc.Source{"some-default-k1": "some-default-v1"}},
		{Name: "some-other-type", Type: "some-type", Source: atc.Source{"some-default-k1": "some-default-v1", "some": "other-repository"}, Privileged: true},
		{Name: "some-type-with-params", Type: "s3", Source: atc.Source{"some": "repository"}, Params: atc.Params{"unpack": "true"}},
		{Name: "some-type-with-custom-check", Type: "registry-image", Source: atc.Source{"some": "repository"}, CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond}},
	}
	if baseDefaults {
		expected[2].Source = atc.Source{"some": "repository", "default-s3-key": "some-value"}
	}
	return fmt.Sprintf("exact=%t", equalResourceTypesByName(types.Deserialize(), expected)), nil
}

func equalResourceTypesByName(actual, expected atc.ResourceTypes) bool {
	if len(actual) != len(expected) {
		return false
	}
	byName := make(map[string]atc.ResourceType, len(actual))
	for _, resourceType := range actual {
		byName[resourceType.Name] = resourceType
	}
	for _, resourceType := range expected {
		if !reflect.DeepEqual(byName[resourceType.Name], resourceType) {
			return false
		}
	}
	return true
}

func observeStrictResourceTypeScope(database JetbridgeDB) (string, error) {
	pipeline, err := resourceTypePipeline(database, "strict-type-scope", atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "type", Type: "registry-image", Source: atc.Source{"repository": "example/type"}}}})
	if err != nil {
		return "", err
	}
	resourceType, found, err := pipeline.ResourceType("type")
	if err != nil || !found {
		return "", fmt.Errorf("load strict scoped resource type: found=%t: %w", found, err)
	}
	beforeZero := resourceType.ResourceConfigScopeID() == 0
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resourceType.Type(), resourceType.Source(), nil)
	if err != nil {
		return "", err
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if err := resourceType.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if _, err := resourceType.Reload(); err != nil {
		return "", err
	}
	return fmt.Sprintf("before-zero=%t;after-equal=%t", beforeZero, resourceType.ResourceConfigScopeID() == scope.ID()), nil
}

func strictResourceTypeForBuild(database JetbridgeDB, suffix string) (db.ResourceType, error) {
	pipeline, err := resourceTypePipeline(database, "strict-type-build-"+suffix, atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "type", Type: "registry-image"}}})
	if err != nil {
		return nil, err
	}
	resourceType, found, err := pipeline.ResourceType("type")
	if err != nil || !found {
		return nil, fmt.Errorf("load strict build resource type: found=%t: %w", found, err)
	}
	return resourceType, nil
}

func strictResourceTypeBuildPlan() atc.Plan {
	return atc.Plan{ID: "some-plan", Check: &atc.CheckPlan{Name: "wreck"}}
}

func observeStrictResourceTypeBuild(database JetbridgeDB, profile string) (string, error) {
	resourceType, err := strictResourceTypeForBuild(database, profile)
	if err != nil {
		return "", err
	}
	plan := strictResourceTypeBuildPlan()

	if profile == "trace" {
		provider := sdktrace.NewTracerProvider()
		tracing.ConfigureTraceProvider(provider)
		ctx, span := tracing.StartSpan(context.Background(), "resource-type-strict-build", nil)
		defer func() {
			span.End()
			tracing.Configured = false
		}()
		build, created, err := resourceType.CreateBuild(ctx, false, plan)
		if err != nil || !created || build == nil {
			return "", fmt.Errorf("create traced resource type build: created=%t nil=%t: %w", created, build == nil, err)
		}
		return fmt.Sprintf("trace=%t", strings.Contains(build.SpanContext().Get("traceparent"), span.SpanContext().TraceID().String())), nil
	}

	if profile == "created" || profile == "events" {
		build, created, err := resourceType.CreateBuild(context.Background(), false, plan)
		if err != nil || !created || build == nil {
			return "", fmt.Errorf("create strict resource type build: created=%t nil=%t: %w", created, build == nil, err)
		}
		if profile == "created" {
			exact := build.Name() == db.CheckBuildName &&
				build.ResourceTypeID() == resourceType.ID() &&
				build.PipelineID() == resourceType.PipelineID() &&
				build.TeamID() == resourceType.TeamID() &&
				!build.IsManuallyTriggered() &&
				build.Status() == db.BuildStatusStarted &&
				reflect.DeepEqual(build.PrivatePlan(), plan)
			return fmt.Sprintf("exact=%t", exact), nil
		}
		if err := build.SaveEvent(event.Log{Payload: "log"}); err != nil {
			return "", err
		}
		count, err := strictResourceTypeEventCount(database, build)
		return fmt.Sprintf("events=%d", count), err
	}

	completed, created, err := resourceType.CreateBuild(context.Background(), false, plan)
	if err != nil || !created || completed == nil {
		return "", fmt.Errorf("create completed predecessor: created=%t nil=%t: %w", created, completed == nil, err)
	}
	if err := completed.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	running, created, err := resourceType.CreateBuild(context.Background(), false, plan)
	if err != nil || !created || running == nil {
		return "", fmt.Errorf("create running predecessor: created=%t nil=%t: %w", created, running == nil, err)
	}

	switch profile {
	case "blocked":
		build, secondCreated, err := resourceType.CreateBuild(context.Background(), false, plan)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("created=%t;nil=%t", secondCreated, build == nil), nil
	case "manual":
		build, manualCreated, err := resourceType.CreateBuild(context.Background(), true, plan)
		if err != nil || build == nil {
			return "", fmt.Errorf("create manual resource type build: created=%t nil=%t: %w", manualCreated, build == nil, err)
		}
		return fmt.Sprintf("created=%t;manual=%t;type-id=%t", manualCreated, build.IsManuallyTriggered(), build.ResourceTypeID() == resourceType.ID()), nil
	case "after":
		if err := running.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		build, afterCreated, err := resourceType.CreateBuild(context.Background(), false, plan)
		if err != nil || build == nil {
			return "", fmt.Errorf("create resource type build after finish: created=%t nil=%t: %w", afterCreated, build == nil, err)
		}
		return fmt.Sprintf("created=%t;type-id=%t", afterCreated, build.ResourceTypeID() == resourceType.ID()), nil
	default:
		return "", fmt.Errorf("unknown strict resource type build profile %q", profile)
	}
}

func strictResourceTypeEventCount(database JetbridgeDB, build db.Build) (int, error) {
	var count int
	err := database.Conn.QueryRow(
		"SELECT COUNT(*) FROM check_build_events WHERE build_id = $1",
		build.ID(),
	).Scan(&count)
	return count, err
}

func observeStrictResourceTypePlan(database JetbridgeDB, profile string) (string, error) {
	atc.DefaultCheckInterval = time.Minute
	defer func() { atc.DefaultCheckInterval = 0 }()

	if profile == "base" {
		pipeline, err := resourceTypePipeline(database, "strict-type-plan-base", atc.Config{ResourceTypes: atc.ResourceTypes{{
			Name: "some-resource-type", Type: "some-base-resource-type", Tags: atc.Tags{"tag"}, Source: atc.Source{"some": "source"},
		}}})
		if err != nil {
			return "", err
		}
		resourceType, found, err := pipeline.ResourceType("some-resource-type")
		if err != nil || !found {
			return "", fmt.Errorf("load base plan resource type: found=%t: %w", found, err)
		}
		version := atc.Version{"version": "from"}
		actual := resourceType.CheckPlan(atc.NewPlanFactory(0), atc.ResourceTypes{}, version, atc.CheckEvery{Interval: time.Hour}, atc.Source{"source-test": "default"}, false, false)
		expected := atc.Plan{ID: "1", Check: &atc.CheckPlan{
			Name: "some-resource-type", Type: "some-base-resource-type",
			Source: atc.Source{"some": "source", "source-test": "default"}, Tags: atc.Tags{"tag"},
			TypeImage: atc.TypeImage{BaseType: "some-base-resource-type"}, FromVersion: version,
			ResourceType: "some-resource-type", Interval: atc.CheckEvery{Interval: time.Hour},
		}}
		return fmt.Sprintf("exact=%t", reflect.DeepEqual(actual, expected)), nil
	}

	parentInterval := time.Minute
	privileged := false
	tags := atc.Tags(nil)
	skipInterval := false
	skipRecursively := false
	switch profile {
	case "custom":
		tags = atc.Tags{"tag"}
	case "interval":
		tags = atc.Tags{"tag"}
		parentInterval = 2 * time.Minute
	case "privileged":
		privileged = true
	case "local-skip":
		skipInterval = true
	case "recursive-skip":
		skipInterval = true
		skipRecursively = true
	default:
		return "", fmt.Errorf("unknown strict resource type plan profile %q", profile)
	}

	parentCheckEvery := (*atc.CheckEvery)(nil)
	if profile == "interval" {
		parentCheckEvery = &atc.CheckEvery{Interval: parentInterval}
	}
	custom := atc.ResourceType{Name: "some-custom-resource-type", Type: "some-resource-type", Tags: tags, Source: atc.Source{"some": "source"}}
	parent := atc.ResourceType{
		Name: "some-resource-type", Type: "some-base-resource-type", Source: atc.Source{"some": "type-source"},
		Privileged: privileged, CheckEvery: parentCheckEvery,
	}
	pipeline, err := resourceTypePipeline(database, "strict-type-plan-"+profile, atc.Config{ResourceTypes: atc.ResourceTypes{custom, parent}})
	if err != nil {
		return "", err
	}
	resourceType, found, err := pipeline.ResourceType(custom.Name)
	if err != nil || !found {
		return "", fmt.Errorf("load custom plan resource type: found=%t: %w", found, err)
	}
	version := atc.Version{"version": "from"}
	actual := resourceType.CheckPlan(
		atc.NewPlanFactory(0), atc.ResourceTypes{parent}, version,
		atc.CheckEvery{Interval: time.Hour}, nil, skipInterval, skipRecursively,
	)
	checkID := atc.PlanID("1/image-check")
	expected := atc.Plan{ID: "1", Check: &atc.CheckPlan{
		Name: custom.Name, Type: custom.Type, Source: custom.Source, Tags: tags,
		TypeImage: atc.TypeImage{
			BaseType: parent.Type, Privileged: privileged,
			CheckPlan: &atc.Plan{ID: checkID, Check: &atc.CheckPlan{
				Name: parent.Name, ResourceType: parent.Name, Type: parent.Type,
				Interval: atc.CheckEvery{Interval: parentInterval}, Source: parent.Source,
				SkipInterval: skipInterval && skipRecursively,
				TypeImage:    atc.TypeImage{BaseType: parent.Type}, Tags: tags,
			}},
			GetPlan: &atc.Plan{ID: "1/image-get", Get: &atc.GetPlan{
				Name: parent.Name, Type: parent.Type, Source: parent.Source,
				TypeImage: atc.TypeImage{BaseType: parent.Type}, Tags: tags, VersionFrom: &checkID,
			}},
		},
		FromVersion: version, ResourceType: custom.Name, Interval: atc.CheckEvery{Interval: time.Hour}, SkipInterval: skipInterval,
	}}
	return fmt.Sprintf("exact=%t", reflect.DeepEqual(actual, expected)), nil
}

func observeStrictResourceTypeClear(database JetbridgeDB, profile string) (string, error) {
	atc.EnableGlobalResources = profile == "shared"
	defer func() { atc.EnableGlobalResources = false }()
	pipeline, err := resourceTypePipeline(database, "strict-type-clear-"+profile, atc.Config{ResourceTypes: atc.ResourceTypes{
		{Name: "one", Type: "registry-image", Source: atc.Source{"repository": "shared"}},
		{Name: "two", Type: "registry-image", Source: atc.Source{"repository": "shared"}},
	}})
	if err != nil {
		return "", err
	}
	one, found, err := pipeline.ResourceType("one")
	if err != nil || !found {
		return "", fmt.Errorf("load strict clear type one: found=%t: %w", found, err)
	}
	if profile == "zero" {
		deleted, err := one.ClearVersions()
		return fmt.Sprintf("deleted=%d", deleted), err
	}
	two, found, err := pipeline.ResourceType("two")
	if err != nil || !found {
		return "", fmt.Errorf("load strict clear type two: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(one.Type(), one.Source(), nil)
	if err != nil {
		return "", err
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if err := one.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if profile == "shared" {
		if err := two.SetResourceConfigScope(scope); err != nil {
			return "", err
		}
	}
	if _, err := one.Reload(); err != nil {
		return "", err
	}
	if _, err := two.Reload(); err != nil {
		return "", err
	}
	versions := []atc.Version{{"ref": "v0"}, {"ref": "v1"}, {"ref": "v2"}}
	if err := scope.SaveVersions(db.SpanContext{}, versions); err != nil {
		return "", err
	}
	deleted, err := one.ClearVersions()
	if err != nil {
		return "", err
	}
	absent := true
	for _, version := range versions {
		_, found, findErr := scope.FindVersion(version)
		if findErr != nil {
			return "", findErr
		}
		absent = absent && !found
	}
	if profile == "history" {
		return fmt.Sprintf("deleted=%d;absent=%t", deleted, absent), nil
	}
	otherConfig, found, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindResourceConfigByID(two.ResourceConfigID())
	if err != nil || !found {
		return "", fmt.Errorf("load shared resource config: found=%t: %w", found, err)
	}
	otherScope, err := otherConfig.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	sharedAbsent := true
	for _, version := range versions {
		_, found, findErr := otherScope.FindVersion(version)
		if findErr != nil {
			return "", findErr
		}
		sharedAbsent = sharedAbsent && !found
	}
	return fmt.Sprintf("deleted=%d;shared-absent=%t", deleted, sharedAbsent), nil
}

func observeResourceTypeDomain(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "strict-collection":
		return observeStrictResourceTypeCollection(database)
	case "strict-inactive":
		return observeStrictResourceTypeInactive(database)
	case "strict-filter-same-name":
		return observeStrictResourceTypeFilterSameName(database)
	case "strict-filter-dependency":
		return observeStrictResourceTypeFilterDependency(database)
	case "strict-deserialize-parent":
		return observeStrictResourceTypeDeserialize(database, false)
	case "strict-deserialize-base":
		return observeStrictResourceTypeDeserialize(database, true)
	case "strict-scope":
		return observeStrictResourceTypeScope(database)
	case "strict-build-created", "strict-build-events", "strict-build-trace", "strict-build-blocked", "strict-build-manual", "strict-build-after":
		return observeStrictResourceTypeBuild(database, strings.TrimPrefix(profile, "strict-build-"))
	case "strict-plan-base", "strict-plan-custom", "strict-plan-interval", "strict-plan-privileged", "strict-plan-local-skip", "strict-plan-recursive-skip":
		return observeStrictResourceTypePlan(database, strings.TrimPrefix(profile, "strict-plan-"))
	case "strict-clear-zero", "strict-clear-history", "strict-clear-shared":
		return observeStrictResourceTypeClear(database, strings.TrimPrefix(profile, "strict-clear-"))
	case "collection":
		return observeResourceTypeCollection(database)
	case "filter":
		return observeResourceTypeFilter(database)
	case "scope":
		pipeline, err := resourceTypePipeline(database, "type-scope", atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "type", Type: "registry-image", Source: atc.Source{"repository": "example/type"}}}})
		if err != nil {
			return "", err
		}
		resourceType, found, err := pipeline.ResourceType("type")
		if err != nil || !found {
			return "", fmt.Errorf("load scoped resource type: found=%t: %w", found, err)
		}
		config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resourceType.Type(), resourceType.Source(), nil)
		if err != nil {
			return "", err
		}
		scope, err := config.FindOrCreateScope(nil)
		if err != nil {
			return "", err
		}
		if err := resourceType.SetResourceConfigScope(scope); err != nil {
			return "", err
		}
		if _, err := resourceType.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("scope=%t", resourceType.ResourceConfigScopeID() == scope.ID()), nil
	case "build":
		return observeResourceTypeBuild(database)
	case "plans":
		return observeResourceTypePlans(database)
	case "clear":
		return observeResourceTypeClear(database)
	default:
		return "", fmt.Errorf("unknown resource type domain profile %q", profile)
	}
}

func observeResourceTypeCollection(database JetbridgeDB) (string, error) {
	config := atc.Config{ResourceTypes: atc.ResourceTypes{
		{Name: "base-child", Type: "registry-image", Source: atc.Source{"repository": "one"}, Defaults: atc.Source{"default": "one"}},
		{Name: "nested", Type: "base-child", Source: atc.Source{"repository": "two"}, Privileged: true},
		{Name: "params", Type: "s3", Source: atc.Source{"bucket": "bucket"}, Params: atc.Params{"unpack": "true"}},
		{Name: "interval", Type: "registry-image", Source: atc.Source{"repository": "three"}, CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond}},
	}}
	pipeline, err := resourceTypePipeline(database, "type-collection", config)
	if err != nil {
		return "", err
	}
	types, err := pipeline.ResourceTypes()
	if err != nil {
		return "", err
	}
	fieldsOK := len(types) == 4
	for _, item := range types {
		switch item.Name() {
		case "nested":
			fieldsOK = fieldsOK && item.Privileged()
		case "params":
			fieldsOK = fieldsOK && item.Params()["unpack"] == "true"
		case "interval":
			fieldsOK = fieldsOK && item.CheckEvery().Interval == 10*time.Millisecond
		}
	}
	deserialized := types.Deserialize()
	merged := ""
	for _, item := range deserialized {
		if item.Name == "nested" {
			merged, _ = item.Source["default"].(string)
		}
	}
	atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{"s3": {"base": "value"}})
	withBase := types.Deserialize()
	atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
	baseDefault := ""
	for _, item := range withBase {
		if item.Name == "params" {
			baseDefault, _ = item.Source["base"].(string)
		}
	}
	team, found, err := database.TeamFactory.FindTeam("type-collection-team")
	if err != nil || !found {
		return "", fmt.Errorf("reload resource type collection team: found=%t: %w", found, err)
	}
	pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "type-collection"}, atc.Config{ResourceTypes: atc.ResourceTypes{config.ResourceTypes[0]}}, pipeline.ConfigVersion(), false)
	if err != nil {
		return "", err
	}
	active, err := pipeline.ResourceTypes()
	return fmt.Sprintf("count=%d;fields=%t;merged=%s;base=%s;active=%d", len(types), fieldsOK, merged, baseDefault, len(active)), err
}

func observeResourceTypeFilter(database JetbridgeDB) (string, error) {
	pipeline, err := resourceTypePipeline(database, "type-filter", atc.Config{
		Resources: atc.ResourceConfigs{{Name: "resource", Type: "leaf"}},
		ResourceTypes: atc.ResourceTypes{
			{Name: "leaf", Type: "middle"}, {Name: "middle", Type: "root"}, {Name: "root", Type: "registry-image"}, {Name: "unused", Type: "registry-image"},
		},
	})
	if err != nil {
		return "", err
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return "", fmt.Errorf("load filter resource: found=%t: %w", found, err)
	}
	types, err := pipeline.ResourceTypes()
	if err != nil {
		return "", err
	}
	tree := types.Filter(resource)
	names := make([]string, len(tree))
	for i, item := range tree {
		names[i] = item.Name()
	}
	return fmt.Sprintf("tree=%s;count=%d", strings.Join(names, ","), len(tree)), nil
}

func observeResourceTypeBuild(database JetbridgeDB) (string, error) {
	pipeline, err := resourceTypePipeline(database, "type-build", atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "type", Type: "registry-image"}}})
	if err != nil {
		return "", err
	}
	resourceType, found, err := pipeline.ResourceType("type")
	if err != nil || !found {
		return "", fmt.Errorf("load build resource type: found=%t: %w", found, err)
	}
	plan := atc.Plan{ID: "plan", Check: &atc.CheckPlan{Name: "type"}}
	provider := sdktrace.NewTracerProvider()
	tracing.ConfigureTraceProvider(provider)
	ctx, span := tracing.StartSpan(context.Background(), "resource-type-build", nil)
	defer func() {
		span.End()
		tracing.Configured = false
	}()
	first, created, err := resourceType.CreateBuild(ctx, false, plan)
	if err != nil || !created {
		return "", fmt.Errorf("create first resource type build: created=%t: %w", created, err)
	}
	if err := first.SaveEvent(event.Log{Payload: "line"}); err != nil {
		return "", err
	}
	started := first.Status() == db.BuildStatusStarted
	_, blocked, err := resourceType.CreateBuild(context.Background(), false, plan)
	if err != nil {
		return "", err
	}
	manual, manualCreated, err := resourceType.CreateBuild(context.Background(), true, plan)
	if err != nil {
		return "", err
	}
	if err := first.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	if err := manual.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	after, afterCreated, err := resourceType.CreateBuild(context.Background(), false, plan)
	if err != nil {
		return "", err
	}
	source, err := first.Events(0)
	if err != nil {
		return "", err
	}
	count := 0
	for {
		_, nextErr := source.Next()
		if nextErr != nil {
			break
		}
		count++
	}
	_ = source.Close()
	traceParent := first.SpanContext().Get("traceparent")
	return fmt.Sprintf("created=%t;started=%t;events=%d;trace=%t;blocked=%t;manual=%t;after=%t;ids=%t", created, started, count, strings.Contains(traceParent, span.SpanContext().TraceID().String()), blocked, manualCreated && manual.IsManuallyTriggered(), afterCreated, first.ResourceTypeID() == resourceType.ID() && after.ResourceTypeID() == resourceType.ID()), nil
}

func observeResourceTypePlans(database JetbridgeDB) (string, error) {
	basePipeline, err := resourceTypePipeline(database, "type-plan-base", atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "base", Type: "registry-image", Source: atc.Source{"repository": "base"}}}})
	if err != nil {
		return "", err
	}
	base, found, err := basePipeline.ResourceType("base")
	if err != nil || !found {
		return "", fmt.Errorf("load base resource type: found=%t: %w", found, err)
	}
	basePlan := base.CheckPlan(atc.NewPlanFactory(0), atc.ResourceTypes{}, atc.Version{"ref": "from"}, atc.CheckEvery{Interval: time.Hour}, atc.Source{"default": "value"}, false, false)

	customPipeline, err := resourceTypePipeline(database, "type-plan-custom", atc.Config{ResourceTypes: atc.ResourceTypes{
		{Name: "custom", Type: "parent", Source: atc.Source{"custom": "source"}},
		{Name: "parent", Type: "registry-image", Source: atc.Source{"repository": "parent"}, Privileged: true, CheckEvery: &atc.CheckEvery{Interval: 2 * time.Minute}},
	}})
	if err != nil {
		return "", err
	}
	custom, found, err := customPipeline.ResourceType("custom")
	if err != nil || !found {
		return "", fmt.Errorf("load custom resource type: found=%t: %w", found, err)
	}
	parents := atc.ResourceTypes{{Name: "parent", Type: "registry-image", Source: atc.Source{"repository": "parent"}, Privileged: true, CheckEvery: &atc.CheckEvery{Interval: 2 * time.Minute}}}
	customPlan := custom.CheckPlan(atc.NewPlanFactory(0), parents, nil, atc.CheckEvery{Interval: time.Hour}, nil, false, false)
	localSkip := custom.CheckPlan(atc.NewPlanFactory(0), parents, nil, atc.CheckEvery{Interval: time.Hour}, nil, true, false)
	recursiveSkip := custom.CheckPlan(atc.NewPlanFactory(0), parents, nil, atc.CheckEvery{Interval: time.Hour}, nil, true, true)
	image := customPlan.Check.TypeImage
	return fmt.Sprintf("base=%t;nested=%t;interval=%t;privileged=%t;local=%t;recursive=%t",
		basePlan.Check.TypeImage.BaseType == "registry-image" && basePlan.Check.Source["default"] == "value",
		image.CheckPlan != nil && image.GetPlan != nil,
		image.CheckPlan.Check.Interval.Interval == 2*time.Minute,
		image.Privileged,
		localSkip.Check.SkipInterval && !localSkip.Check.TypeImage.CheckPlan.Check.SkipInterval,
		recursiveSkip.Check.SkipInterval && recursiveSkip.Check.TypeImage.CheckPlan.Check.SkipInterval), nil
}

func observeResourceTypeClear(database JetbridgeDB) (string, error) {
	pipeline, err := resourceTypePipeline(database, "type-clear", atc.Config{ResourceTypes: atc.ResourceTypes{
		{Name: "one", Type: "registry-image", Source: atc.Source{"repository": "shared"}},
		{Name: "two", Type: "registry-image", Source: atc.Source{"repository": "shared"}},
	}})
	if err != nil {
		return "", err
	}
	one, found, err := pipeline.ResourceType("one")
	if err != nil || !found {
		return "", fmt.Errorf("load clear type one: found=%t: %w", found, err)
	}
	two, found, err := pipeline.ResourceType("two")
	if err != nil || !found {
		return "", fmt.Errorf("load clear type two: found=%t: %w", found, err)
	}
	zero, err := one.ClearVersions()
	if err != nil {
		return "", err
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(one.Type(), one.Source(), nil)
	if err != nil {
		return "", err
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if err := one.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if err := two.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if _, err := one.Reload(); err != nil {
		return "", err
	}
	if _, err := two.Reload(); err != nil {
		return "", err
	}
	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "1"}, {"ref": "2"}}); err != nil {
		return "", err
	}
	removed, err := one.ClearVersions()
	if err != nil {
		return "", err
	}
	_, found, err = scope.LatestVersion()
	return fmt.Sprintf("zero=%d;removed=%d;shared-empty=%t", zero, removed, !found), err
}
