package steps

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/util"
)

type InMemoryBuildObservation struct{ Value string }

func InMemoryBuildDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, InMemoryBuildObservation](
			"a real in-memory check build evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (InMemoryBuildObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return InMemoryBuildObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeInMemoryBuild(database, profile)
				return InMemoryBuildObservation{Value: value}, err
			},
		),
		CheckString[InMemoryBuildObservation]("the in-memory build result is {string}", "in-memory build result", func(in InMemoryBuildObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func inMemoryBuildFixture(database JetbridgeDB) (db.Resource, atc.Plan, context.Context, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "memory-build-team"})
	if err != nil {
		return nil, atc.Plan{}, nil, err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "memory-build-pipeline"},
		atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}}},
		0, false,
	)
	if err != nil {
		return nil, atc.Plan{}, nil, err
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return nil, atc.Plan{}, nil, fmt.Errorf("load in-memory resource: found=%t: %w", found, err)
	}
	plan := atc.Plan{ID: "1234", Check: &atc.CheckPlan{Name: resource.Name(), Type: resource.Type(), Source: resource.Source()}}
	return resource, plan, context.Background(), nil
}

func observeInMemoryBuild(database JetbridgeDB, profile string) (string, error) {
	resource, plan, ctx, err := inMemoryBuildFixture(database)
	if err != nil {
		return "", err
	}
	build, err := resource.CreateInMemoryBuild(ctx, plan, util.NewSequenceGenerator(1))
	if err != nil {
		return "", err
	}
	switch profile {
	case "creation-identity":
		valid := build.ID() == 0 && build.Name() == db.CheckBuildName &&
			build.TeamID() == resource.TeamID() && build.TeamName() == resource.TeamName() &&
			build.PipelineID() == resource.PipelineID() && build.PipelineName() == resource.PipelineName() &&
			build.JobID() == 0 && build.JobName() == "" && build.ResourceID() == resource.ID() &&
			build.ResourceName() == resource.Name() && build.ResourceTypeID() == 0 && build.Schema() == "exec.v2" &&
			build.Status() == db.BuildStatusPending && build.IsRunning() && !build.IsManuallyTriggered() &&
			build.HasPlan() && reflect.DeepEqual(build.PrivatePlan(), plan) && reflect.DeepEqual(build.PublicPlan(), plan.Public())
		return fmt.Sprintf("creation=%t", valid), nil
	case "creation-lager":
		data := build.LagerData()
		valid := len(data) == 5 && data["team"] == resource.TeamName() && data["pipeline"] == resource.PipelineName() && data["pre_build_id"] == 1 && data["resource"] == resource.Name() && data["build"] == "check"
		return fmt.Sprintf("lager=%t", valid), nil
	case "creation-tracing":
		data := build.TracingAttrs()
		valid := len(data) == 5 && data["team"] == resource.TeamName() && data["pipeline"] == resource.PipelineName() && data["pre_build_id"] == "1" && data["resource"] == resource.Name() && data["build"] == "check"
		return fmt.Sprintf("tracing=%t", valid), nil
	case "creation-span":
		return fmt.Sprintf("span=%t", reflect.DeepEqual(build.SpanContext(), db.NewSpanContext(ctx))), nil
	case "creation-events-error":
		_, err := build.Events(0)
		return fmt.Sprintf("events-error=%t", err != nil && err.Error() == "no build event"), nil
	case "started-events-error":
		if err := saveMemoryStartEvents(build, plan); err != nil {
			return "", err
		}
		_, err := build.Events(0)
		return fmt.Sprintf("events-error=%t", err != nil && err.Error() == "no build event"), nil
	case "prestart-success-id":
		if err := saveMemoryStartEvents(build, plan); err != nil {
			return "", err
		}
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		return fmt.Sprintf("id=%d", build.ID()), nil
	case "prestart-error-summary", "prestart-error-event":
		if err := saveMemoryStartEvents(build, plan); err != nil {
			return "", err
		}
		if err := build.Finish(db.BuildStatusErrored); err != nil {
			return "", err
		}
		if profile == "prestart-error-summary" {
			if _, err := resource.Reload(); err != nil {
				return "", err
			}
			return fmt.Sprintf("summary=%s", resource.BuildSummary().Status), nil
		}
		return memoryBuildEvent(build, 3, "status", "errored")
	case "started-id", "started-summary", "started-lager", "started-tracing", "started-events", "started-log", "started-cache-user", "started-owner", "started-run-state", "started-tracking-lock", "finish-summary", "finish-event":
		return observeStartedInMemoryBuild(resource, plan, build, profile)
	case "api-find", "api-lager", "api-events":
		return observeAPIInMemoryBuild(database, resource, plan, profile)
	default:
		return "", fmt.Errorf("unknown in-memory build profile %q", profile)
	}
}

func saveMemoryStartEvents(build db.Build, plan atc.Plan) error {
	if err := build.SaveEvent(event.Initialize{Origin: event.Origin{ID: event.OriginID(plan.ID)}, Time: time.Now().Unix()}); err != nil {
		return err
	}
	return build.SaveEvent(event.Start{Origin: event.Origin{ID: event.OriginID(plan.ID)}, Time: time.Now().Unix()})
}

