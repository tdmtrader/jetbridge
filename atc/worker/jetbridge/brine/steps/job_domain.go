package steps

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type JobDomainObservation struct{ Value string }

func JobDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, JobDomainObservation](
			"the real job domain handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (JobDomainObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return JobDomainObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeJobDomain(database, profile)
				return JobDomainObservation{Value: value}, err
			},
		),
		CheckString[JobDomainObservation]("the job domain result is {string}", "job domain result", func(in JobDomainObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeJobDomain(database JetbridgeDB, profile string) (string, error) {
	primary := atc.JobConfig{Name: "alpha"}
	switch profile {
	case "public-true":
		primary.Public = true
	case "public-false", "public-default":
		primary.Public = false
	case "disable-true":
		primary.DisableManualTrigger = true
	}
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "job-domain"})
	if err != nil {
		return "", err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Jobs: atc.JobConfigs{primary, {Name: "beta"}}}, 0, false)
	if err != nil {
		return "", err
	}
	job, found, err := pipeline.Job("alpha")
	if err != nil || !found {
		return "", firstError(err, fmt.Errorf("alpha job missing"))
	}
	reload := func() error {
		found, err := job.Reload()
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("job disappeared")
		}
		return nil
	}
	switch profile {
	case "public-true", "public-false", "public-default":
		return fmt.Sprintf("public=%t", job.Public()), nil
	case "disable-true", "disable-false":
		return fmt.Sprintf("disabled=%t", job.DisableManualTrigger()), nil
	case "unpaused":
		return fmt.Sprintf("paused=%t", job.Paused()), nil
	case "pause-state", "pause-no-schedule", "pause-by-empty", "pause-at", "pause-by-user":
		before := job.ScheduleRequestedTime()
		user := ""
		if profile == "pause-by-user" {
			user = "concourse"
		}
		if err := job.Pause(user); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		switch profile {
		case "pause-state":
			return fmt.Sprintf("paused=%t", job.Paused()), nil
		case "pause-no-schedule":
			return fmt.Sprintf("unchanged=%t", job.ScheduleRequestedTime().Equal(before)), nil
		case "pause-by-empty", "pause-by-user":
			return "paused-by=" + job.PausedBy(), nil
		case "pause-at":
			return fmt.Sprintf("recent=%t", time.Since(job.PausedAt()) < time.Second), nil
		}
	case "unpause-state", "unpause-schedule":
		before := job.ScheduleRequestedTime()
		if err := job.Unpause(); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		if profile == "unpause-state" {
			return fmt.Sprintf("paused=%t", job.Paused()), nil
		}
		return fmt.Sprintf("within-second=%t", absDuration(job.ScheduleRequestedTime().Sub(before)) <= time.Second), nil
	case "first-logged":
		if job.FirstLoggedBuildID() != 0 {
			return "", fmt.Errorf("initial first logged ID was %d", job.FirstLoggedBuildID())
		}
		if err := job.UpdateFirstLoggedBuildID(57); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		sameErr := job.UpdateFirstLoggedBuildID(57)
		decreaseErr := job.UpdateFirstLoggedBuildID(56)
		decreased, exact := decreaseErr.(db.FirstLoggedBuildIDDecreasedError)
		exact = exact && decreased.Job == job.Name() && decreased.OldID == 57 && decreased.NewID == 56
		return fmt.Sprintf("id=%d;same-ok=%t;decrease-exact=%t", job.FirstLoggedBuildID(), sameErr == nil, exact), nil
	case "latest-completed":
		build, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		if err := build.Finish(db.BuildStatusFailed); err != nil {
			return "", err
		}
		id, err := job.LatestCompletedBuildId()
		return fmt.Sprintf("matches=%t", id == build.ID()), err
	case "build-latest", "build-exact", "build-missing", "build-latest-missing", "build-create-schedule":
		return observeJobBuildLookup(job, profile, reload)
	case "builds-empty", "builds-first", "builds-to-middle", "builds-to-end", "builds-from-middle", "builds-from-start":
		return observeJobBuildPages(pipeline, job, profile)
	case "time-empty", "time-limit", "time-to", "time-from", "time-range":
		return observeJobTimePages(database, job, profile)
	case "ensure-pending", "ensure-idempotent":
		if err := job.EnsurePendingBuildExists(context.Background()); err != nil {
			return "", err
		}
		if profile == "ensure-idempotent" {
			if err := job.EnsurePendingBuildExists(context.Background()); err != nil {
				return "", err
			}
		}
		pending, err := job.GetPendingBuilds()
		if err != nil {
			return "", err
		}
		_, next, nextErr := job.FinishedAndNextBuild()
		if nextErr != nil {
			return "", nextErr
		}
		nextMatches := len(pending) == 1 && next != nil && pending[0].ID() == next.ID()
		if profile == "ensure-pending" {
			return fmt.Sprintf("pending=%d;next-matches=%t", len(pending), nextMatches), nil
		}
		if len(pending) != 1 {
			return fmt.Sprintf("pending=%d;next-matches=%t;after-start=-1", len(pending), nextMatches), nil
		}
		startedPending, startErr := pending[0].Start(atc.Plan{})
		if startErr != nil {
			return "", startErr
		}
		after, afterErr := job.GetPendingBuilds()
		if afterErr != nil {
			return "", afterErr
		}
		return fmt.Sprintf("pending=%d;next-matches=%t;started=%t;after-start=%d", len(pending), nextMatches, startedPending, len(after)), nil
	case "new-inputs-initial":
		return fmt.Sprintf("new=%t", job.HasNewInputs()), nil
	case "new-inputs-toggle":
		if err := job.SetHasNewInputs(true); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		wasTrue := job.HasNewInputs()
		if err := job.SetHasNewInputs(false); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("true=%t;false=%t", wasTrue, !job.HasNewInputs()), nil
	default:
		return "", fmt.Errorf("unknown job domain profile %q", profile)
	}
	return "", fmt.Errorf("unhandled job domain profile %q", profile)
}

