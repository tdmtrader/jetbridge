package steps

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/util"
)

type DBResourceFinalObservation struct{ Value string }

func DBResourceFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceFinalObservation](
			"the remaining real resource domain evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceFinalObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBResourceFinal(database, profile)
				return DBResourceFinalObservation{Value: value}, err
			},
		),
		CheckString[DBResourceFinalObservation]("the remaining resource domain result is {string}", "remaining resource domain result", func(in DBResourceFinalObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func dbResourceFinalPipeline(database JetbridgeDB, name string, config atc.Config) (db.Pipeline, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: name + "-team"})
	if err != nil {
		return nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, config, 0, false)
	return pipeline, err
}

func dbResourceFinalOne(database JetbridgeDB, name string, config atc.Config, resourceName string) (db.Pipeline, db.Resource, error) {
	pipeline, err := dbResourceFinalPipeline(database, name, config)
	if err != nil {
		return nil, nil, err
	}
	resource, found, err := pipeline.Resource(resourceName)
	if err != nil || !found {
		return nil, nil, fmt.Errorf("load resource %q: found=%t: %w", resourceName, found, err)
	}
	return pipeline, resource, nil
}

func dbResourceFinalScope(database JetbridgeDB, resource db.Resource) (db.ResourceConfigScope, error) {
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return nil, err
	}
	return config.FindOrCreateScope(db.NewIntPtr(resource.ID()))
}

func observeDBResourceFinalScope(database JetbridgeDB, profile string) (string, error) {
	config := atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/resource"}},
			{Name: "other", Type: "registry-image", Source: atc.Source{"repository": "example/other"}},
		},
		Jobs: atc.JobConfigs{
			{Name: "consumer", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "resource"}}}},
			{Name: "unrelated", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "other"}}}},
		},
	}
	pipeline, resource, err := dbResourceFinalOne(database, "db-resource-scope-"+profile, config, "resource")
	if err != nil {
		return "", err
	}
	scope, err := dbResourceFinalScope(database, resource)
	if err != nil {
		return "", err
	}
	if profile == "association" {
		before := resource.ResourceConfigID() == 0 && resource.ResourceConfigScopeID() == 0
		if err := resource.SetResourceConfigScope(scope); err != nil {
			return "", err
		}
		if _, err := resource.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("before-zero=%t;after-equal=%t", before, resource.ResourceConfigID() == scope.ResourceConfig().ID() && resource.ResourceConfigScopeID() == scope.ID()), nil
	}
	consumer, found, err := pipeline.Job("consumer")
	if err != nil || !found {
		return "", fmt.Errorf("load consumer: found=%t: %w", found, err)
	}
	unrelated, found, err := pipeline.Job("unrelated")
	if err != nil || !found {
		return "", fmt.Errorf("load unrelated: found=%t: %w", found, err)
	}
	consumerBefore, unrelatedBefore := consumer.ScheduleRequestedTime(), unrelated.ScheduleRequestedTime()
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if found, err := consumer.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload consumer: found=%t: %w", found, err)
	}
	if found, err := unrelated.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload unrelated: found=%t: %w", found, err)
	}
	return fmt.Sprintf("consumer=%t;unrelated=%t", consumer.ScheduleRequestedTime().After(consumerBefore), unrelated.ScheduleRequestedTime().After(unrelatedBefore)), nil
}

