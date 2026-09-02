package steps

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBCheckFactoryObservation struct{ Value string }

func DBCheckFactoryStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBCheckFactoryObservation](
			"the production check factory evaluates profile {string}", []string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBCheckFactoryObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBCheckFactoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBCheckFactory(database, profile)
				return DBCheckFactoryObservation{Value: value}, err
			},
		),
		brine.DefineCheck[DBCheckFactoryObservation]("the check factory observation is {string}", func(in DBCheckFactoryObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if in.Value != want {
				return fmt.Errorf("expected check factory observation %q, got %q", want, in.Value)
			}
			return nil
		}),
	}
}

func observeDBCheckFactory(database JetbridgeDB, profile string) (string, error) {
	switch {
	case strings.HasPrefix(profile, "resources-"):
		return observeCheckFactoryResources(database, profile)
	case strings.HasPrefix(profile, "resource-types-"):
		return observeCheckFactoryResourceTypes(database, profile)
	default:
		return observeCheckFactoryBuild(database, profile)
	}
}

func observeCheckFactoryBuild(database JetbridgeDB, profile string) (string, error) {
	atc.DefaultCheckInterval = time.Minute
	atc.DefaultWebhookInterval = time.Hour
	atc.DefaultResourceTypeInterval = time.Hour
	defer func() {
		atc.DefaultCheckInterval = 0
		atc.DefaultWebhookInterval = 0
		atc.DefaultResourceTypeInterval = 0
	}()

	pipeline, resource, resourceType, resourceTypes, err := saveCheckFactoryPipeline(database, profile)
	if err != nil {
		return "", err
	}
	_ = pipeline

	checkable := db.Checkable(resource)
	if strings.HasPrefix(profile, "resource-type-") {
		checkable = resourceType
	}

	manuallyTriggered := profile == "manual-trigger"
	if profile == "interval-skip" || profile == "manual-trigger" || profile == "webhook-skip" {
		ago := atc.DefaultCheckInterval / 2
		if profile == "webhook-skip" {
			ago = atc.DefaultWebhookInterval / 2
		}
		if err := attachAndBackdateCheckScope(database, resource, ago); err != nil {
			return "", err
		}
	}
	if profile == "running-build" {
		_, created, err := resource.CreateBuild(context.Background(), false, atc.Plan{ID: "existing-plan"})
		if err != nil || !created {
			return "", fmt.Errorf("create existing check build: created=%t: %w", created, err)
		}
	}

	factory := db.NewCheckFactory(database.Conn, database.LockFactory, nil, nil)
	build, created, callErr := factory.TryCreateCheck(
		context.Background(), checkable, resourceTypes, atc.Version{"from": "version"}, manuallyTriggered, false, true,
	)

	switch profile {
	case "resource-return":
		return fmt.Sprintf("error=%t;created=%t;id-positive=%t;name=%s;resource-match=%t", callErr != nil, created, build != nil && build.ID() > 0, buildName(build), build != nil && build.ResourceID() == resource.ID()), nil
	case "interval-skip", "webhook-skip", "running-build":
		return fmt.Sprintf("error=%t;created=%t;nil=%t;count=%d", callErr != nil, created, build == nil, countCheckBuilds(database, "resource_id", resource.ID())), nil
	case "manual-trigger":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return fmt.Sprintf("error=%t;created=%t;nil=%t", callErr != nil, created, build == nil), nil
		}
		return fmt.Sprintf("error=%t;created=%t;nil=%t;manual=%t;skip=%t", callErr != nil, created, build == nil, build.IsManuallyTriggered(), plan.Check.SkipInterval), nil
	case "resource-plan":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		return fmt.Sprintf("count=%d;manual=%t;plan-id=%t;name=%s;resource=%s;type=%s;tags=%s;from=%s;interval=%s;skip=%t;source=%s;base=%s",
			countCheckBuilds(database, "resource_id", resource.ID()), build.IsManuallyTriggered(), plan.ID != "", plan.Check.Name, plan.Check.Resource, plan.Check.Type,
			strings.Join(plan.Check.Tags, ","), versionString(plan.Check.FromVersion), plan.Check.Interval.Interval, plan.Check.SkipInterval,
			sourceString(plan.Check.Source), plan.Check.TypeImage.BaseType), nil
	case "webhook-plan", "explicit-interval-plan":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		return fmt.Sprintf("from=%s;interval=%s;source=%s;base=%s", versionString(plan.Check.FromVersion), plan.Check.Interval.Interval, sourceString(plan.Check.Source), plan.Check.TypeImage.BaseType), nil
	case "never-plan":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		return fmt.Sprintf("from=%s;never=%t;source=%s;base=%s", versionString(plan.Check.FromVersion), plan.Check.Interval.Never, sourceString(plan.Check.Source), plan.Check.TypeImage.BaseType), nil
	case "parent-plan":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		image := plan.Check.TypeImage
		return fmt.Sprintf("from=%s;interval=%s;source=%s;base=%s;get=%t;get-name=%s;get-type=%s;get-source=%s;get-tags=%s;check=%t;check-type=%s",
			versionString(plan.Check.FromVersion), plan.Check.Interval.Interval, sourceString(plan.Check.Source), image.BaseType,
			image.GetPlan != nil, imageGetName(image), imageGetType(image), imageGetSource(image), imageGetTags(image), image.CheckPlan != nil, imageCheckType(image)), nil
	case "parent-return":
		return fmt.Sprintf("error=%t;created=%t;id-positive=%t;resource-match=%t", callErr != nil, created, build != nil && build.ID() > 0, build != nil && build.ResourceID() == resource.ID()), nil
	case "parent-start":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		return fmt.Sprintf("manual=%t;plan-id=%t;resource=%s", build.IsManuallyTriggered(), plan.ID != "", plan.Check.Resource), nil
	case "resource-type-plan":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		return fmt.Sprintf("name=%s;resource-type=%s;from=%s;interval=%s;source=%s;base=%s", plan.Check.Name, plan.Check.ResourceType, versionString(plan.Check.FromVersion), plan.Check.Interval.Interval, sourceString(plan.Check.Source), plan.Check.TypeImage.BaseType), nil
	case "resource-type-return":
		return fmt.Sprintf("error=%t;created=%t;id-positive=%t;name=%s", callErr != nil, created, build != nil && build.ID() > 0, buildName(build)), nil
	case "resource-type-start":
		plan, err := persistedCheckPlan(database, build)
		if err != nil {
			return "build-present=false", nil
		}
		return fmt.Sprintf("count=%d;manual=%t;plan-id=%t", countCheckBuilds(database, "resource_type_id", resourceType.ID()), build.IsManuallyTriggered(), plan.ID != ""), nil
	default:
		return "", fmt.Errorf("unknown check build profile %q", profile)
	}
}

