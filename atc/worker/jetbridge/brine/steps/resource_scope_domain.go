package steps

import (
	"encoding/json"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type ResourceScopeObservation struct{ Value string }

type resourceScopeFixture struct {
	database JetbridgeDB
	resource db.Resource
	scope    db.ResourceConfigScope
	direct   db.Job
	passed   db.Job
	other    db.Job
}

func ResourceScopeDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ResourceScopeObservation](
			"the real resource scope evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (ResourceScopeObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ResourceScopeObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeResourceScope(database, profile)
				return ResourceScopeObservation{Value: value}, err
			},
		),
		CheckString[ResourceScopeObservation]("the resource scope result is {string}", "resource scope result", func(in ResourceScopeObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func newResourceScopeFixture(database JetbridgeDB) (*resourceScopeFixture, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "scope-team"})
	if err != nil {
		return nil, err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "scope-pipeline"},
		atc.Config{
			Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}},
			Jobs: atc.JobConfigs{
				{Name: "direct", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "resource"}}}},
				{Name: "passed", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "resource", Passed: []string{"direct"}}}}},
				{Name: "other"},
			},
		}, 0, false,
	)
	if err != nil {
		return nil, err
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return nil, fmt.Errorf("load scope resource: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return nil, err
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return nil, err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return nil, err
	}
	loadJob := func(name string) (db.Job, error) {
		job, found, jobErr := pipeline.Job(name)
		if jobErr != nil || !found {
			return nil, fmt.Errorf("load scope job %s: found=%t: %w", name, found, jobErr)
		}
		return job, nil
	}
	direct, err := loadJob("direct")
	if err != nil {
		return nil, err
	}
	passed, err := loadJob("passed")
	if err != nil {
		return nil, err
	}
	other, err := loadJob("other")
	if err != nil {
		return nil, err
	}
	return &resourceScopeFixture{database: database, resource: resource, scope: scope, direct: direct, passed: passed, other: other}, nil
}

func observeResourceScope(database JetbridgeDB, profile string) (string, error) {
	fixture, err := newResourceScopeFixture(database)
	if err != nil {
		return "", err
	}
	switch profile {
	case "save":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		latest, found, err := fixture.scope.LatestVersion()
		if err != nil || !found {
			return "", fmt.Errorf("load first latest scope version: found=%t: %w", found, err)
		}
		firstOrder := latest.CheckOrder()
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		latest, found, err = fixture.scope.LatestVersion()
		if err != nil || !found {
			return "", fmt.Errorf("load repeated latest scope version: found=%t: %w", found, err)
		}
		repeatedOrder := latest.CheckOrder()
		directBefore, passedBefore, otherBefore := fixture.direct.ScheduleRequestedTime(), fixture.passed.ScheduleRequestedTime(), fixture.other.ScheduleRequestedTime()
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v2"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		latest, found, err = fixture.scope.LatestVersion()
		if err != nil || !found {
			return "", fmt.Errorf("load reordered latest scope version: found=%t: %w", found, err)
		}
		for _, job := range []db.Job{fixture.direct, fixture.passed, fixture.other} {
			if found, err := job.Reload(); err != nil || !found {
				return "", fmt.Errorf("reload scope job: found=%t: %w", found, err)
			}
		}
		return fmt.Sprintf("first=%d;repeat=%d;reordered=%d;direct=%t;passed=%t;other=%t", firstOrder, repeatedOrder, latest.CheckOrder(), fixture.direct.ScheduleRequestedTime().After(directBefore), fixture.passed.ScheduleRequestedTime().After(passedBefore), fixture.other.ScheduleRequestedTime().After(otherBefore)), nil
	case "empty":
		err := fixture.scope.SaveVersions(nil, []atc.Version{{}})
		return fmt.Sprintf("error=%t", err != nil), nil
	case "latest-find":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		latest, found, err := fixture.scope.LatestVersion()
		if err != nil || !found {
			return "", fmt.Errorf("load latest scope version: found=%t: %w", found, err)
		}
		if err := fixture.resource.DisableVersion(latest.ID()); err != nil {
			return "", err
		}
		disabledLatest, found, err := fixture.scope.LatestVersion()
		if err != nil || !found {
			return "", fmt.Errorf("load disabled latest scope version: found=%t: %w", found, err)
		}
		if err := fixture.resource.EnableVersion(latest.ID()); err != nil {
			return "", err
		}
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v4"}, {"ref": "v5"}}); err != nil {
			return "", err
		}
		updated, found, err := fixture.scope.LatestVersion()
		if err != nil || !found {
			return "", fmt.Errorf("load updated latest scope version: found=%t: %w", found, err)
		}
		v1, v1Found, err := fixture.scope.FindVersion(atc.Version{"ref": "v1"})
		if err != nil {
			return "", err
		}
		missing, missingFound, err := fixture.scope.FindVersion(atc.Version{"ref": "missing"})
		return fmt.Sprintf("latest=%s;disabled=%s;updated=%s;v1=%t:%d;missing=%t:%t", latest.Version()["ref"], disabledLatest.Version()["ref"], updated.Version()["ref"], v1Found, v1.CheckOrder(), missingFound, missing == nil), err
	case "check-times":
		plan := atc.Plan{ID: "1234", Check: &atc.CheckPlan{Name: "resource", Type: "registry-image"}}
		bytes, err := json.Marshal(plan)
		if err != nil {
			return "", err
		}
		raw := json.RawMessage(bytes)
		started, err := fixture.scope.UpdateLastCheckStartTime(99, &raw)
		if err != nil {
			return "", err
		}
		if _, err := fixture.resource.Reload(); err != nil {
			return "", err
		}
		summary := fixture.resource.BuildSummary()
		ended, err := fixture.scope.UpdateLastCheckEndTime(true)
		if err != nil {
			return "", err
		}
		lastCheck, err := fixture.scope.LastCheck()
		return fmt.Sprintf("start=%t;summary=%d:%t;end=%t;succeeded=%t", started, summary.ID, summary.PublicPlan != nil, ended, lastCheck.Succeeded), err
	case "deprecate-copy":
		return observeScopeDeprecationCopy(fixture)
	case "copy":
		return observeScopeCopy(fixture)
	case "locks":
		logger := lagertest.NewTestLogger("resource-scope-lock")
		first, acquired, err := fixture.scope.AcquireResourceCheckingLock(logger)
		if err != nil || !acquired {
			return "", fmt.Errorf("first scope lock: acquired=%t: %w", acquired, err)
		}
		_, second, err := fixture.scope.AcquireResourceCheckingLock(logger)
		if err != nil {
			return "", err
		}
		if err := first.Release(); err != nil {
			return "", err
		}
		reacquired, third, err := fixture.scope.AcquireResourceCheckingLock(logger)
		if err != nil {
			return "", err
		}
		if third {
			_ = reacquired.Release()
		}
		return fmt.Sprintf("first=true;second=%t;after-release=%t", second, third), nil
	default:
		return "", fmt.Errorf("unknown resource scope profile %q", profile)
	}
}