func dbResourceFinalBuildResource(database JetbridgeDB, name string) (db.Resource, error) {
	_, resource, err := dbResourceFinalOne(database, "db-resource-build-"+name, atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image"}}}, "resource")
	return resource, err
}

func dbResourceFinalBuildPlan() atc.Plan {
	return atc.Plan{ID: "some-plan", Check: &atc.CheckPlan{Name: "wreck"}}
}

func dbResourceFinalEventCount(database JetbridgeDB, build db.Build) (int, error) {
	var count int
	err := database.Conn.QueryRow("SELECT COUNT(*) FROM check_build_events WHERE build_id = $1", build.ID()).Scan(&count)
	return count, err
}

func observeDBResourceFinalBuild(database JetbridgeDB, profile string) (string, error) {
	resource, err := dbResourceFinalBuildResource(database, profile)
	if err != nil {
		return "", err
	}
	plan := dbResourceFinalBuildPlan()
	if profile == "created" || profile == "events" {
		build, created, err := resource.CreateBuild(context.Background(), false, plan)
		if err != nil || !created || build == nil {
			return "", fmt.Errorf("create resource build: created=%t nil=%t: %w", created, build == nil, err)
		}
		if profile == "created" {
			exact := build.Name() == db.CheckBuildName && build.ResourceID() == resource.ID() && build.PipelineID() == resource.PipelineID() && build.TeamID() == resource.TeamID() && !build.IsManuallyTriggered() && build.Status() == db.BuildStatusStarted && reflect.DeepEqual(build.PrivatePlan(), plan)
			return fmt.Sprintf("exact=%t", exact), nil
		}
		if err := build.SaveEvent(event.Log{Payload: "log"}); err != nil {
			return "", err
		}
		count, err := dbResourceFinalEventCount(database, build)
		return fmt.Sprintf("events=%d", count), err
	}
	completed, created, err := resource.CreateBuild(context.Background(), false, plan)
	if err != nil || !created || completed == nil {
		return "", fmt.Errorf("create completed predecessor: created=%t nil=%t: %w", created, completed == nil, err)
	}
	if err := completed.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	running, created, err := resource.CreateBuild(context.Background(), false, plan)
	if err != nil || !created || running == nil {
		return "", fmt.Errorf("create running predecessor: created=%t nil=%t: %w", created, running == nil, err)
	}
	switch profile {
	case "blocked":
		build, secondCreated, err := resource.CreateBuild(context.Background(), false, plan)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("created=%t;nil=%t", secondCreated, build == nil), nil
	case "manual":
		build, manualCreated, err := resource.CreateBuild(context.Background(), true, plan)
		if err != nil || build == nil {
			return "", fmt.Errorf("create manual resource build: created=%t nil=%t: %w", manualCreated, build == nil, err)
		}
		return fmt.Sprintf("created=%t;manual=%t;resource-id=%t", manualCreated, build.IsManuallyTriggered(), build.ResourceID() == resource.ID()), nil
	case "after":
		if err := running.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		build, afterCreated, err := resource.CreateBuild(context.Background(), false, plan)
		if err != nil || build == nil {
			return "", fmt.Errorf("create resource build after finish: created=%t nil=%t: %w", afterCreated, build == nil, err)
		}
		return fmt.Sprintf("created=%t;resource-id=%t", afterCreated, build.ResourceID() == resource.ID()), nil
	default:
		return "", fmt.Errorf("unknown resource build profile %q", profile)
	}
}

func observeDBResourceFinalMemory(database JetbridgeDB, profile string) (string, error) {
	resource, err := dbResourceFinalBuildResource(database, "memory-"+profile)
	if err != nil {
		return "", err
	}
	plan := dbResourceFinalBuildPlan()
	build, err := resource.CreateInMemoryBuild(context.Background(), plan, util.NewSequenceGenerator(1))
	if err != nil {
		return "", err
	}
	if profile == "created" {
		exact := build != nil && build.ID() == 0 && build.Name() == db.CheckBuildName && build.ResourceID() == resource.ID() && build.PipelineID() == resource.PipelineID() && build.TeamID() == resource.TeamID() && !build.IsManuallyTriggered() && reflect.DeepEqual(build.PrivatePlan(), plan)
		return fmt.Sprintf("exact=%t", exact), nil
	}
	if err := build.SaveEvent(event.Log{Payload: "log"}); err != nil {
		return "", err
	}
	count, err := dbResourceFinalEventCount(database, build)
	return fmt.Sprintf("events=%d", count), err
}

func observeDBResourceFinalClear(database JetbridgeDB, profile string) (string, error) {
	atc.EnableGlobalResources = profile == "shared"
	defer func() { atc.EnableGlobalResources = false }()
	pipeline, resource, err := dbResourceFinalOne(database, "db-resource-clear-"+profile, atc.Config{Resources: atc.ResourceConfigs{
		{Name: "one", Type: "registry-image", Source: atc.Source{"repository": "shared"}},
		{Name: "two", Type: "registry-image", Source: atc.Source{"repository": "shared"}},
	}}, "one")
	if err != nil {
		return "", err
	}
	if profile == "zero" {
		deleted, err := resource.ClearVersions()
		return fmt.Sprintf("deleted=%d", deleted), err
	}
	other, found, err := pipeline.Resource("two")
	if err != nil || !found {
		return "", fmt.Errorf("load second resource: found=%t: %w", found, err)
	}
	scope, err := dbResourceFinalScope(database, resource)
	if err != nil {
		return "", err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if profile == "shared" {
		if err := other.SetResourceConfigScope(scope); err != nil {
			return "", err
		}
	}
	if _, err := resource.Reload(); err != nil {
		return "", err
	}
	if _, err := other.Reload(); err != nil {
		return "", err
	}
	versions := []atc.Version{{"ref": "v0"}, {"ref": "v1"}, {"ref": "v2"}}
	if err := scope.SaveVersions(db.SpanContext{}, versions); err != nil {
		return "", err
	}
	if profile == "state" {
		v0, found, err := scope.FindVersion(versions[0])
		if err != nil || !found {
			return "", fmt.Errorf("find v0: found=%t: %w", found, err)
		}
		v1, found, err := scope.FindVersion(versions[1])
		if err != nil || !found {
			return "", fmt.Errorf("find v1: found=%t: %w", found, err)
		}
		if err := resource.DisableVersion(v0.ID()); err != nil {
			return "", err
		}
		if pinned, err := resource.PinVersion(v1.ID()); err != nil || !pinned {
			return "", fmt.Errorf("pin v1: pinned=%t: %w", pinned, err)
		}
	}
	deleted, err := resource.ClearVersions()
	if err != nil {
		return "", err
	}
	if profile == "state" {
		if err := scope.SaveVersions(db.SpanContext{}, versions); err != nil {
			return "", err
		}
		page, _, _, err := resource.Versions(db.Page{Limit: 5}, nil)
		if err != nil {
			return "", err
		}
		disabled := false
		for _, version := range page {
			if reflect.DeepEqual(version.Version, versions[0]) {
				disabled = !version.Enabled
			}
		}
		if _, err := resource.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted=%d;disabled=%t;pinned=%t", deleted, disabled, reflect.DeepEqual(resource.CurrentPinnedVersion(), versions[1])), nil
	}
	absent := true
	for _, version := range versions {
		_, found, err := scope.FindVersion(version)
		if err != nil {
			return "", err
		}
		absent = absent && !found
	}
	if profile == "history" {
		return fmt.Sprintf("deleted=%d;absent=%t", deleted, absent), nil
	}
	otherScope, err := dbResourceFinalScope(database, other)
	if err != nil {
		return "", err
	}
	otherAbsent := true
	for _, version := range versions {
		_, found, err := otherScope.FindVersion(version)
		if err != nil {
			return "", err
		}
		otherAbsent = otherAbsent && !found
	}
	return fmt.Sprintf("deleted=%d;both-absent=%t", deleted, absent && otherAbsent), nil
}

func observeDBResourceFinalPlan(database JetbridgeDB, profile string) (string, error) {
	atc.DefaultCheckInterval = time.Minute
	defer func() { atc.DefaultCheckInterval = 0 }()
	if profile == "base" {
		_, resource, err := dbResourceFinalOne(database, "db-resource-plan-base", atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Tags: atc.Tags{"tag"}, CheckTimeout: "1h", Source: atc.Source{"some": "source"}}}}, "resource")
		if err != nil {
			return "", err
		}
		version := atc.Version{"version": "from"}
		actual := resource.CheckPlan(atc.NewPlanFactory(0), atc.ResourceTypes{}, version, atc.CheckEvery{Interval: time.Hour}, atc.Source{"source-test": "default"}, false, false)
		expected := atc.Plan{ID: "1", Check: &atc.CheckPlan{Name: "resource", Type: "registry-image", Source: atc.Source{"some": "source", "source-test": "default"}, Tags: atc.Tags{"tag"}, Timeout: "1h", TypeImage: atc.TypeImage{BaseType: "registry-image"}, FromVersion: version, Resource: "resource", Interval: atc.CheckEvery{Interval: time.Hour}}}
		return fmt.Sprintf("exact=%t", reflect.DeepEqual(actual, expected)), nil
	}
	parentInterval := time.Minute
	privileged, skip, recursive := false, false, false
	tags := atc.Tags(nil)
	switch profile {
	case "custom":
		tags = atc.Tags{"tag"}
	case "interval":
		tags, parentInterval = atc.Tags{"tag"}, 2*time.Minute
	case "privileged":
		privileged = true
	case "local-skip":
		skip = true
	case "recursive-skip":
		skip, recursive = true, true
	default:
		return "", fmt.Errorf("unknown resource plan profile %q", profile)
	}
	checkEvery := (*atc.CheckEvery)(nil)
	if profile == "interval" {
		checkEvery = &atc.CheckEvery{Interval: parentInterval}
	}
	resourceConfig := atc.ResourceConfig{Name: "resource", Type: "custom-type", Source: atc.Source{"some": "source"}, Tags: tags}
	parent := atc.ResourceType{Name: "custom-type", Type: "registry-image", Source: atc.Source{"some": "type-source"}, Privileged: privileged, CheckEvery: checkEvery}
	_, resource, err := dbResourceFinalOne(database, "db-resource-plan-"+profile, atc.Config{Resources: atc.ResourceConfigs{resourceConfig}, ResourceTypes: atc.ResourceTypes{parent}}, "resource")
	if err != nil {
		return "", err
	}
	version := atc.Version{"version": "from"}
	actual := resource.CheckPlan(atc.NewPlanFactory(0), atc.ResourceTypes{parent}, version, atc.CheckEvery{Interval: time.Hour}, nil, skip, recursive)
	checkID := atc.PlanID("1/image-check")
	expected := atc.Plan{ID: "1", Check: &atc.CheckPlan{Name: "resource", Type: "custom-type", Source: resourceConfig.Source, Tags: tags, TypeImage: atc.TypeImage{BaseType: parent.Type, Privileged: privileged, CheckPlan: &atc.Plan{ID: checkID, Check: &atc.CheckPlan{Name: parent.Name, ResourceType: parent.Name, Type: parent.Type, Interval: atc.CheckEvery{Interval: parentInterval}, Source: parent.Source, SkipInterval: skip && recursive, TypeImage: atc.TypeImage{BaseType: parent.Type}, Tags: tags}}, GetPlan: &atc.Plan{ID: "1/image-get", Get: &atc.GetPlan{Name: parent.Name, Type: parent.Type, Source: parent.Source, TypeImage: atc.TypeImage{BaseType: parent.Type}, Tags: tags, VersionFrom: &checkID, SkipDownload: true}}}, FromVersion: version, Resource: "resource", Interval: atc.CheckEvery{Interval: time.Hour}, SkipInterval: skip}}
	return fmt.Sprintf("exact=%t", reflect.DeepEqual(actual, expected)), nil
}

