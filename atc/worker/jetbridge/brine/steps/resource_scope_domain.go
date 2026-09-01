package steps

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"code.cloudfoundry.org/lager/v3"
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
	case "save-check-order":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		first, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v2"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		second, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("first=%s:%d;second=%s:%d", first.Version()["ref"], first.CheckOrder(), second.Version()["ref"], second.CheckOrder()), nil
	case "save-existing-order":
		versions := []atc.Version{{"ref": "v1"}, {"ref": "v3"}}
		if err := fixture.scope.SaveVersions(nil, versions); err != nil {
			return "", err
		}
		before, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		if err := fixture.scope.SaveVersions(nil, versions); err != nil {
			return "", err
		}
		after, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("before=%d;after=%d", before.CheckOrder(), after.CheckOrder()), nil
	case "schedule-direct":
		return observeScopeSchedule(fixture, fixture.direct)
	case "schedule-passed":
		return observeScopeSchedule(fixture, fixture.passed)
	case "schedule-unrelated":
		return observeScopeSchedule(fixture, fixture.other)
	case "empty-error":
		err := fixture.scope.SaveVersions(nil, []atc.Version{{}})
		if err == nil {
			return "error=<nil>", nil
		}
		return "error=" + err.Error(), nil
	case "latest":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		latest, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("version=%s;order=%d", latest.Version()["ref"], latest.CheckOrder()), nil
	case "latest-disabled":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		initial, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"version": "1"}}); err != nil {
			return "", err
		}
		latest, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		if err := fixture.resource.DisableVersion(latest.ID()); err != nil {
			return "", err
		}
		disabled, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		if err := fixture.resource.EnableVersion(latest.ID()); err != nil {
			return "", err
		}
		enabled, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("initial=%s:%d;saved=%s;disabled=%s;enabled=%s", initial.Version()["ref"], initial.CheckOrder(), latest.Version()["version"], disabled.Version()["version"], enabled.Version()["version"]), nil
	case "latest-updated":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		initial, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "4"}, {"ref": "5"}}); err != nil {
			return "", err
		}
		latest, err := requiredScopeLatest(fixture.scope)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("initial=%s:%d;version=%s", initial.Version()["ref"], initial.CheckOrder(), latest.Version()["ref"]), nil
	case "find-existing":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		version, found, err := fixture.scope.FindVersion(atc.Version{"ref": "v1"})
		if err != nil {
			return "", err
		}
		if !found || version == nil {
			return fmt.Sprintf("found=%t;version=nil", found), nil
		}
		return fmt.Sprintf("found=true;version=%s;order=%d", version.Version()["ref"], version.CheckOrder()), nil
	case "find-missing":
		if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v1"}, {"ref": "v3"}}); err != nil {
			return "", err
		}
		version, found, err := fixture.scope.FindVersion(atc.Version{"ref": "v2"})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("found=%t;nil=%t", found, version == nil), nil
	case "check-start":
		return observeScopeCheckStart(fixture)
	case "check-end":
		return observeScopeCheckEnd(fixture)
	case "scope-soft-delete":
		return observeScopeSoftDelete(fixture)
	case "deprecated-one":
		return observeOneDeprecatedScope(fixture)
	case "deprecated-empty":
		deprecated, err := fixture.resource.DeprecatedScopes()
		return fmt.Sprintf("count=%d", len(deprecated)), err
	case "migration-lifecycle":
		return observeScopeMigration(fixture)
	case "copy-all":
		return observeExactScopeCopy(fixture, 0)
	case "copy-duplicates":
		return observeExactScopeCopy(fixture, 1)
	case "copy-self":
		if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}}); err != nil {
			return "", err
		}
		copied, err := fixture.scope.CopyVersionsFrom(fixture.scope.ID())
		return fmt.Sprintf("copied=%d", copied), err
	case "lock-contended":
		return observeScopeLockContended(fixture)
	case "lock-released":
		return observeScopeLockReleased(fixture)
	case "lock-periodic":
		return observeScopeLockPeriodic(fixture)
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
		logger := lager.NewLogger("resource-scope-lock")
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

func requiredScopeLatest(scope db.ResourceConfigScope) (db.ResourceConfigVersion, error) {
	version, found, err := scope.LatestVersion()
	if err != nil || !found {
		return nil, fmt.Errorf("load latest scope version: found=%t: %w", found, err)
	}
	return version, nil
}