func observeJobBuildLookup(job db.Job, profile string, reload func() error) (string, error) {
	if profile == "build-missing" || profile == "build-latest-missing" {
		name := "missing"
		if profile == "build-latest-missing" {
			name = "latest"
		}
		build, found, err := job.Build(name)
		return fmt.Sprintf("found=%t;nil=%t", found, build == nil), err
	}
	before := job.ScheduleRequestedTime()
	first, err := job.CreateBuild("brine-user")
	if err != nil {
		return "", err
	}
	if profile == "build-create-schedule" {
		if err := reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("advanced=%t", job.ScheduleRequestedTime().After(before)), nil
	}
	name, expectedID := first.Name(), first.ID()
	if profile == "build-latest" {
		second, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		name, expectedID = "latest", second.ID()
	}
	build, found, err := job.Build(name)
	return fmt.Sprintf("found=%t;matches=%t;status=pending:%t", found, found && build.ID() == expectedID, found && build.Status() == db.BuildStatusPending), err
}

func observeJobBuildPages(pipeline db.Pipeline, job db.Job, profile string) (string, error) {
	if profile == "builds-empty" {
		other, found, err := pipeline.Job("beta")
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("beta job missing"))
		}
		builds, pagination, err := other.Builds(db.Page{})
		return fmt.Sprintf("builds-exact=%t;pagination-exact=%t", len(builds) == 0, reflect.DeepEqual(pagination, db.Pagination{})), err
	}
	builds := make([]db.Build, 10)
	for i := range builds {
		var err error
		builds[i], err = job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
	}
	page := db.Page{Limit: 2}
	switch profile {
	case "builds-to-middle":
		page.To = db.NewIntPtr(builds[6].ID())
	case "builds-to-end":
		page.To = db.NewIntPtr(builds[1].ID())
	case "builds-from-middle":
		page.From = db.NewIntPtr(builds[6].ID())
	case "builds-from-start":
		page.From = db.NewIntPtr(builds[8].ID())
	}
	got, pagination, err := job.Builds(page)
	if err != nil {
		return "", err
	}
	wantBuilds := []db.Build{builds[9], builds[8]}
	wantPagination := db.Pagination{Older: &db.Page{To: db.NewIntPtr(builds[7].ID()), Limit: 2}}
	switch profile {
	case "builds-to-middle":
		wantBuilds = []db.Build{builds[6], builds[5]}
		wantPagination = db.Pagination{Newer: &db.Page{From: db.NewIntPtr(builds[7].ID()), Limit: 2}, Older: &db.Page{To: db.NewIntPtr(builds[4].ID()), Limit: 2}}
	case "builds-to-end":
		wantBuilds = []db.Build{builds[1], builds[0]}
		wantPagination = db.Pagination{Newer: &db.Page{From: db.NewIntPtr(builds[2].ID()), Limit: 2}}
	case "builds-from-middle":
		wantBuilds = []db.Build{builds[7], builds[6]}
		wantPagination = db.Pagination{Newer: &db.Page{From: db.NewIntPtr(builds[8].ID()), Limit: 2}, Older: &db.Page{To: db.NewIntPtr(builds[5].ID()), Limit: 2}}
	case "builds-from-start":
		wantBuilds = []db.Build{builds[9], builds[8]}
		wantPagination = db.Pagination{Older: &db.Page{To: db.NewIntPtr(builds[7].ID()), Limit: 2}}
	}
	return fmt.Sprintf("builds-exact=%t;pagination-exact=%t", sameBuildIDsInOrder(got, wantBuilds), reflect.DeepEqual(pagination, wantPagination)), nil
}

