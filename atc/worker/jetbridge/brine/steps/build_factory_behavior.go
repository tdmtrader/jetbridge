package steps

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/util"
)

type BuildFactoryObservation struct{ Value string }

func BuildFactoryBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, BuildFactoryObservation](
			"the real build factory evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (BuildFactoryObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return BuildFactoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeBuildFactory(database, profile)
				return BuildFactoryObservation{Value: value}, err
			},
		),
		CheckString[BuildFactoryObservation]("the build factory result is {string}", "build factory result", func(in BuildFactoryObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeBuildFactory(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "factory-team"})
	if err != nil {
		return "", err
	}
	switch profile {
	case "lookup":
		created, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		found, ok, err := database.BuildFactory.Build(created.ID())
		return fmt.Sprintf("found=%t;same=%t", ok, err == nil && found.ID() == created.ID()), err
	case "one-off-within-grace":
		return buildFactoryInterceptibility(database, team, true, true)
	case "one-off-past-grace":
		return buildFactoryInterceptibility(database, team, true, false)
	case "one-off-running":
		build, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		factory := db.NewBuildFactory(database.Conn, database.LockFactory, time.Hour, time.Hour)
		if err := factory.MarkNonInterceptibleBuilds(); err != nil {
			return "", err
		}
		value, err := build.Interceptible()
		return fmt.Sprintf("interceptible=%t", value), err
	case "pipeline-completed":
		return buildFactoryInterceptibility(database, team, false, false)
	case "pipeline-running":
		_, job, err := buildFactoryJob(team, "running")
		if err != nil {
			return "", err
		}
		created, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		started, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		if _, err := started.Start(atc.Plan{}); err != nil {
			return "", err
		}
		if err := database.BuildFactory.MarkNonInterceptibleBuilds(); err != nil {
			return "", err
		}
		first, err := created.Interceptible()
		if err != nil {
			return "", err
		}
		second, err := started.Interceptible()
		return fmt.Sprintf("pending=%t;started=%t", first, second), err
	case "pipeline-failed-immediate":
		_, firstJob, err := buildFactoryJob(team, "failed-a")
		if err != nil {
			return "", err
		}
		_, secondJob, err := buildFactoryJob(team, "failed-b")
		if err != nil {
			return "", err
		}
		values := make([]string, 0, 4)
		for i := 0; i < 4; i++ {
			job := firstJob
			if i >= 2 {
				job = secondJob
			}
			build, err := job.CreateBuild("brine-user")
			if err != nil {
				return "", err
			}
			if err := build.Finish(db.BuildStatusFailed); err != nil {
				return "", err
			}
			interceptible, err := build.Interceptible()
			if err != nil {
				return "", err
			}
			values = append(values, fmt.Sprintf("%t", interceptible))
		}
		return strings.Join(values, ","), nil
	case "visibility":
		return observeBuildFactoryVisibility(database, team)
	case "drainable":
		return observeBuildFactoryDrainable(database, team)
	case "started":
		_, job, err := buildFactoryJob(team, "started")
		if err != nil {
			return "", err
		}
		oneOff, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		jobBuild, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		if _, err := team.CreateOneOffBuild(); err != nil {
			return "", err
		}
		if _, err := oneOff.Start(atc.Plan{}); err != nil {
			return "", err
		}
		if _, err := jobBuild.Start(atc.Plan{}); err != nil {
			return "", err
		}
		builds, err := database.BuildFactory.GetAllStartedBuilds()
		if err != nil {
			return "", err
		}
		actual := joinedBuildIDs(builds)
		expected := joinedInts([]int{oneOff.ID(), jobBuild.ID()})
		return fmt.Sprintf("count=%d;match=%t", len(builds), actual == expected), nil
	case "date-pages":
		one, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		two, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		if _, err := team.CreateOneOffBuild(); err != nil {
			return "", err
		}
		if _, err := one.Start(atc.Plan{}); err != nil {
			return "", err
		}
		if _, err := two.Start(atc.Plan{}); err != nil {
			return "", err
		}
		now := int(time.Now().Unix())
		inside, _, err := database.BuildFactory.AllBuilds(db.Page{Limit: 10, From: db.NewIntPtr(now - 10000), To: db.NewIntPtr(now + 10), UseDate: true})
		if err != nil {
			return "", err
		}
		future, _, err := database.BuildFactory.AllBuilds(db.Page{Limit: 10, From: db.NewIntPtr(now + 10), UseDate: true})
		if err != nil {
			return "", err
		}
		old, _, err := database.BuildFactory.AllBuilds(db.Page{Limit: 10, To: db.NewIntPtr(now - 10000), UseDate: true})
		return fmt.Sprintf("inside=%d;future=%d;old=%d", len(inside), len(future), len(old)), err
	default:
		return "", fmt.Errorf("unknown build factory profile %q", profile)
	}
}