func observeDBResourceFinalSummary(database JetbridgeDB, profile string) (string, error) {
	_, resource, err := dbResourceFinalOne(database, "db-resource-summary-"+profile, atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/resource"}}}}, "resource")
	if err != nil {
		return "", err
	}
	scope, err := dbResourceFinalScope(database, resource)
	if err != nil {
		return "", err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	if profile == "empty" {
		return fmt.Sprintf("nil=%t", resource.BuildSummary() == nil), nil
	}
	plan := atc.Plan{ID: "1234", Check: &atc.CheckPlan{Name: "resource", Type: "registry-image"}}
	build, err := resource.CreateInMemoryBuild(context.Background(), plan, util.NewSequenceGenerator(1))
	if err != nil {
		return "", err
	}
	if err := build.OnCheckBuildStart(); err != nil {
		return "", err
	}
	if profile == "failed" || profile == "shared" || profile == "newest" {
		if _, err := scope.UpdateLastCheckStartTime(build.ID(), build.PublicPlan()); err != nil {
			return "", err
		}
		if _, err := scope.UpdateLastCheckEndTime(false); err != nil {
			return "", err
		}
	}
	if profile == "shared" || profile == "newest" {
		if _, err := scope.UpdateLastCheckStartTime(999999, build.PublicPlan()); err != nil {
			return "", err
		}
		if _, err := scope.UpdateLastCheckEndTime(true); err != nil {
			return "", err
		}
	}
	expectedID, expectedStatus, expectEnd := build.ID(), atc.StatusStarted, false
	if profile == "failed" {
		expectedStatus, expectEnd = atc.StatusFailed, true
	}
	if profile == "shared" {
		expectedID, expectedStatus, expectEnd = 999999, atc.StatusSucceeded, true
	}
	if profile == "newest" {
		newBuild, err := resource.CreateInMemoryBuild(context.Background(), plan, util.NewSequenceGenerator(2))
		if err != nil {
			return "", err
		}
		if err := newBuild.OnCheckBuildStart(); err != nil {
			return "", err
		}
		expectedID, expectedStatus, expectEnd = newBuild.ID(), atc.StatusStarted, false
	}
	if _, err := resource.Reload(); err != nil {
		return "", err
	}
	summary := resource.BuildSummary()
	exact := summary != nil && summary.ID == expectedID && summary.Status == expectedStatus && time.Since(time.Unix(summary.StartTime, 0)) < 2*time.Second && (summary.EndTime != 0) == expectEnd && reflect.DeepEqual(summary.PublicPlan, build.PublicPlan())
	return fmt.Sprintf("exact=%t", exact), nil
}

func observeDBResourceFinal(database JetbridgeDB, profile string) (string, error) {
	switch {
	case profile == "scope-association":
		return observeDBResourceFinalScope(database, "association")
	case profile == "scope-schedule":
		return observeDBResourceFinalScope(database, "schedule")
	case len(profile) > 6 && profile[:6] == "build-":
		return observeDBResourceFinalBuild(database, profile[6:])
	case len(profile) > 7 && profile[:7] == "memory-":
		return observeDBResourceFinalMemory(database, profile[7:])
	case len(profile) > 6 && profile[:6] == "clear-":
		return observeDBResourceFinalClear(database, profile[6:])
	case len(profile) > 5 && profile[:5] == "plan-":
		return observeDBResourceFinalPlan(database, profile[5:])
	case len(profile) > 8 && profile[:8] == "summary-":
		return observeDBResourceFinalSummary(database, profile[8:])
	default:
		return "", fmt.Errorf("unknown remaining resource profile %q", profile)
	}
}
