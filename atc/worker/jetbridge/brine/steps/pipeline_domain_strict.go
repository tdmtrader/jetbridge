package steps

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
)

func observeStrictPipelineDomain(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return "", err
	}
	config := strictPipelineDomainConfig()
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "fake-pipeline"}, config, 0, false)
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
		if err := pipeline.Pause(""); err != nil {
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
			user = "concourse"
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
		delta := time.Since(pipeline.PausedAt())
		return fmt.Sprintf("within-one-second=%t", delta >= -time.Second && delta <= time.Second), nil
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
		before := make(map[int]time.Time, len(jobs))
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
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(jobs))
		for _, job := range jobs {
			names = append(names, job.Name())
		}
		return strings.Join(names, ","), nil
	case "builds":
		return observePipelineBuilds(team, pipeline)
	case "started-metadata", "started-public-plan", "started-event":
		return observePipelineStartedBuild(pipeline, profile)
	case "resource-types":
		resourceTypes, err := pipeline.ResourceTypes()
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(resourceTypes))
		for _, resourceType := range resourceTypes {
			names = append(names, resourceType.Name())
		}
		sort.Strings(names)
		return strings.Join(names, ","), nil
	case "config":
		actual, err := pipeline.Config()
		return fmt.Sprintf("equal=%t", reflect.DeepEqual(actual, config)), err
	case "destroy":
		return observePipelineDestroy(database, team)
	case "destroy-marker":
		pipelineID := pipeline.ID()
		if err := pipeline.Destroy(); err != nil {
			return "", err
		}
		var exists bool
		err := database.Conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM deleted_pipelines WHERE id = $1)`, pipelineID).Scan(&exists)
		return fmt.Sprintf("marked=%t", exists), err
	}

	if strings.HasPrefix(profile, "archive-") {
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
			expected := make(atc.JobConfigs, len(config.Jobs))
			return fmt.Sprintf("count=%d;equal=%t", len(configs), reflect.DeepEqual(configs, expected)), err
		case "archive-resources":
			items, err := pipeline.Resources()
			if err != nil {
				return "", err
			}
			configs := items.Configs()
			expected := make(atc.ResourceConfigs, len(config.Resources))
			return fmt.Sprintf("count=%d;equal=%t", len(configs), reflect.DeepEqual(configs, expected)), nil
		case "archive-resource-types":
			items, err := pipeline.ResourceTypes()
			if err != nil {
				return "", err
			}
			configs := items.Configs()
			expected := atc.ResourceTypes{{Name: "some-other-resource-type", Type: "base-type"}, {Name: "some-resource-type", Type: "base-type"}}
			return fmt.Sprintf("count=%d;equal=%t", len(configs), reflect.DeepEqual(configs, expected)), nil
		case "archive-prototypes":
			items, err := pipeline.Prototypes()
			if err != nil {
				return "", err
			}
			configs := items.Configs()
			expected := atc.Prototypes{{Name: "some-other-prototype", Type: "base-type"}, {Name: "some-prototype", Type: "base-type"}}
			return fmt.Sprintf("count=%d;equal=%t", len(configs), reflect.DeepEqual(configs, expected)), nil
		}
	}

	return "", fmt.Errorf("unknown pipeline domain profile %q", profile)
}

func strictPipelineDomainConfig() atc.Config {
	return atc.Config{
		Groups:     atc.GroupConfigs{{Name: "some-group", Jobs: []string{"job-1", "job-2"}, Resources: []string{"some-resource", "some-other-resource"}}},
		VarSources: atc.VarSourceConfigs{{Name: "some-var-source", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "pv"}}}},
		Display:    &atc.DisplayConfig{BackgroundImage: "background.jpg"},
		Jobs: atc.JobConfigs{
			{Name: "job-name", Public: true, Serial: true, SerialGroups: []string{"serial-group"}, PlanSequence: []atc.Step{
				{Config: &atc.PutStep{Name: "some-resource", Params: atc.Params{"some-param": "some-value"}}},
				{Config: &atc.GetStep{Name: "some-input", Resource: "some-resource", Params: atc.Params{"some-param": "some-value"}, Passed: []string{"job-1", "job-2"}, Trigger: true}},
				{Config: &atc.TaskStep{Name: "some-task", Privileged: true, ConfigPath: "some/config/path.yml", Config: &atc.TaskConfig{RootfsURI: "some-image"}}},
				{Config: &atc.SetPipelineStep{Name: "some-pipeline", File: "some-file", VarFiles: []string{"var-file1", "var-file2"}, Vars: map[string]any{"k1": "v1", "k2": "v2"}}},
			}},
			{Name: "some-other-job", Serial: true},
			{Name: "a-job"},
			{Name: "shared-job"},
			{Name: "random-job"},
			{Name: "job-1"},
			{Name: "job-2"},
			{Name: "other-serial-group-job", SerialGroups: []string{"serial-group", "really-different-group"}},
			{Name: "different-serial-group-job", SerialGroups: []string{"different-serial-group"}},
		},
		Resources: atc.ResourceConfigs{
			{Name: "some-other-resource", Type: "some-type", Source: atc.Source{"some": "other-source"}},
			{Name: "some-resource", Type: "some-type", Source: atc.Source{"some": "source"}},
		},
		ResourceTypes: atc.ResourceTypes{
			{Name: "some-other-resource-type", Type: "base-type", Source: atc.Source{"some": "other-type-soure"}},
			{Name: "some-resource-type", Type: "base-type", Source: atc.Source{"some": "type-soure"}},
		},
		Prototypes: atc.Prototypes{
			{Name: "some-other-prototype", Type: "base-type", Source: atc.Source{"some": "other-type-source"}},
			{Name: "some-prototype", Type: "base-type", Source: atc.Source{"some": "type-source"}},
		},
	}
}

func observePipelineBuilds(team db.Team, pipeline db.Pipeline) (string, error) {
	expected := map[int]bool{}
	for _, jobName := range []string{"job-name", "job-name", "some-other-job"} {
		job, found, err := pipeline.Job(jobName)
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("job %q missing", jobName))
		}
		build, err := job.CreateBuild("some-user")
		if err != nil {
			return "", err
		}
		expected[build.ID()] = true
	}
	oneOff, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	builds, _, err := pipeline.Builds(db.Page{Limit: 10})
	if err != nil {
		return "", err
	}
	matches := len(builds) == len(expected)
	oneOffExcluded := true
	for _, build := range builds {
		matches = matches && expected[build.ID()]
		oneOffExcluded = oneOffExcluded && build.ID() != oneOff.ID()
	}
	return fmt.Sprintf("count=%d;matches=%t;one-off-excluded=%t", len(builds), matches, oneOffExcluded), nil
}

func strictPipelineStartedPlan() atc.Plan {
	return atc.Plan{ID: "56", Get: &atc.GetPlan{
		Type: "some-type", Name: "some-name", Resource: "some-resource",
		Source: atc.Source{"some": "source"}, Params: atc.Params{"some": "params"},
		Version: &atc.Version{"some": "version"}, Tags: atc.Tags{"some-tags"},
	}}
}

func observePipelineStartedBuild(pipeline db.Pipeline, profile string) (string, error) {
	plan := strictPipelineStartedPlan()
	build, err := pipeline.CreateStartedBuild(plan)
	if err != nil {
		return "", err
	}
	switch profile {
	case "started-metadata":
		return fmt.Sprintf("id-positive=%t;job=%s;pipeline=%s;name=%s;team=%s;status=%s", build.ID() > 0, build.JobName(), build.PipelineName(), build.Name(), build.TeamName(), build.Status()), nil
	case "started-public-plan":
		found, err := build.Reload()
		return fmt.Sprintf("equal=%t", found && reflect.DeepEqual(build.PublicPlan(), plan.Public())), err
	case "started-event":
		found, err := build.Reload()
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("started build disappeared"))
		}
		source, err := build.Events(0)
		if err != nil {
			return "", err
		}
		defer source.Close()
		envelope, err := nextPipelineDomainEvent(source)
		if err != nil {
			return "", err
		}
		var status event.Status
		if envelope.Data == nil {
			return "", fmt.Errorf("persisted start event had nil data")
		}
		if err := json.Unmarshal(*envelope.Data, &status); err != nil {
			return "", err
		}
		return fmt.Sprintf("type=%s;version=%s;id=%s;status=%s;time-matches=%t", envelope.Event, envelope.Version, envelope.EventID, status.Status, status.Time == build.StartTime().Unix()), nil
	default:
		return "", fmt.Errorf("unknown started-build profile %q", profile)
	}
}

type pipelineDomainEventResult struct {
	envelope event.Envelope
	err      error
}

func nextPipelineDomainEvent(source db.EventSource) (event.Envelope, error) {
	result := make(chan pipelineDomainEventResult, 1)
	go func() {
		envelope, err := source.Next()
		result <- pipelineDomainEventResult{envelope: envelope, err: err}
	}()
	select {
	case observed := <-result:
		return observed.envelope, observed.err
	case <-time.After(2 * time.Second):
		return event.Envelope{}, fmt.Errorf("timed out waiting for persisted pipeline start event")
	}
}

func observePipelineDestroy(database JetbridgeDB, team db.Team) (string, error) {
	config := atc.Config{
		Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "some-type", Source: atc.Source{"some": "source"}}},
		Jobs:      atc.JobConfigs{{Name: "some-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-input", Resource: "some-resource"}}}}},
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "destroy-pipeline"}, config, 0, false)
	if err != nil {
		return "", err
	}
	resource, found, err := pipeline.Resource("some-resource")
	if err != nil || !found {
		return "", firstError(err, fmt.Errorf("destroy resource missing"))
	}
	resourceConfig, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return "", err
	}
	resourceID := resource.ID()
	scope, err := resourceConfig.FindOrCreateScope(&resourceID)
	if err != nil {
		return "", err
	}
	version := atc.Version{"key": "value"}
	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{version}); err != nil {
		return "", err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return "", err
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil || !found {
		return "", firstError(err, fmt.Errorf("destroy job missing"))
	}
	build, err := job.CreateBuild("some-user")
	if err != nil {
		return "", err
	}
	if err := build.SaveOutput("some-type", nil, atc.Source{"some": "source"}, version, nil, "some-output-name", "some-resource"); err != nil {
		return "", err
	}
	if err := build.SaveEvent(event.StartTask{}); err != nil {
		return "", err
	}
	if err := pipeline.Destroy(); err != nil {
		return "", err
	}
	pipelineFound, err := pipeline.Reload()
	if err != nil {
		return "", err
	}
	buildFound, err := build.Reload()
	if err != nil {
		return "", err
	}
	_, lookupFound, err := team.Pipeline(atc.PipelineRef{Name: pipeline.Name()})
	return fmt.Sprintf("pipeline=%t;build=%t;lookup=%t", pipelineFound, buildFound, lookupFound), err
}
