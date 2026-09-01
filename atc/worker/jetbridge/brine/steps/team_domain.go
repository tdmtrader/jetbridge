package steps

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
)

type TeamDomainObservation struct{ Value string }

// TeamDomainDefinitions exercises Team through its real PostgreSQL-backed
// implementation. Each Brine scenario gets a fresh database, so there are no
// factory, cache, worker, pipeline, or build doubles in this contract.
func TeamDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, TeamDomainObservation](
			"the real team domain evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (TeamDomainObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return TeamDomainObservation{}, fmt.Errorf("expected team domain profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return TeamDomainObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeTeamDomain(database, profile)
				return TeamDomainObservation{Value: value}, err
			},
		),
		CheckString[TeamDomainObservation]("the team domain observation is {string}", "team domain observation",
			func(in TeamDomainObservation) (string, error) { return in.Value, nil }),
		CheckContains[TeamDomainObservation]("the team domain observation contains {string}", "team domain observation",
			func(in TeamDomainObservation) (string, error) { return in.Value, nil }),
	}
}

func observeTeamDomain(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return "", err
	}
	other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
	if err != nil {
		return "", err
	}
	switch profile {
	case "delete":
		id := other.ID()
		if err := other.Delete(); err != nil {
			return "", err
		}
		_, found, err := database.TeamFactory.FindTeam("other-team")
		if err != nil {
			return "", err
		}
		var tableExists bool
		err = database.Conn.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", fmt.Sprintf("team_build_events_%d", id)).Scan(&tableExists)
		return fmt.Sprintf("found=%t;event-table=%t", found, tableExists), err
	case "rename":
		if err := team.Rename("renamed-team"); err != nil {
			return "", err
		}
		_, found, err := database.TeamFactory.FindTeam("renamed-team")
		return fmt.Sprintf("renamed-found=%t", found), err
	case "worker-overwrite":
		worker := teamDomainWorker("worker")
		if _, err := team.SaveWorker(worker, 5*time.Minute); err != nil {
			return "", err
		}
		saved, err := team.SaveWorker(worker, 5*time.Minute)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("name=%s;state=%s", saved.Name(), saved.State()), nil
	case "worker-cross-team":
		worker := teamDomainWorker("worker")
		if _, err := other.SaveWorker(worker, 5*time.Minute); err != nil {
			return "", err
		}
		_, err := team.SaveWorker(worker, 5*time.Minute)
		return configError(err), nil
	case "workers/team-and-global":
		if _, err := team.SaveWorker(teamDomainWorker("team-worker"), 0); err != nil {
			return "", err
		}
		if _, err := database.WorkerFactory.SaveWorker(teamDomainWorker("global-worker"), 0); err != nil {
			return "", err
		}
		workers, err := team.Workers()
		return workerNames(workers), err
	case "workers/excludes-other-team":
		if _, err := other.SaveWorker(teamDomainWorker("other-worker"), 0); err != nil {
			return "", err
		}
		workers, err := team.Workers()
		return workerNames(workers), err
	case "workers/empty":
		workers, err := database.WorkerFactory.Workers()
		return fmt.Sprintf("count=%d", len(workers)), err
	case "auth/save-and-clear-legacy":
		if _, err := database.Conn.Exec("UPDATE teams SET legacy_auth = $1 WHERE id = $2", `{"basicauth":{"username":"u","password":"p"}}`, team.ID()); err != nil {
			return "", err
		}
		auth := atc.TeamAuth{accessor.OwnerRole: {"users": {"local:username"}}}
		if err := team.UpdateProviderAuth(auth); err != nil {
			return "", err
		}
		var legacy sql.NullString
		if err := database.Conn.QueryRow("SELECT legacy_auth FROM teams WHERE id = $1", team.ID()).Scan(&legacy); err != nil {
			return "", err
		}
		return fmt.Sprintf("owner=%s;legacy-valid=%t", team.Auth()[accessor.OwnerRole]["users"][0], legacy.Valid), nil
	case "auth/override":
		if err := team.UpdateProviderAuth(atc.TeamAuth{accessor.OwnerRole: {"users": {"local:old"}}, accessor.ViewerRole: {"users": {"local:viewer"}}}); err != nil {
			return "", err
		}
		if err := team.UpdateProviderAuth(atc.TeamAuth{accessor.OwnerRole: {"users": {"local:new"}}}); err != nil {
			return "", err
		}
		return fmt.Sprintf("owner=%s;roles=%d", team.Auth()[accessor.OwnerRole]["users"][0], len(team.Auth())), nil
	case "pipelines/list":
		refs := []atc.PipelineRef{
			{Name: "fake-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
			{Name: "fake-pipeline", InstanceVars: atc.InstanceVars{"branch": "feature/foo"}},
			{Name: "fake-pipeline-two"},
		}
		for _, ref := range refs {
			if _, _, err := team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false); err != nil {
				return "", err
			}
		}
		pipelines, err := team.Pipelines()
		return pipelineRefs(pipelines), err
	case "pipelines/grouped-order":
		refs := []atc.PipelineRef{
			{Name: "fake-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
			{Name: "fake-pipeline", InstanceVars: atc.InstanceVars{"branch": "feature/foo"}},
			{Name: "fake-pipeline-two"},
			{Name: "fake-pipeline", InstanceVars: atc.InstanceVars{"branch": "other"}},
		}
		for _, ref := range refs {
			if _, _, err := team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false); err != nil {
				return "", err
			}
		}
		pipelines, err := team.Pipelines()
		return pipelineRefs(pipelines), err
	case "pipelines/empty":
		pipelines, err := team.Pipelines()
		return fmt.Sprintf("count=%d", len(pipelines)), err
	case "public-pipelines/one":
		if _, _, err := team.SavePipeline(atc.PipelineRef{Name: "private"}, atc.Config{}, 0, false); err != nil {
			return "", err
		}
		public, _, err := team.SavePipeline(atc.PipelineRef{Name: "public"}, atc.Config{}, 0, false)
		if err != nil {
			return "", err
		}
		if err := public.Expose(); err != nil {
			return "", err
		}
		pipelines, err := team.PublicPipelines()
		return pipelineRefs(pipelines), err
	case "public-pipelines/empty":
		pipelines, err := team.PublicPipelines()
		return fmt.Sprintf("count=%d", len(pipelines)), err
	case "order-pipelines":
		for _, name := range []string{"pipeline1", "pipeline2"} {
			if _, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, atc.Config{}, 0, false); err != nil {
				return "", err
			}
			if _, _, err := other.SavePipeline(atc.PipelineRef{Name: name}, atc.Config{}, 0, false); err != nil {
				return "", err
			}
		}
		if err := team.OrderPipelines([]string{"pipeline2", "pipeline1"}); err != nil {
			return "", err
		}
		if err := other.OrderPipelines([]string{"pipeline1", "pipeline2"}); err != nil {
			return "", err
		}
		mine, err := team.Pipelines()
		if err != nil {
			return "", err
		}
		theirs, err := other.Pipelines()
		return "mine=" + pipelineRefs(mine) + ";theirs=" + pipelineRefs(theirs), err
	case "order-pipelines/missing":
		if _, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline1"}, atc.Config{}, 0, false); err != nil {
			return "", err
		}
		err := team.OrderPipelines([]string{"pipeline1", "missing"})
		return configError(err), nil
	case "one-off-build":
		build, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("id=%t;name=%s;team=%s;job=%s;pipeline=%s;status=%s", build.ID() > 0, build.Name(), build.TeamName(), build.JobName(), build.PipelineName(), build.Status()), nil
	case "pipeline/instance-lookup":
		ref := atc.PipelineRef{Name: "release", InstanceVars: atc.InstanceVars{"branch": "feature"}}
		saved, _, err := team.SavePipeline(ref, atc.Config{}, 0, false)
		if err != nil {
			return "", err
		}
		foundPipeline, found, err := team.Pipeline(ref)
		return fmt.Sprintf("found=%t;same-id=%t", found, foundPipeline != nil && foundPipeline.ID() == saved.ID()), err
	case "pipeline/name-does-not-match-instance":
		if _, _, err := team.SavePipeline(atc.PipelineRef{Name: "release", InstanceVars: atc.InstanceVars{"branch": "feature"}}, atc.Config{}, 0, false); err != nil {
			return "", err
		}
		foundPipeline, found, err := team.Pipeline(atc.PipelineRef{Name: "release"})
		return fmt.Sprintf("found=%t;nil=%t", found, foundPipeline == nil), err
	case "pipeline/named-wins":
		if _, _, err := team.SavePipeline(atc.PipelineRef{Name: "release", InstanceVars: atc.InstanceVars{"branch": "feature"}}, atc.Config{}, 0, false); err != nil {
			return "", err
		}
		named, _, err := team.SavePipeline(atc.PipelineRef{Name: "release"}, atc.Config{}, 0, false)
		if err != nil {
			return "", err
		}
		foundPipeline, found, err := team.Pipeline(atc.PipelineRef{Name: "release"})
		return fmt.Sprintf("found=%t;same-id=%t", found, foundPipeline != nil && foundPipeline.ID() == named.ID()), err
	case "save-pipeline/default":
		pipeline, created, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, false)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("created=%t;team-id=%t;paused=%t;archived=%t", created, pipeline.TeamID() == team.ID(), pipeline.Paused(), pipeline.Archived()), nil
	case "save-pipeline/paused":
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, true)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("paused=%t", pipeline.Paused()), nil
	case "rename-pipeline/one":
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "old"}, atc.Config{}, 0, false)
		if err != nil {
			return "", err
		}
		found, err := team.RenamePipeline("old", "new")
		if err != nil {
			return "", err
		}
		_, err = pipeline.Reload()
		return fmt.Sprintf("found=%t;name=%s", found, pipeline.Name()), err
	case "rename-pipeline/instances":
		pipelines := []db.Pipeline{}
		for _, vars := range []atc.InstanceVars{{"version": "6"}, {"version": "7"}, nil} {
			pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "release", InstanceVars: vars}, atc.Config{}, 0, false)
			if err != nil {
				return "", err
			}
			pipelines = append(pipelines, pipeline)
		}
		found, err := team.RenamePipeline("release", "new")
		if err != nil {
			return "", err
		}
		names := []string{}
		for _, pipeline := range pipelines {
			if _, err := pipeline.Reload(); err != nil {
				return "", err
			}
			names = append(names, pipeline.Name())
		}
		return fmt.Sprintf("found=%t;names=%s", found, strings.Join(names, ",")), nil
	case "rename-pipeline/missing":
		found, err := team.RenamePipeline("missing", "new")
		return fmt.Sprintf("found=%t", found), err
	default:
		return "", fmt.Errorf("unknown team domain profile %q", profile)
	}
}

func teamDomainWorker(name string) atc.Worker {
	return atc.Worker{Name: name, Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: atc.Tags{"tag"}}
}

func workerNames(workers []db.Worker) string {
	names := make([]string, len(workers))
	for i := range workers {
		names[i] = workers[i].Name()
	}
	return strings.Join(names, ",")
}

func pipelineRefs(pipelines []db.Pipeline) string {
	refs := make([]string, len(pipelines))
	for i, pipeline := range pipelines {
		branch, _ := pipeline.InstanceVars()["branch"].(string)
		if branch != "" {
			refs[i] = pipeline.Name() + "@" + branch
		} else {
			refs[i] = pipeline.Name()
		}
	}
	return strings.Join(refs, ",")
}
