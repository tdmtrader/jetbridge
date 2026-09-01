package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/tracing"
)

type DBBuildStrictObservation struct{ Value string }

func DBBuildStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBBuildStrictObservation](
			"the strict real DB build evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBBuildStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBBuildStrictObservation{}, fmt.Errorf("expected strict DB build profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBBuildStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeDBBuildStrict(database, profile)
				return DBBuildStrictObservation{Value: value}, err
			},
		),
		CheckString[DBBuildStrictObservation](
			"the strict DB build observation is {string}",
			"strict DB build observation",
			func(observation DBBuildStrictObservation) (string, error) { return observation.Value, nil },
		),
	}
}

func observeDBBuildStrict(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-build-team"})
	if err != nil {
		return "", err
	}
	build, err := saveAuthBuild(team)
	if err != nil {
		return "", err
	}

	switch profile {
	case "created-by":
		if build.CreatedBy() == nil {
			return "<nil>", nil
		}
		return *build.CreatedBy(), nil
	case "one-off-no-plan":
		oneOff, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("has-plan=%t", oneOff.HasPlan()), nil
	case "create-time":
		return fmt.Sprintf("recent=%t", time.Since(build.CreateTime()) >= 0 && time.Since(build.CreateTime()) < time.Second), nil
	case "comment-round-trip":
		empty := build.Comment() == ""
		if err := build.SetComment("hello-world"); err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		first := build.Comment()
		if err := build.SetComment("updated-comment"); err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		return fmt.Sprintf("empty=%t;first=%s;second=%s", empty, first, build.Comment()), nil
	case "run-state":
		return fmt.Sprintf("matches=%t", build.RunStateID() == fmt.Sprintf("build:%d", build.ID())), nil
	case "associated-team":
		names := build.AllAssociatedTeamNames()
		return fmt.Sprintf("count=%d;same=%t", len(names), len(names) == 1 && names[0] == build.TeamName()), nil
	case "resource-cache-user":
		values := build.ResourceCacheUser().SQLMap()
		return fmt.Sprintf("build-id=%t", values["build_id"] == build.ID()), nil
	case "container-owner":
		values, err := build.ContainerOwner("strict-plan").Create(nil, "")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("build-id=%t;plan-id=%t;team-id=%t", values["build_id"] == build.ID(), values["plan_id"] == atc.PlanID("strict-plan"), values["team_id"] == build.TeamID()), nil
	case "lager/one-off", "lager/job", "lager/resource":
		selected, err := strictBuildKind(team, build, profile)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("matches=%t", reflect.DeepEqual(selected.LagerData(), expectedStrictLager(selected, profile))), nil
	case "syslog/one-off", "syslog/job", "syslog/resource":
		selected, err := strictBuildKind(team, build, profile)
		if err != nil {
			return "", err
		}
		origin := event.OriginID("strict-origin")
		return fmt.Sprintf("matches=%t", selected.SyslogTag(origin) == expectedStrictSyslog(selected, profile, origin)), nil
	case "tracing/one-off", "tracing/job", "tracing/resource":
		selected, err := strictBuildKind(team, build, profile)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("matches=%t", reflect.DeepEqual(selected.TracingAttrs(), expectedStrictTracing(selected, profile))), nil
	case "reload":
		if _, err := build.Start(atc.Plan{}); err != nil {
			return "", err
		}
		before := build.Status()
		found, err := build.Reload()
		return fmt.Sprintf("before=%s;found=%t;after=%s", before, found, build.Status()), err
	case "drain/default":
		return fmt.Sprintf("drained=%t", build.IsDrained()), nil
	case "drain/persisted":
		if err := build.SetDrained(true); err != nil {
			return "", err
		}
		immediate := build.IsDrained()
		found, err := build.Reload()
		return fmt.Sprintf("immediate=%t;found=%t;reloaded=%t", immediate, found, build.IsDrained()), err
	case "start/aborted-result", "start/aborted-status":
		if err := build.MarkAsAborted(); err != nil {
			return "", err
		}
		started, err := build.Start(strictBuildPlan())
		if err != nil {
			return "", err
		}
		if profile == "start/aborted-result" {
			return fmt.Sprintf("started=%t", started), nil
		}
		found, err := build.Reload()
		return fmt.Sprintf("found=%t;status=%s", found, build.Status()), err
	case "start/result":
		started, err := build.Start(strictBuildPlan())
		return fmt.Sprintf("started=%t", started), err
	case "start/event":
		if _, err := build.Start(strictBuildPlan()); err != nil {
			return "", err
		}
		if _, err := build.Reload(); err != nil {
			return "", err
		}
		source, err := build.Events(0)
		if err != nil {
			return "", err
		}
		defer source.Close()
		envelope, err := nextStrictBuildEvent(source)
		if err != nil {
			return "", err
		}
		var status event.Status
		if err := json.Unmarshal(*envelope.Data, &status); err != nil {
			return "", err
		}
		return fmt.Sprintf("type=%s;version=%s;id=%s;status=%s;time-matches=%t", envelope.Event, envelope.Version, envelope.EventID, status.Status, status.Time == build.StartTime().Unix()), nil
	case "start/status":
		if _, err := build.Start(strictBuildPlan()); err != nil {
			return "", err
		}
		found, err := build.Reload()
		return fmt.Sprintf("found=%t;status=%s", found, build.Status()), err
	case "start/public-plan":
		plan := strictBuildPlan()
		if _, err := build.Start(plan); err != nil {
			return "", err
		}
		found, err := build.Reload()
		return fmt.Sprintf("found=%t;has-plan=%t;matches=%t", found, build.HasPlan(), reflect.DeepEqual(build.PublicPlan(), plan.Public())), err
	case "pipeline":
		pipeline, found, err := build.Pipeline()
		return fmt.Sprintf("found=%t;same-id=%t;name=%s", found, pipeline != nil && pipeline.ID() == build.PipelineID(), strictPipelineName(pipeline)), err
	default:
		return "", fmt.Errorf("unknown strict DB build profile %q", profile)
	}
}

