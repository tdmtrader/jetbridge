package steps

import (
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type PipelineDomainObservation struct{ Value string }

func PipelineDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, PipelineDomainObservation](
			"the real pipeline domain handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (PipelineDomainObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return PipelineDomainObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeStrictPipelineDomain(database, profile)
				return PipelineDomainObservation{Value: value}, err
			},
		),
		CheckString[PipelineDomainObservation]("the pipeline domain result is {string}", "pipeline domain result", func(in PipelineDomainObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observePipelineDomain(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "pipeline-domain"})
	if err != nil {
		return "", err
	}
	config := atc.Config{
		Jobs:          atc.JobConfigs{{Name: "alpha"}, {Name: "beta"}},
		Resources:     atc.ResourceConfigs{{Name: "source", Type: "custom", Source: atc.Source{"repository": "example/source"}}},
		ResourceTypes: atc.ResourceTypes{{Name: "custom", Type: "registry-image", Source: atc.Source{"repository": "example/type"}}},
		Prototypes:    atc.Prototypes{{Name: "prototype", Type: "registry-image", Source: atc.Source{"repository": "example/prototype"}}},
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, config, 0, false)
	if err != nil {
		return "", err
	}
	reload := func() error {
		found, reloadErr := pipeline.Reload()
		if reloadErr != nil {
			return reloadErr
		}
		if !found {
			return fmt.Errorf("pipeline disappeared")
		}
		return nil
	}
	switch profile {
	case "check-unpaused":
		paused, err := pipeline.CheckPaused()
		return fmt.Sprintf("paused=%t", paused), err
	case "check-paused":
		if err := pipeline.Pause("brine-user"); err != nil {
			return "", err
		}
		paused, err := pipeline.CheckPaused()
		return fmt.Sprintf("paused=%t", paused), err
	case "pause":
		if err := pipeline.Pause(""); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("paused=%t", pipeline.Paused()), nil
	case "paused-by-empty", "paused-by-user":
		user := ""
		if profile == "paused-by-user" {
			user = "brine-user"
		}
		if err := pipeline.Pause(user); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		return "paused-by=" + pipeline.PausedBy(), nil
	case "paused-at":
		if err := pipeline.Pause(""); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("recent=%t", time.Since(pipeline.PausedAt()) < time.Second), nil
	case "unpause":
		if err := pipeline.Pause(""); err != nil {
			return "", err
		}
		if err := pipeline.Unpause(); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("paused=%t", pipeline.Paused()), nil
	case "unpause-schedules":
		jobs, err := pipeline.Jobs()
		if err != nil {
			return "", err
		}
		before := make(map[int]time.Time)
		for _, job := range jobs {
			before[job.ID()] = job.ScheduleRequestedTime()
		}
		if err := pipeline.Pause(""); err != nil {
			return "", err
		}
		if err := pipeline.Unpause(); err != nil {
			return "", err
		}
		advanced := 0
		for _, job := range jobs {
			found, err := job.Reload()
			if err != nil || !found {
				return "", firstError(err, fmt.Errorf("job disappeared"))
			}
			if job.ScheduleRequestedTime().After(before[job.ID()]) {
				advanced++
			}
		}
		return fmt.Sprintf("advanced=%d", advanced), nil
	case "jobs":
		jobs, err := pipeline.Jobs()
		return fmt.Sprintf("count=%d", len(jobs)), err
	case "builds":
		for _, name := range []string{"alpha", "beta"} {
			job, found, err := pipeline.Job(name)
			if err != nil || !found {
				return "", firstError(err, fmt.Errorf("job %s missing", name))
			}
			if _, err := job.CreateBuild("brine-user"); err != nil {
				return "", err
			}
		}
		if _, err := team.CreateOneOffBuild(); err != nil {
			return "", err
		}
		builds, _, err := pipeline.Builds(db.Page{Limit: 10})
		return fmt.Sprintf("count=%d", len(builds)), err
	case "started-metadata", "started-public-plan", "started-event":
		plan := atc.Plan{ID: "56", Task: &atc.TaskPlan{Name: "task", Config: &atc.TaskConfig{Run: atc.TaskRunConfig{Path: "true"}}}}
		build, err := pipeline.CreateStartedBuild(plan)
		if err != nil {
			return "", err
		}
		if profile == "started-metadata" {
			return fmt.Sprintf("id-positive=%t;pipeline=%s;team=%s;status=%s", build.ID() > 0, build.PipelineName(), build.TeamName(), build.Status()), nil
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		if profile == "started-public-plan" {
			return fmt.Sprintf("equal=%t", reflect.DeepEqual(build.PublicPlan(), plan.Public())), nil
		}
		var count int
		err = database.Conn.QueryRow(`SELECT count(*) FROM build_events WHERE build_id = $1`, build.ID()).Scan(&count)
		return fmt.Sprintf("events=%d", count), err
	case "resource-types":
		resourceTypes, err := pipeline.ResourceTypes()
		return fmt.Sprintf("count=%d", len(resourceTypes)), err
	case "config":
		actual, err := pipeline.Config()
		return fmt.Sprintf("equal=%t", reflect.DeepEqual(actual, config)), err
	}

	if profile == "destroy" || profile == "destroy-marker" {
		job, found, err := pipeline.Job("alpha")
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("alpha job missing"))
		}
		build, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		pipelineID := pipeline.ID()
		if err := pipeline.Destroy(); err != nil {
			return "", err
		}
		if profile == "destroy-marker" {
			var exists bool
			err := database.Conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM deleted_pipelines WHERE id = $1)`, pipelineID).Scan(&exists)
			return fmt.Sprintf("marked=%t", exists), err
		}
		pipelineFound, err := pipeline.Reload()
		if err != nil {
			return "", err
		}
		buildFound, err := build.Reload()
		return fmt.Sprintf("pipeline=%t;build=%t", pipelineFound, buildFound), err
	}

	if len(profile) > 8 && profile[:8] == "archive-" {
		before := pipeline.LastUpdated()
		if err := pipeline.Archive(); err != nil {
			return "", err
		}
		if err := reload(); err != nil {
			return "", err
		}
		switch profile {
		case "archive-state":
			return fmt.Sprintf("archived=%t", pipeline.Archived()), nil
		case "archive-updated":
			return fmt.Sprintf("advanced=%t", pipeline.LastUpdated().After(before)), nil
		case "archive-version":
			return fmt.Sprintf("version=%d", pipeline.ConfigVersion()), nil
		case "archive-jobs":
			items, err := pipeline.Jobs()
			if err != nil {
				return "", err
			}
			configs, err := items.Configs()
			return fmt.Sprintf("count=%d;empty=%t", len(configs), configs[0].Name == "" && configs[1].Name == ""), err
		case "archive-resources":
			items, err := pipeline.Resources()
			if err != nil {
				return "", err
			}
			configs := items.Configs()
			return fmt.Sprintf("count=%d;empty=%t", len(configs), configs[0].Name == ""), nil
		case "archive-resource-types":
			items, err := pipeline.ResourceTypes()
			if err != nil {
				return "", err
			}
			configs := items.Configs()
			return fmt.Sprintf("count=%d;source-empty=%t", len(configs), len(configs[0].Source) == 0), nil
		case "archive-prototypes":
			items, err := pipeline.Prototypes()
			if err != nil {
				return "", err
			}
			configs := items.Configs()
			return fmt.Sprintf("count=%d;source-empty=%t", len(configs), len(configs[0].Source) == 0), nil
		}
	}
	return "", fmt.Errorf("unknown pipeline domain profile %q", profile)
}
