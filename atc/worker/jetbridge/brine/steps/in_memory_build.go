package steps

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
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
	case "creation":
		_, eventsErr := build.Events(0)
		lager := build.LagerData()
		trace := build.TracingAttrs()
		return fmt.Sprintf("id=%d;name=%s;pending=%t;running=%t;manual=%t;plan=%t;lager=%t;trace=%t;span=%t;events-error=%t",
			build.ID(), build.Name(), build.Status() == db.BuildStatusPending, build.IsRunning(), build.IsManuallyTriggered(), build.HasPlan() && reflect.DeepEqual(build.PrivatePlan(), plan),
			lager["pre_build_id"] == 1 && lager["resource"] == resource.Name(), trace["pre_build_id"] == "1" && trace["resource"] == resource.Name(), reflect.DeepEqual(build.SpanContext(), db.NewSpanContext(ctx)), eventsErr != nil), nil
	case "prestart-success":
		if err := saveMemoryStartEvents(build, plan); err != nil {
			return "", err
		}
		_, eventsErr := build.Events(0)
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		return fmt.Sprintf("events-error=%t;id=%d", eventsErr != nil, build.ID()), nil
	case "prestart-error":
		if err := saveMemoryStartEvents(build, plan); err != nil {
			return "", err
		}
		if err := build.Finish(db.BuildStatusErrored); err != nil {
			return "", err
		}
		if _, err := resource.Reload(); err != nil {
			return "", err
		}
		source, err := build.Events(3)
		if err != nil {
			return "", err
		}
		defer source.Close()
		ev, err := source.Next()
		return fmt.Sprintf("summary=%s;event=%s;id=%s", resource.BuildSummary().Status, ev.Event, ev.EventID), err
	case "started":
		return observeStartedInMemoryBuild(resource, plan, build)
	case "api":
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
		apiBuild, found, err := database.BuildFactory.BuildForAPI(1999)
		if err != nil || !found {
			return "", fmt.Errorf("load API in-memory build: found=%t: %w", found, err)
		}
		_, eventsErr := apiBuild.Events(0)
		return fmt.Sprintf("found=true;id=%d;name=%s;running=%t;plan=%t;events=%t", apiBuild.ID(), apiBuild.Name(), apiBuild.IsRunning(), apiBuild.HasPlan() && reflect.DeepEqual(apiBuild.PublicPlan(), plan.Public()), eventsErr == nil), nil
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

func observeStartedInMemoryBuild(resource db.Resource, plan atc.Plan, build db.Build) (string, error) {
	if err := saveMemoryStartEvents(build, plan); err != nil {
		return "", err
	}
	if err := build.OnCheckBuildStart(); err != nil {
		return "", err
	}
	if _, err := resource.Reload(); err != nil {
		return "", err
	}
	source, err := build.Events(0)
	if err != nil {
		return "", err
	}
	eventTypes := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		ev, err := source.Next()
		if err != nil {
			return "", err
		}
		eventTypes = append(eventTypes, string(ev.Event))
	}
	if err := source.Close(); err != nil {
		return "", err
	}
	if err := build.SaveEvent(event.Log{Origin: event.Origin{ID: event.OriginID(plan.ID)}, Time: time.Now().Unix(), Payload: "line"}); err != nil {
		return "", err
	}
	logSource, err := build.Events(3)
	if err != nil {
		return "", err
	}
	logEvent, err := logSource.Next()
	if err != nil {
		return "", err
	}
	_ = logSource.Close()
	lock, acquired, err := build.AcquireTrackingLock(lagertest.NewTestLogger("memory-build"), time.Second)
	if err != nil || !acquired {
		return "", fmt.Errorf("acquire memory build lock: acquired=%t: %w", acquired, err)
	}
	_, acquiredAgain, err := build.AcquireTrackingLock(lagertest.NewTestLogger("memory-build-second"), time.Second)
	if releaseErr := lock.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return "", err
	}
	if err := build.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	if _, err := resource.Reload(); err != nil {
		return "", err
	}
	owner, err := build.ContainerOwner("plan").Create(nil, "")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("id-positive=%t;summary=%s;events=%s,%s,%s;log=%s;cache=%t;owner=%t;run-state=%s;second-lock=%t",
		build.ID() > 0, resource.BuildSummary().Status, eventTypes[0], eventTypes[1], eventTypes[2], logEvent.Event,
		build.ResourceCacheUser() != nil, owner["in_memory_build_id"] != nil, build.RunStateID(), acquiredAgain), nil
}