type strictBuildEventResult struct {
	envelope event.Envelope
	err      error
}

func nextStrictBuildEvent(source db.EventSource) (event.Envelope, error) {
	result := make(chan strictBuildEventResult, 1)
	go func() {
		envelope, err := source.Next()
		result <- strictBuildEventResult{envelope: envelope, err: err}
	}()

	select {
	case observed := <-result:
		return observed.envelope, observed.err
	case <-time.After(2 * time.Second):
		return event.Envelope{}, fmt.Errorf("timed out waiting for persisted build event")
	}
}

func strictBuildPlan() atc.Plan {
	return atc.Plan{ID: "strict-plan", Get: &atc.GetPlan{Name: "input", Type: "git", Resource: "repository"}}
}

func strictBuildKind(team db.Team, jobBuild db.Build, profile string) (db.Build, error) {
	switch {
	case profile == "lager/one-off" || profile == "syslog/one-off" || profile == "tracing/one-off":
		return team.CreateOneOffBuild()
	case profile == "lager/job" || profile == "syslog/job" || profile == "tracing/job":
		return jobBuild, nil
	default:
		resource, _, _, err := inMemoryBuildFixtureForTeam(team)
		if err != nil {
			return nil, err
		}
		resourceBuild, created, err := resource.CreateBuild(context.Background(), false, atc.Plan{})
		if err != nil {
			return nil, err
		}
		if !created {
			return nil, fmt.Errorf("resource build was not created")
		}
		return resourceBuild, nil
	}
}

func inMemoryBuildFixtureForTeam(team db.Team) (db.Resource, db.Pipeline, atc.Config, error) {
	config := atc.Config{Resources: atc.ResourceConfigs{{Name: "strict-resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}}}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "strict-resource-pipeline"}, config, 0, false)
	if err != nil {
		return nil, nil, atc.Config{}, err
	}
	resource, found, err := pipeline.Resource("strict-resource")
	if err != nil {
		return nil, nil, atc.Config{}, err
	}
	if !found {
		return nil, nil, atc.Config{}, fmt.Errorf("strict resource not found")
	}
	return resource, pipeline, config, nil
}

func expectedStrictLager(build db.Build, profile string) lager.Data {
	data := lager.Data{"build_id": build.ID(), "build": build.Name(), "team": build.TeamName()}
	if profile != "lager/one-off" {
		data["pipeline"] = build.PipelineName()
	}
	if profile == "lager/job" {
		data["job"] = build.JobName()
	}
	if profile == "lager/resource" {
		data["resource"] = build.ResourceName()
	}
	return data
}

func expectedStrictSyslog(build db.Build, profile string, origin event.OriginID) string {
	switch profile {
	case "syslog/one-off":
		return fmt.Sprintf("%s/%d/%s", build.TeamName(), build.ID(), origin)
	case "syslog/job":
		return fmt.Sprintf("%s/%s/%s/%s/%s", build.TeamName(), build.PipelineName(), build.JobName(), build.Name(), origin)
	default:
		return fmt.Sprintf("%s/%s/%s/%d/%s", build.TeamName(), build.PipelineName(), build.ResourceName(), build.ID(), origin)
	}
}

func expectedStrictTracing(build db.Build, profile string) tracing.Attrs {
	attrs := tracing.Attrs{"build_id": fmt.Sprintf("%d", build.ID()), "build": build.Name(), "team_name": build.TeamName()}
	if profile != "tracing/one-off" {
		attrs["pipeline"] = build.PipelineName()
	}
	if profile == "tracing/job" {
		attrs["job"] = build.JobName()
	}
	if profile == "tracing/resource" {
		attrs["resource"] = build.ResourceName()
	}
	return attrs
}

func strictPipelineName(pipeline db.Pipeline) string {
	if pipeline == nil {
		return "<nil>"
	}
	return pipeline.Name()
}