func observeScopeDeprecationCopy(fixture *resourceScopeFixture) (string, error) {
	if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"}}); err != nil {
		return "", err
	}
	oldID := fixture.scope.ID()
	config, err := db.NewResourceConfigFactory(fixture.database.Conn, fixture.database.LockFactory).FindOrCreateResourceConfig(fixture.resource.Type(), atc.Source{"repository": "example/changed"}, nil)
	if err != nil {
		return "", err
	}
	resourceID := fixture.resource.ID()
	newScope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return "", err
	}
	deprecated, err := fixture.resource.DeprecatedScopes()
	if err != nil {
		return "", err
	}
	copied, err := newScope.CopyVersionsFrom(oldID)
	if err != nil {
		return "", err
	}
	if err := fixture.resource.SetResourceConfigScope(newScope); err != nil {
		return "", err
	}
	if found, err := fixture.resource.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload resource after scope migration: found=%t: %w", found, err)
	}
	copiedAgain, err := fixture.resource.CopyVersionsFromScope(oldID)
	if err != nil {
		return "", err
	}
	var oldCount, newCount int
	if err := fixture.database.Conn.QueryRow(`SELECT count(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`, oldID).Scan(&oldCount); err != nil {
		return "", err
	}
	if err := fixture.database.Conn.QueryRow(`SELECT count(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`, newScope.ID()).Scan(&newCount); err != nil {
		return "", err
	}
	return fmt.Sprintf("different=%t;deprecated=%d;old=%d;copied=%d;new=%d;again=%d", newScope.ID() != oldID, len(deprecated), oldCount, copied, newCount, copiedAgain), nil
}

func observeScopeCopy(fixture *resourceScopeFixture) (string, error) {
	if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"}}); err != nil {
		return "", err
	}
	config, err := db.NewResourceConfigFactory(fixture.database.Conn, fixture.database.LockFactory).FindOrCreateResourceConfig(fixture.resource.Type(), atc.Source{"repository": "example/target"}, nil)
	if err != nil {
		return "", err
	}
	target, err := config.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if err := target.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}}); err != nil {
		return "", err
	}
	copied, err := target.CopyVersionsFrom(fixture.scope.ID())
	if err != nil {
		return "", err
	}
	self, err := fixture.scope.CopyVersionsFrom(fixture.scope.ID())
	return fmt.Sprintf("new=%d;self=%d", copied, self), err
}