func observeJobTimePages(database JetbridgeDB, job db.Job, profile string) (string, error) {
	builds := make([]db.Build, 4)
	for i := range builds {
		var err error
		builds[i], err = job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		start := time.Date(2020, 11, i+1, 0, 0, 0, 0, time.UTC)
		if _, err := database.Conn.Exec(`UPDATE builds SET start_time = to_timestamp($1) WHERE id = $2`, start.Unix(), builds[i].ID()); err != nil {
			return "", err
		}
	}
	page := db.Page{}
	want := []db.Build{}
	switch profile {
	case "time-empty":
	case "time-limit":
		page.Limit, want = 2, []db.Build{builds[3], builds[2]}
	case "time-to":
		page.To, page.Limit, want = db.NewIntPtr(int(time.Date(2020, 11, 3, 0, 0, 0, 0, time.UTC).Unix())), 50, []db.Build{builds[0], builds[1], builds[2]}
	case "time-from":
		page.From, page.Limit, want = db.NewIntPtr(int(time.Date(2020, 11, 2, 0, 0, 0, 0, time.UTC).Unix())), 50, []db.Build{builds[1], builds[2], builds[3]}
	case "time-range":
		page.From = db.NewIntPtr(int(time.Date(2020, 11, 2, 0, 0, 0, 0, time.UTC).Unix()))
		page.To = db.NewIntPtr(int(time.Date(2020, 11, 3, 0, 0, 0, 0, time.UTC).Unix()))
		page.Limit, want = 50, []db.Build{builds[1], builds[2]}
	}
	got, _, err := job.BuildsWithTime(page)
	return fmt.Sprintf("builds-exact=%t", sameBuildIDsUnordered(got, want)), err
}

func sameBuildIDsInOrder(got []db.BuildForAPI, want []db.Build) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].ID() != want[i].ID() {
			return false
		}
	}
	return true
}

func sameBuildIDsUnordered(got []db.BuildForAPI, want []db.Build) bool {
	if len(got) != len(want) {
		return false
	}
	ids := map[int]int{}
	for _, build := range want {
		ids[build.ID()]++
	}
	for _, build := range got {
		ids[build.ID()]--
	}
	for _, count := range ids {
		if count != 0 {
			return false
		}
	}
	return true
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