func observeScopeSchedule(fixture *resourceScopeFixture, observed db.Job) (string, error) {
	versions := []atc.Version{{"ref": "v1"}, {"ref": "v3"}}
	if err := fixture.scope.SaveVersions(nil, versions); err != nil {
		return "", err
	}
	initial, err := requiredScopeLatest(fixture.scope)
	if err != nil {
		return "", err
	}
	if err := fixture.scope.SaveVersions(nil, versions); err != nil {
		return "", err
	}
	if found, err := observed.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload observed scope job before schedule: found=%t: %w", found, err)
	}
	before := observed.ScheduleRequestedTime()
	if err := fixture.scope.SaveVersions(nil, []atc.Version{{"ref": "v0"}, {"ref": "v3"}}); err != nil {
		return "", err
	}
	if found, err := observed.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload observed scope job: found=%t: %w", found, err)
	}
	return fmt.Sprintf("initial=%s:%d;advanced=%t", initial.Version()["ref"], initial.CheckOrder(), observed.ScheduleRequestedTime().After(before)), nil
}

func observeScopeCheckStart(fixture *resourceScopeFixture) (string, error) {
	before := fixture.resource.LastCheckStartTime()
	plan := atc.Plan{ID: "1234", Check: &atc.CheckPlan{Name: "resource", Type: "registry-image"}}
	bytes, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	raw := json.RawMessage(bytes)
	startedAt := time.Now()
	updated, err := fixture.scope.UpdateLastCheckStartTime(99, &raw)
	if err != nil {
		return "", err
	}
	if found, err := fixture.resource.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload resource after check start: found=%t: %w", found, err)
	}
	summary := fixture.resource.BuildSummary()
	var actualPlan atc.Plan
	planEqual := summary != nil && summary.PublicPlan != nil && json.Unmarshal(*summary.PublicPlan, &actualPlan) == nil && reflect.DeepEqual(actualPlan, plan)
	return fmt.Sprintf("updated=%t;advanced=%t;id=%d;recent=%t;plan=%t", updated, fixture.resource.LastCheckStartTime().After(before), summary.ID, time.Unix(summary.StartTime, 0).After(startedAt.Add(-time.Second)), planEqual), nil
}

func observeScopeCheckEnd(fixture *resourceScopeFixture) (string, error) {
	if _, err := fixture.scope.UpdateLastCheckStartTime(99, nil); err != nil {
		return "", err
	}
	if found, err := fixture.resource.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload resource before check end: found=%t: %w", found, err)
	}
	before := fixture.resource.LastCheckEndTime()
	updated, err := fixture.scope.UpdateLastCheckEndTime(true)
	if err != nil {
		return "", err
	}
	if found, err := fixture.resource.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload resource after check end: found=%t: %w", found, err)
	}
	return fmt.Sprintf("updated=%t;advanced=%t", updated, fixture.resource.LastCheckEndTime().After(before)), nil
}

func changedResourceScope(fixture *resourceScopeFixture, source string) (int, db.ResourceConfigScope, error) {
	oldID := fixture.scope.ID()
	config, err := db.NewResourceConfigFactory(fixture.database.Conn, fixture.database.LockFactory).FindOrCreateResourceConfig(fixture.resource.Type(), atc.Source{"repository": source}, nil)
	if err != nil {
		return 0, nil, err
	}
	resourceID := fixture.resource.ID()
	newScope, err := config.FindOrCreateScope(&resourceID)
	return oldID, newScope, err
}

func observeScopeSoftDelete(fixture *resourceScopeFixture) (string, error) {
	if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}}); err != nil {
		return "", err
	}
	oldID, _, err := changedResourceScope(fixture, "example/different")
	if err != nil {
		return "", err
	}
	var deprecatedAt *time.Time
	var deprecatedFrom *int
	var count int
	if err := fixture.database.Conn.QueryRow(`SELECT deprecated_at, deprecated_from_resource_id FROM resource_config_scopes WHERE id = $1`, oldID).Scan(&deprecatedAt, &deprecatedFrom); err != nil {
		return "", err
	}
	if err := fixture.database.Conn.QueryRow(`SELECT count(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`, oldID).Scan(&count); err != nil {
		return "", err
	}
	return fmt.Sprintf("deprecated=%t;resource=%t;versions=%d", deprecatedAt != nil, deprecatedFrom != nil && *deprecatedFrom == fixture.resource.ID(), count), nil
}

func observeOneDeprecatedScope(fixture *resourceScopeFixture) (string, error) {
	_, _, err := changedResourceScope(fixture, "example/changed")
	if err != nil {
		return "", err
	}
	if found, err := fixture.resource.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload deprecated resource: found=%t: %w", found, err)
	}
	deprecated, err := fixture.resource.DeprecatedScopes()
	if err != nil {
		return "", err
	}
	nonzero := len(deprecated) == 1 && !deprecated[0].DeprecatedAt.IsZero()
	return fmt.Sprintf("count=%d;timestamp=%t", len(deprecated), nonzero), nil
}