func saveCheckFactoryPipeline(database JetbridgeDB, profile string) (db.Pipeline, db.Resource, db.ResourceType, db.ResourceTypes, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "check-factory-team"})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	resourceConfig := atc.ResourceConfig{Name: "some-name", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}, Tags: atc.Tags{"tag-a", "tag-b"}}
	resourceTypeConfig := atc.ResourceType{Name: "some-type", Type: "some-base-type", Source: atc.Source{"some": "type-source"}, Tags: atc.Tags{"some-tag"}, Defaults: atc.Source{"some-default": "some-default-value"}}
	switch profile {
	case "webhook-plan", "webhook-skip":
		resourceConfig.WebhookToken = "some-token"
	case "explicit-interval-plan":
		resourceConfig.CheckEvery = &atc.CheckEvery{Interval: 42 * time.Second}
	case "never-plan":
		resourceConfig.CheckEvery = &atc.CheckEvery{Never: true}
	case "parent-plan", "parent-return", "parent-start":
		resourceConfig.Type = "custom-type"
		resourceTypeConfig = atc.ResourceType{Name: "custom-type", Type: "some-base-type", Source: atc.Source{"some": "type-source"}, Tags: atc.Tags{"some-tag"}, Defaults: atc.Source{"sdk": "sdk"}}
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "check-factory-pipeline"}, atc.Config{Resources: atc.ResourceConfigs{resourceConfig}, ResourceTypes: atc.ResourceTypes{resourceTypeConfig}}, 0, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	resource, found, err := pipeline.Resource("some-name")
	if err != nil || !found {
		return nil, nil, nil, nil, fmt.Errorf("load check resource: found=%t: %w", found, err)
	}
	resourceTypes, err := pipeline.ResourceTypes()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var resourceType db.ResourceType
	for _, candidate := range resourceTypes {
		if candidate.Name() == resourceTypeConfig.Name {
			resourceType = candidate
			break
		}
	}
	if resourceType == nil {
		return nil, nil, nil, nil, fmt.Errorf("resource type %q not found", resourceTypeConfig.Name)
	}
	return pipeline, resource, resourceType, resourceTypes, nil
}

