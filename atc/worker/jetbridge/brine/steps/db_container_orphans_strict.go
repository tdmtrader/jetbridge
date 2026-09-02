package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/util"
)

type DBContainerOrphanObservation struct{ Value string }

func DBContainerOrphansStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBContainerOrphanObservation](
			"the production orphan repository evaluates profile {string}", []string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBContainerOrphanObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBContainerOrphanObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeContainerOrphan(database, profile)
				return DBContainerOrphanObservation{Value: value}, err
			},
		),
		brine.DefineCheck[DBContainerOrphanObservation]("the orphan repository observation is {string}", func(in DBContainerOrphanObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if in.Value != want {
				return fmt.Errorf("expected orphan observation %q, got %q", want, in.Value)
			}
			return nil
		}),
	}
}

func observeContainerOrphan(database JetbridgeDB, profile string) (string, error) {
	var err error
	switch {
	case strings.HasPrefix(profile, "check-"):
		err = prepareOrphanCheck(database, profile)
	case strings.HasPrefix(profile, "build-"):
		err = prepareOrphanBuild(database, profile)
	case strings.HasPrefix(profile, "memory-"):
		err = prepareOrphanMemory(database, profile)
	default:
		return "", fmt.Errorf("unknown orphan profile %q", profile)
	}
	if err != nil {
		return "", err
	}
	creating, created, destroying, callErr := database.ContainerRepository.FindOrphanedContainers()
	return fmt.Sprintf("creating=%d;created=%d;destroying=%d;error=%t", len(creating), len(created), len(destroying), callErr != nil), nil
}

func prepareOrphanCheck(database JetbridgeDB, profile string) error {
	if err := persistContainerWorker(database, "orphan-worker", db.WorkerStateRunning); err != nil {
		return err
	}
	worker, found, err := database.WorkerFactory.GetWorker("orphan-worker")
	if err != nil || !found {
		return fmt.Errorf("load orphan worker: found=%t: %w", found, err)
	}
	factory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	config, err := factory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "source"}, nil)
	if err != nil {
		return err
	}
	owner := db.NewResourceConfigCheckSessionContainerOwner(config.ID(), config.OriginBaseResourceType().ID, db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour})
	container, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
	if err != nil {
		return err
	}
	if profile == "check-expired-created" || profile == "check-expired-destroying" {
		created, err := container.Created()
		if err != nil {
			return err
		}
		if profile == "check-expired-destroying" {
			if _, err := created.Destroying(); err != nil {
				return err
			}
		}
	}
	if profile == "check-config-cleaned" {
		if _, err := database.Conn.Exec(`DELETE FROM resource_config_check_sessions`); err != nil {
			return err
		}
		if err := factory.CleanUnreferencedConfigs(0); err != nil {
			return err
		}
	} else if profile == "check-worker-version-changed" {
		_, err = database.WorkerFactory.SaveWorker(atc.Worker{Name: "orphan-worker", Platform: "linux", State: string(db.WorkerStateRunning), ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-resource-type", Image: "example/base", Version: "v2"}}}, 0)
		if err != nil {
			return err
		}
	} else {
		if _, err := database.Conn.Exec(`UPDATE resource_config_check_sessions SET expires_at = NOW() - INTERVAL '1 second'`); err != nil {
			return err
		}
	}
	return db.NewResourceConfigCheckSessionLifecycle(database.Conn).CleanExpiredResourceConfigCheckSessions()
}

func orphanPipeline(database JetbridgeDB) (db.Pipeline, db.Job, db.Resource, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "orphan-team"})
	if err != nil {
		return nil, nil, nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "orphan-pipeline"}, atc.Config{
		Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}},
		Jobs:      atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "resource"}}}}},
	}, 0, false)
	if err != nil {
		return nil, nil, nil, err
	}
	job, found, err := pipeline.Job("job")
	if err != nil || !found {
		return nil, nil, nil, fmt.Errorf("load orphan job: found=%t: %w", found, err)
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return nil, nil, nil, fmt.Errorf("load orphan resource: found=%t: %w", found, err)
	}
	return pipeline, job, resource, nil
}

func prepareOrphanBuild(database JetbridgeDB, profile string) error {
	if err := persistContainerWorker(database, "orphan-worker", db.WorkerStateRunning); err != nil {
		return err
	}
	worker, found, err := database.WorkerFactory.GetWorker("orphan-worker")
	if err != nil || !found {
		return fmt.Errorf("load orphan worker: found=%t: %w", found, err)
	}
	pipeline, job, _, err := orphanPipeline(database)
	if err != nil {
		return err
	}
	build, err := job.CreateBuild("orphan-user")
	if err != nil {
		return err
	}
	container, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "simple-plan", build.TeamID()), db.ContainerMetadata{Type: db.ContainerTypeTask})
	if err != nil {
		return err
	}
	if profile == "build-interceptible" {
		if err := build.SetInterceptible(true); err != nil {
			return err
		}
	} else {
		if err := build.SetInterceptible(false); err != nil {
			return err
		}
	}
	if strings.HasSuffix(profile, "-created") || strings.HasSuffix(profile, "-destroying") {
		created, err := container.Created()
		if err != nil {
			return err
		}
		if strings.HasSuffix(profile, "-destroying") {
			if _, err := created.Destroying(); err != nil {
				return err
			}
		}
	}
	if profile == "build-deleted" {
		return pipeline.Destroy()
	}
	return nil
}

func prepareOrphanMemory(database JetbridgeDB, profile string) error {
	if err := persistContainerWorker(database, "orphan-worker", db.WorkerStateRunning); err != nil {
		return err
	}
	worker, found, err := database.WorkerFactory.GetWorker("orphan-worker")
	if err != nil || !found {
		return fmt.Errorf("load orphan worker: found=%t: %w", found, err)
	}
	_, _, resource, err := orphanPipeline(database)
	if err != nil {
		return err
	}
	build, err := resource.CreateInMemoryBuild(context.Background(), atc.Plan{}, util.NewSequenceGenerator(1))
	if err != nil {
		return err
	}
	if err := build.OnCheckBuildStart(); err != nil {
		return err
	}
	if _, err := worker.CreateContainer(build.ContainerOwner("simple-plan"), db.ContainerMetadata{Type: db.ContainerTypeCheck}); err != nil {
		return err
	}
	if profile == "memory-finished" {
		return build.Finish(db.BuildStatusSucceeded)
	}
	return nil
}