func buildFactoryInterceptibility(database JetbridgeDB, team db.Team, oneOff, withinGrace bool) (string, error) {
	statuses := []db.BuildStatus{db.BuildStatusSucceeded, db.BuildStatusAborted, db.BuildStatusErrored, db.BuildStatusFailed}
	values := make([]string, 0, len(statuses))
	factory := db.NewBuildFactory(database.Conn, database.LockFactory, 0, 0)
	if withinGrace {
		factory = db.NewBuildFactory(database.Conn, database.LockFactory, time.Hour, time.Hour)
	}
	var job db.Job
	if !oneOff {
		_, createdJob, err := buildFactoryJob(team, "completed")
		if err != nil {
			return "", err
		}
		job = createdJob
	}
	for _, status := range statuses {
		var build db.Build
		var err error
		if oneOff {
			build, err = team.CreateOneOffBuild()
		} else {
			build, err = job.CreateBuild("brine-user")
		}
		if err != nil {
			return "", err
		}
		if err := build.Finish(status); err != nil {
			return "", err
		}
		if err := factory.MarkNonInterceptibleBuilds(); err != nil {
			return "", err
		}
		value, err := build.Interceptible()
		if err != nil {
			return "", err
		}
		values = append(values, fmt.Sprintf("%s=%t", status, value))
	}
	return strings.Join(values, ";"), nil
}

func buildFactoryJob(team db.Team, suffix string) (db.Pipeline, db.Job, error) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "factory-pipeline-" + suffix},
		atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, false,
	)
	if err != nil {
		return nil, nil, err
	}
	job, found, err := pipeline.Job("job")
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("saved factory job not found")
	}
	return pipeline, job, nil
}

func observeBuildFactoryVisibility(database JetbridgeDB, team db.Team) (string, error) {
	oneOff, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	privatePipeline, privateJob, err := buildFactoryJob(team, "private")
	if err != nil {
		return "", err
	}
	private, err := privateJob.CreateBuild("brine-user")
	if err != nil {
		return "", err
	}
	publicPipeline, publicJob, err := buildFactoryJob(team, "public")
	if err != nil {
		return "", err
	}
	if err := publicPipeline.Expose(); err != nil {
		return "", err
	}
	public, err := publicJob.CreateBuild("brine-user")
	if err != nil {
		return "", err
	}
	run, err := privateJob.RerunBuild(private, "brine-user")
	if err != nil {
		return "", err
	}
	otherTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-factory-team"})
	if err != nil {
		return "", err
	}
	other, err := otherTeam.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	visible, _, err := database.BuildFactory.VisibleBuilds([]string{team.Name()}, db.Page{Limit: 10})
	if err != nil {
		return "", err
	}
	all, _, err := database.BuildFactory.AllBuilds(db.Page{Limit: 10})
	if err != nil {
		return "", err
	}
	publicOnly, _, err := database.BuildFactory.PublicBuilds(db.Page{Limit: 10})
	if err != nil {
		return "", err
	}
	expectedVisible := joinedInts([]int{oneOff.ID(), private.ID(), public.ID(), run.ID()})
	expectedAll := joinedInts([]int{oneOff.ID(), private.ID(), public.ID(), run.ID(), other.ID()})
	return fmt.Sprintf("visible=%t;all=%t;public=%t;private-pipeline=%t", joinedBuildForAPIIDs(visible) == expectedVisible, joinedBuildForAPIIDs(all) == expectedAll, joinedBuildForAPIIDs(publicOnly) == fmt.Sprint(public.ID()), privatePipeline.Name() == "factory-pipeline-private"), nil
}

func observeBuildFactoryDrainable(database JetbridgeDB, team db.Team) (string, error) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "factory-drainable"},
		atc.Config{
			Jobs: atc.JobConfigs{{Name: "job"}},
			Resources: atc.ResourceConfigs{{
				Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"},
			}},
		}, 0, false,
	)
	if err != nil {
		return "", err
	}
	job, found, err := pipeline.Job("job")
	if err != nil || !found {
		return "", fmt.Errorf("load drainable job: found=%t: %w", found, err)
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return "", fmt.Errorf("load drainable resource: found=%t: %w", found, err)
	}
	resourceTypes, err := pipeline.ResourceTypes()
	if err != nil {
		return "", err
	}
	checkFactory := db.NewCheckFactory(database.Conn, database.LockFactory, make(chan db.Build, 1), util.NewSequenceGenerator(1))
	check, created, err := checkFactory.TryCreateCheck(
		lagerctx.NewContext(context.Background(), lagertest.NewTestLogger("build-factory-drainable")),
		resource, resourceTypes, nil, false, false, true,
	)
	if err != nil || !created {
		return "", fmt.Errorf("create persisted check: created=%t: %w", created, err)
	}
	if err := check.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	drainable, err := job.CreateBuild("brine-user")
	if err != nil {
		return "", err
	}
	if err := drainable.Finish(db.BuildStatusFailed); err != nil {
		return "", err
	}
	drained, err := job.CreateBuild("brine-user")
	if err != nil {
		return "", err
	}
	if err := drained.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	if err := drained.SetDrained(true); err != nil {
		return "", err
	}
	if _, err := team.CreateOneOffBuild(); err != nil {
		return "", err
	}
	started, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	if _, err := started.Start(atc.Plan{}); err != nil {
		return "", err
	}
	builds, err := database.BuildFactory.GetDrainableBuilds()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("count=%d;match=%t", len(builds), joinedBuildIDs(builds) == fmt.Sprint(drainable.ID())), nil
}

func joinedBuildIDs(builds []db.Build) string {
	ids := make([]int, 0, len(builds))
	for _, build := range builds {
		ids = append(ids, build.ID())
	}
	return joinedInts(ids)
}

func joinedBuildForAPIIDs(builds []db.BuildForAPI) string {
	ids := make([]int, 0, len(builds))
	for _, build := range builds {
		ids = append(ids, build.ID())
	}
	return joinedInts(ids)
}

func joinedInts(values []int) string {
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprint(value))
	}
	return strings.Join(parts, ",")
}