func attachAndBackdateCheckScope(database JetbridgeDB, resource db.Resource, ago time.Duration) error {
	factory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	config, err := factory.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return err
	}
	id := resource.ID()
	scope, err := config.FindOrCreateScope(&id)
	if err != nil {
		return err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return err
	}
	if found, err := scope.UpdateLastCheckStartTime(99, nil); err != nil || !found {
		return fmt.Errorf("record last check start: found=%t: %w", found, err)
	}
	if found, err := scope.UpdateLastCheckEndTime(true); err != nil || !found {
		return fmt.Errorf("record last check end: found=%t: %w", found, err)
	}
	if _, err := database.Conn.Exec(`UPDATE resource_config_scopes SET last_check_start_time = last_check_start_time - $1::interval, last_check_end_time = last_check_end_time - $1::interval WHERE id = $2`, fmt.Sprintf("%d milliseconds", ago.Milliseconds()), scope.ID()); err != nil {
		return err
	}
	_, err = resource.Reload()
	return err
}

func persistedCheckPlan(database JetbridgeDB, build db.Build) (atc.Plan, error) {
	if build == nil {
		return atc.Plan{}, fmt.Errorf("build is nil")
	}
	reloaded, found, err := database.BuildFactory.Build(build.ID())
	if err != nil || !found {
		return atc.Plan{}, fmt.Errorf("reload check build: found=%t: %w", found, err)
	}
	return reloaded.PrivatePlan(), nil
}

func countCheckBuilds(database JetbridgeDB, column string, id int) int {
	var count int
	if err := database.Conn.QueryRow(fmt.Sprintf(`SELECT COUNT(1) FROM builds WHERE %s = $1`, column), id).Scan(&count); err != nil {
		return -1
	}
	return count
}

func buildName(build db.Build) string {
	if build == nil {
		return "<nil>"
	}
	return build.Name()
}

func versionString(version atc.Version) string {
	keys := make([]string, 0, len(version))
	for key := range version {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+version[key])
	}
	return strings.Join(parts, ",")
}

func sourceString(source atc.Source) string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, source[key]))
	}
	return strings.Join(parts, ",")
}

func imageGetName(image atc.TypeImage) string {
	if image.GetPlan == nil || image.GetPlan.Get == nil {
		return ""
	}
	return image.GetPlan.Get.Name
}

func imageGetType(image atc.TypeImage) string {
	if image.GetPlan == nil || image.GetPlan.Get == nil {
		return ""
	}
	return image.GetPlan.Get.Type
}

func imageGetSource(image atc.TypeImage) string {
	if image.GetPlan == nil || image.GetPlan.Get == nil {
		return ""
	}
	return sourceString(image.GetPlan.Get.Source)
}

func imageGetTags(image atc.TypeImage) string {
	if image.GetPlan == nil || image.GetPlan.Get == nil {
		return ""
	}
	return strings.Join(image.GetPlan.Get.Tags, ",")
}

func imageCheckType(image atc.TypeImage) string {
	if image.CheckPlan == nil || image.CheckPlan.Check == nil {
		return ""
	}
	return image.CheckPlan.Check.ResourceType
}

