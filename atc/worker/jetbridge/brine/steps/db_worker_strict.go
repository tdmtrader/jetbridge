package steps

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBWorkerStrictObservation struct{ Value string }

func DBWorkerStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBWorkerStrictObservation](
			"the production worker handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBWorkerStrictObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBWorkerStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBWorkerStrict(database, profile)
				return DBWorkerStrictObservation{Value: value}, err
			},
		),
		CheckString[DBWorkerStrictObservation]("the worker persistence result is {string}", "worker persistence result", func(in DBWorkerStrictObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

type dbWorkerStrictFixture struct {
	database JetbridgeDB
	worker   db.Worker
	other    db.Worker
	owner    db.ContainerOwner
}

func newDBWorkerStrictFixture(database JetbridgeDB) (dbWorkerStrictFixture, error) {
	if _, err := database.TeamFactory.CreateTeam(atc.Team{Name: "worker-domain-team"}); err != nil {
		return dbWorkerStrictFixture{}, err
	}
	payload := atc.Worker{
		Name: "worker-domain", Platform: "linux", Version: "1.2.3",
		State: string(db.WorkerStateRunning), Ephemeral: true, ActiveContainers: 140,
		ResourceTypes: []atc.WorkerResourceType{{
			Type: "worker-domain-type", Image: "example/worker-domain", Version: "v1",
		}},
	}
	worker, err := saveDBWorkerStrict(database, payload)
	if err != nil {
		return dbWorkerStrictFixture{}, err
	}
	payload.Name = "worker-domain-other"
	other, err := saveDBWorkerStrict(database, payload)
	if err != nil {
		return dbWorkerStrictFixture{}, err
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(
		"worker-domain-type", atc.Source{"repository": "example/image"}, nil,
	)
	if err != nil {
		return dbWorkerStrictFixture{}, err
	}
	owner := db.NewResourceConfigCheckSessionContainerOwner(
		config.ID(), config.OriginBaseResourceType().ID,
		db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour},
	)
	return dbWorkerStrictFixture{database: database, worker: worker, other: other, owner: owner}, nil
}

func saveDBWorkerStrict(database JetbridgeDB, payload atc.Worker) (db.Worker, error) {
	if _, err := database.WorkerFactory.SaveWorker(payload, 5*time.Minute); err != nil {
		return nil, err
	}
	worker, found, err := database.WorkerFactory.GetWorker(payload.Name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("worker %q was not persisted", payload.Name)
	}
	return worker, nil
}

func observeDBWorkerStrict(database JetbridgeDB, profile string) (string, error) {
	if profile == "delete" {
		worker, err := saveDBWorkerStrict(database, atc.Worker{Name: "worker-domain-delete", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning)})
		if err != nil {
			return "", err
		}
		if err := worker.Delete(); err != nil {
			return "", err
		}
		_, found, err := database.WorkerFactory.GetWorker(worker.Name())
		return fmt.Sprintf("found=%t", found), err
	}

	fixture, err := newDBWorkerStrictFixture(database)
	if err != nil {
		return "", err
	}
	switch profile {
	case "creating", "creating-other", "created", "created-other":
		creating, err := fixture.worker.CreateContainer(fixture.owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		if err != nil {
			return "", err
		}
		if profile == "created" || profile == "created-other" {
			if _, err := creating.Created(); err != nil {
				return "", err
			}
		}
		finder := fixture.worker
		if profile == "creating-other" || profile == "created-other" {
			finder = fixture.other
		}
		foundCreating, foundCreated, err := finder.FindContainer(fixture.owner)
		return fmt.Sprintf("creating=%t;created=%t", foundCreating != nil, foundCreated != nil), err
	case "check-dedup":
		creating, err := fixture.worker.CreateContainer(fixture.owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		if err != nil {
			return "", err
		}
		if _, err := creating.Failed(); err != nil {
			return "", err
		}
		destroyed, err := database.ContainerRepository.DestroyFailedContainers()
		if err != nil {
			return "", err
		}
		var before int
		if err := database.Conn.QueryRow("SELECT COUNT(*) FROM resource_config_check_sessions").Scan(&before); err != nil {
			return "", err
		}
		second, err := fixture.worker.CreateContainer(fixture.owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		if err != nil {
			return "", err
		}
		var after int
		if err := database.Conn.QueryRow("SELECT COUNT(*) FROM resource_config_check_sessions").Scan(&after); err != nil {
			return "", err
		}
		return fmt.Sprintf("destroyed=%d;before=%d;second=%t;after=%d", destroyed, before, second != nil, after), nil
	case "check-team":
		container, err := fixture.worker.CreateContainer(fixture.owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		if err != nil {
			return "", err
		}
		return dbWorkerStrictTeam(database, container.ID())
	case "build-team":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "worker-build-team"})
		if err != nil {
			return "", err
		}
		build, err := team.CreateOneOffBuild()
		if err != nil {
			return "", err
		}
		container, err := fixture.worker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("worker-plan"), team.ID()),
			db.ContainerMetadata{Type: db.ContainerTypeGet},
		)
		if err != nil {
			return "", err
		}
		return dbWorkerStrictTeam(database, container.ID())
	case "fixed-handle":
		owner := db.NewFixedHandleContainerOwner("worker-fixed-handle")
		creating, err := fixture.worker.CreateContainer(owner, db.ContainerMetadata{})
		if err != nil {
			return "", err
		}
		foundCreating, _, err := fixture.worker.FindContainer(owner)
		if err != nil {
			return "", err
		}
		created, err := creating.Created()
		if err != nil {
			return "", err
		}
		_, foundCreated, err := fixture.worker.FindContainer(owner)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("handle=%t;creating=%t;created=%t", creating.Handle() == "worker-fixed-handle", foundCreating != nil && foundCreating.ID() == creating.ID(), foundCreated != nil && foundCreated.ID() == created.ID()), nil
	default:
		return "", fmt.Errorf("unknown worker persistence profile %q", profile)
	}
}

func dbWorkerStrictTeam(database JetbridgeDB, containerID int) (string, error) {
	var teamID sql.NullInt64
	if err := database.Conn.QueryRow("SELECT team_id FROM containers WHERE id = $1", containerID).Scan(&teamID); err != nil {
		return "", err
	}
	return fmt.Sprintf("team-valid=%t", teamID.Valid), nil
}