func observeStartedInMemoryBuild(resource db.Resource, plan atc.Plan, build db.Build, profile string) (string, error) {
	if err := saveMemoryStartEvents(build, plan); err != nil {
		return "", err
	}
	if err := build.OnCheckBuildStart(); err != nil {
		return "", err
	}
	switch profile {
	case "started-id":
		return fmt.Sprintf("id-positive=%t", build.ID() > 0), nil
	case "started-summary":
		if _, err := resource.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("summary=%s", resource.BuildSummary().Status), nil
	case "started-lager":
		data := build.LagerData()
		valid := len(data) == 6 && data["build_id"] == build.ID() && data["pre_build_id"] == 1 && data["team"] == resource.TeamName() && data["pipeline"] == resource.PipelineName() && data["resource"] == resource.Name() && data["build"] == "check"
		return fmt.Sprintf("lager=%t", valid), nil
	case "started-tracing":
		data := build.TracingAttrs()
		valid := len(data) == 6 && data["build_id"] == fmt.Sprintf("%d", build.ID()) && data["pre_build_id"] == "1" && data["team"] == resource.TeamName() && data["pipeline"] == resource.PipelineName() && data["resource"] == resource.Name() && data["build"] == "check"
		return fmt.Sprintf("tracing=%t", valid), nil
	case "started-events":
		source, err := build.Events(0)
		if err != nil {
			return "", err
		}
		want := []string{"status:0", "initialize:1", "start:2"}
		got := []string{}
		for range 3 {
			ev, err := source.Next()
			if err != nil {
				return "", err
			}
			got = append(got, fmt.Sprintf("%s:%s", ev.Event, ev.EventID))
		}
		if err := source.Close(); err != nil {
			return "", err
		}
		return fmt.Sprintf("events=%t", reflect.DeepEqual(got, want)), nil
	case "started-log":
		if err := build.SaveEvent(event.Log{Origin: event.Origin{ID: event.OriginID(plan.ID)}, Time: time.Now().Unix(), Payload: "some-log-line"}); err != nil {
			return "", err
		}
		return memoryBuildEvent(build, 3, "log", "")
	case "started-cache-user":
		return fmt.Sprintf("cache-user=%t", reflect.DeepEqual(build.ResourceCacheUser(), db.ForInMemoryBuild(1, build.CreateTime()))), nil
	case "started-owner":
		return fmt.Sprintf("owner=%t", reflect.DeepEqual(build.ContainerOwner("some-plan"), db.NewInMemoryCheckBuildContainerOwner(1, build.CreateTime(), "some-plan", resource.TeamID()))), nil
	case "started-run-state":
		return fmt.Sprintf("run-state=%s", build.RunStateID()), nil
	case "started-tracking-lock":
		logger := lager.NewLogger("brine-in-memory-build-lock")
		held, acquired, err := build.AcquireTrackingLock(logger, time.Second)
		if err != nil || !acquired {
			return "", fmt.Errorf("acquire tracking lock: %t: %w", acquired, err)
		}
		_, second, secondErr := build.AcquireTrackingLock(logger, time.Second)
		releaseErr := held.Release()
		if secondErr != nil {
			return "", secondErr
		}
		if releaseErr != nil {
			return "", releaseErr
		}
		return fmt.Sprintf("second-lock=%t", second), nil
	case "finish-summary", "finish-event":
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		if _, err := resource.Reload(); err != nil {
			return "", err
		}
		if profile == "finish-summary" {
			return fmt.Sprintf("summary=%s", resource.BuildSummary().Status), nil
		}
		return memoryBuildEvent(build, 3, "status", "")
	default:
		return "", fmt.Errorf("unknown started profile %q", profile)
	}
}

func memoryBuildEvent(build db.Build, from uint, want, payloadSubstring string) (string, error) {
	source, err := build.Events(from)
	if err != nil {
		return "", err
	}
	ev, err := source.Next()
	if err != nil {
		return "", err
	}
	matches := string(ev.Event) == want
	if payloadSubstring != "" {
		matches = matches && ev.Data != nil && strings.Contains(string(*ev.Data), payloadSubstring)
	}
	return fmt.Sprintf("event=%s;id=%s;matches=%t", ev.Event, ev.EventID, matches), nil
}

func observeAPIInMemoryBuild(database JetbridgeDB, resource db.Resource, plan atc.Plan, profile string) (string, error) {
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return "", err
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return "", err
	}
	found, err := scope.UpdateLastCheckStartTime(1999, plan.Public())
	if err != nil || !found {
		return "", fmt.Errorf("save API in-memory build: found=%t: %w", found, err)
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	build, found, err := database.BuildFactory.BuildForAPI(1999)
	if err != nil || !found {
		return "", fmt.Errorf("load API in-memory build: found=%t: %w", found, err)
	}
	switch profile {
	case "api-find":
		valid := build.ID() == 1999 && build.Name() == db.CheckBuildName && build.TeamID() == resource.TeamID() && build.TeamName() == resource.TeamName() && build.PipelineID() == resource.PipelineID() && build.PipelineName() == resource.PipelineName() && build.JobID() == 0 && build.JobName() == "" && build.ResourceID() == resource.ID() && build.ResourceName() == resource.Name() && build.Schema() == "exec.v2" && !build.IsRunning() && build.HasPlan() && reflect.DeepEqual(build.PublicPlan(), plan.Public())
		return fmt.Sprintf("api-find=%t", valid), nil
	case "api-lager":
		data := build.LagerData()
		valid := len(data) == 5 && data["build_id"] == 1999 && data["team"] == resource.TeamName() && data["pipeline"] == resource.PipelineName() && data["resource"] == resource.Name() && data["build"] == "check"
		return fmt.Sprintf("api-lager=%t", valid), nil
	case "api-events":
		_, err := build.Events(0)
		return fmt.Sprintf("api-events=%t", err == nil), nil
	default:
		return "", fmt.Errorf("unknown API profile %q", profile)
	}
}