func observeScopeMigration(fixture *resourceScopeFixture) (string, error) {
	if err := fixture.scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"}}); err != nil {
		return "", err
	}
	oldID, newScope, err := changedResourceScope(fixture, "example/new-after-change")
	if err != nil {
		return "", err
	}
	oldBefore, err := countResourceScopeVersions(fixture.database, oldID)
	if err != nil {
		return "", err
	}
	newBefore, err := countResourceScopeVersions(fixture.database, newScope.ID())
	if err != nil {
		return "", err
	}
	copied, err := newScope.CopyVersionsFrom(oldID)
	if err != nil {
		return "", err
	}
	newAfter, err := countResourceScopeVersions(fixture.database, newScope.ID())
	if err != nil {
		return "", err
	}
	if err := fixture.resource.SetResourceConfigScope(newScope); err != nil {
		return "", err
	}
	if found, err := fixture.resource.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload resource after migration: found=%t: %w", found, err)
	}
	deprecated, err := fixture.resource.DeprecatedScopes()
	if err != nil {
		return "", err
	}
	again, err := fixture.resource.CopyVersionsFromScope(oldID)
	if err != nil {
		return "", err
	}
	deprecatedMatches := len(deprecated) == 1 && deprecated[0].ID == oldID
	return fmt.Sprintf("different=%t;old=%d;new-before=%d;copied=%d;new-after=%d;deprecated=%t;again=%d", newScope.ID() != oldID, oldBefore, newBefore, copied, newAfter, deprecatedMatches, again), nil
}

func observeExactScopeCopy(fixture *resourceScopeFixture, targetOverlap int) (string, error) {
	versions := []atc.Version{{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"}}
	if targetOverlap == 1 {
		versions = versions[:2]
	}
	if err := fixture.scope.SaveVersions(db.SpanContext{}, versions); err != nil {
		return "", err
	}
	config, err := db.NewResourceConfigFactory(fixture.database.Conn, fixture.database.LockFactory).FindOrCreateResourceConfig(fixture.resource.Type(), atc.Source{"repository": fmt.Sprintf("example/target-%d", targetOverlap)}, nil)
	if err != nil {
		return "", err
	}
	target, err := config.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	if targetOverlap == 1 {
		if err := target.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}}); err != nil {
			return "", err
		}
	}
	copied, err := target.CopyVersionsFrom(fixture.scope.ID())
	if err != nil {
		return "", err
	}
	count, err := countResourceScopeVersions(fixture.database, target.ID())
	return fmt.Sprintf("copied=%d;count=%d", copied, count), err
}

func countResourceScopeVersions(database JetbridgeDB, scopeID int) (int, error) {
	var count int
	err := database.Conn.QueryRow(`SELECT count(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`, scopeID).Scan(&count)
	return count, err
}

func observeScopeLockContended(fixture *resourceScopeFixture) (string, error) {
	logger := lager.NewLogger("resource-scope-lock")
	held, first, err := fixture.scope.AcquireResourceCheckingLock(logger)
	if err != nil || !first {
		return "", fmt.Errorf("first scope lock: acquired=%t: %w", first, err)
	}
	defer held.Release()
	contender, second, err := fixture.scope.AcquireResourceCheckingLock(logger)
	if err != nil {
		return "", err
	}
	if second {
		_ = contender.Release()
	}
	return fmt.Sprintf("first=%t;second=%t", first, second), nil
}

func observeScopeLockReleased(fixture *resourceScopeFixture) (string, error) {
	logger := lager.NewLogger("resource-scope-lock")
	held, first, err := fixture.scope.AcquireResourceCheckingLock(logger)
	if err != nil || !first {
		return "", fmt.Errorf("first scope lock: acquired=%t: %w", first, err)
	}
	if err := held.Release(); err != nil {
		return "", err
	}
	reacquired, second, err := fixture.scope.AcquireResourceCheckingLock(logger)
	if err != nil {
		return "", err
	}
	if second {
		_ = reacquired.Release()
	}
	return fmt.Sprintf("first=%t;after-release=%t", first, second), nil
}

func observeScopeLockPeriodic(fixture *resourceScopeFixture) (string, error) {
	logger := lager.NewLogger("resource-scope-lock")
	held, first, err := fixture.scope.AcquireResourceCheckingLock(logger)
	if err != nil || !first {
		return "", fmt.Errorf("first scope lock: acquired=%t: %w", first, err)
	}
	blocked := true
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		contender, acquired, err := fixture.scope.AcquireResourceCheckingLock(logger)
		if err != nil {
			_ = held.Release()
			return "", err
		}
		if acquired {
			blocked = false
			_ = contender.Release()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := held.Release(); err != nil {
		return "", err
	}
	time.Sleep(time.Second)
	reacquired, afterRelease, err := fixture.scope.AcquireResourceCheckingLock(logger)
	if err != nil {
		return "", err
	}
	if afterRelease {
		_ = reacquired.Release()
	}
	return fmt.Sprintf("first=%t;blocked=%t;after-release=%t", first, blocked, afterRelease), nil
}