func observeCheckFactoryResources(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "check-resources-team"})
	if err != nil {
		return "", err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "check-resources-pipeline"}, atc.Config{
		Jobs: atc.JobConfigs{{Name: "some-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-resource"}}, {Config: &atc.PutStep{Name: "some-put-only-resource"}}}}},
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}},
			{Name: "some-put-only-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}},
		},
		ResourceTypes: atc.ResourceTypes{{Name: "some-type", Type: "some-base-resource-type", Source: atc.Source{"some-type": "source"}}},
	}, 1, false)
	if err != nil {
		return "", err
	}
	putOnly, found, err := pipeline.Resource("some-put-only-resource")
	if err != nil || !found {
		return "", fmt.Errorf("load put-only resource: found=%t: %w", found, err)
	}
	factory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	config, err := factory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "source"}, nil)
	if err != nil {
		return "", err
	}
	id := putOnly.ID()
	scope, err := config.FindOrCreateScope(&id)
	if err != nil {
		return "", err
	}
	if err := putOnly.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if found, err := scope.UpdateLastCheckStartTime(99, nil); err != nil || !found {
		return "", fmt.Errorf("record put-only check start: found=%t: %w", found, err)
	}
	succeeded := profile != "resources-put-failed"
	if found, err := scope.UpdateLastCheckEndTime(succeeded); err != nil || !found {
		return "", fmt.Errorf("record put-only check end: found=%t: %w", found, err)
	}
	if profile == "resources-inactive" {
		if _, err := database.Conn.Exec(`UPDATE resources SET active = false`); err != nil {
			return "", err
		}
	}
	if profile == "resources-paused" {
		if err := pipeline.Pause("check-factory-user"); err != nil {
			return "", err
		}
	}
	resources, callErr := db.NewCheckFactory(database.Conn, database.LockFactory, nil, nil).Resources()
	if profile == "resources-used" {
		names := make([]string, 0, len(resources))
		for _, resource := range resources {
			names = append(names, resource.Name())
		}
		sort.Strings(names)
		return fmt.Sprintf("count=%d;names=%s;error=%t", len(resources), strings.Join(names, ","), callErr != nil), nil
	}
	return fmt.Sprintf("count=%d;error=%t", len(resources), callErr != nil), nil
}

func observeCheckFactoryResourceTypes(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "check-types-team"})
	if err != nil {
		return "", err
	}
	first, _, err := team.SavePipeline(atc.PipelineRef{Name: "first-pipeline"}, atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "some-type", Type: "some-base-resource-type", Source: atc.Source{"some-type": "source"}}}}, 1, false)
	if err != nil {
		return "", err
	}
	second, _, err := team.SavePipeline(atc.PipelineRef{Name: "second-pipeline"}, atc.Config{ResourceTypes: atc.ResourceTypes{
		{Name: "some-type", Type: "some-base-resource-type", Source: atc.Source{"some-type": "source"}},
		{Name: "some-other-type", Type: "some-base-resource-type", Source: atc.Source{"some-other-type": "source"}},
	}}, 1, false)
	if err != nil {
		return "", err
	}
	if profile == "resource-types-inactive" {
		if _, err := database.Conn.Exec(`UPDATE resource_types SET active = false`); err != nil {
			return "", err
		}
	}
	if profile == "resource-types-paused" {
		if err := first.Pause("check-factory-user"); err != nil {
			return "", err
		}
		if err := second.Pause("check-factory-user"); err != nil {
			return "", err
		}
	}
	byPipeline, callErr := db.NewCheckFactory(database.Conn, database.LockFactory, nil, nil).ResourceTypesByPipeline()
	if profile != "resource-types-list" {
		return fmt.Sprintf("pipelines=%d;error=%t", len(byPipeline), callErr != nil), nil
	}
	firstNames := resourceTypeNames(byPipeline[first.ID()])
	secondNames := resourceTypeNames(byPipeline[second.ID()])
	return fmt.Sprintf("pipelines=%d;first=%s;second=%s;error=%t", len(byPipeline), strings.Join(firstNames, ","), strings.Join(secondNames, ","), callErr != nil), nil
}

func resourceTypeNames(types db.ResourceTypes) []string {
	names := make([]string, 0, len(types))
	for _, resourceType := range types {
		names = append(names, resourceType.Name())
	}
	sort.Strings(names)
	return names
}
