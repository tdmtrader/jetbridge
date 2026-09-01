package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

func observeResourceTypeDomain(database JetbridgeDB, profile string) (string, error) {
	switch profile {
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
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
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
