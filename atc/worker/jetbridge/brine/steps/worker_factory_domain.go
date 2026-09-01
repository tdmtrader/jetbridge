package steps

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type WorkerFactoryObservation struct{ Value string }

func WorkerFactoryDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, WorkerFactoryObservation](
			"the real worker factory handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (WorkerFactoryObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return WorkerFactoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeStrictWorkerFactory(database, profile)
				return WorkerFactoryObservation{Value: value}, err
			},
		),
		CheckString[WorkerFactoryObservation]("the worker factory result is {string}", "worker factory result", func(in WorkerFactoryObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeWorkerFactory(database JetbridgeDB, profile string) (string, error) {
	worker := atc.Worker{
		Ephemeral: true, ActiveContainers: 140, ActiveVolumes: 550,
		ResourceTypes: []atc.WorkerResourceType{
			{Type: "some-type", Image: "some-image", Version: "some-version", Privileged: true},
			{Type: "other-type", Image: "other-image", Version: "other-version"},
		},
		Platform: "some-platform", Tags: atc.Tags{"some", "tags"}, Name: "some-name",
		StartTime: 1565367209, Version: "1.0.0", State: string(db.WorkerStateRunning),
	}
	save := func(value atc.Worker) (db.Worker, error) {
		return database.WorkerFactory.SaveWorker(value, 5*time.Minute)
	}
	switch profile {
	case "save-new":
		saved, err := save(worker)
		if err != nil {
			return "", err
		}
		version := ""
		if saved.Version() != nil {
			version = *saved.Version()
		}
		return fmt.Sprintf("name=%s;state=%s;version=%s", saved.Name(), saved.State(), version), nil
	case "save-types":
		if _, err := save(worker); err != nil {
			return "", err
		}
		return workerTypeCount(database, worker.Name)
	case "remove-type":
		if _, err := save(worker); err != nil {
			return "", err
		}
		worker.ResourceTypes = worker.ResourceTypes[1:]
		if _, err := save(worker); err != nil {
			return "", err
		}
		return workerTypeCount(database, worker.Name)
	case "replace-image", "replace-version":
		if _, err := save(worker); err != nil {
			return "", err
		}
		before, err := workerTypeIDs(database, worker.Name)
		if err != nil {
			return "", err
		}
		if profile == "replace-image" {
			worker.ResourceTypes[0].Image = "new-image"
		} else {
			worker.ResourceTypes[0].Version = "new-version"
		}
		if _, err := save(worker); err != nil {
			return "", err
		}
		after, err := workerTypeIDs(database, worker.Name)
		return fmt.Sprintf("changed=%t;other-stable=%t", before["some-type"] != after["some-type"], before["other-type"] == after["other-type"]), err
	case "update-version":
		worker.Version = ""
		first, err := save(worker)
		if err != nil {
			return "", err
		}
		worker.Version = "2.0.0"
		second, err := save(worker)
		return fmt.Sprintf("before-nil=%t;after=%s", first.Version() == nil, *second.Version()), err
	case "get":
		if _, err := save(worker); err != nil {
			return "", err
		}
		foundWorker, found, err := database.WorkerFactory.GetWorker(worker.Name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("found=%t;name=%s;ephemeral=%t;containers=%d;volumes=%d;platform=%s;tags=%s;types=%d", found, foundWorker.Name(), foundWorker.Ephemeral(), foundWorker.ActiveContainers(), foundWorker.ActiveVolumes(), foundWorker.Platform(), strings.Join(foundWorker.Tags(), ","), len(foundWorker.ResourceTypes())), nil
	case "missing":
		missing, found, err := database.WorkerFactory.GetWorker("missing")
		return fmt.Sprintf("found=%t;nil=%t", found, missing == nil), err
	case "visible":
		teamA, err := database.TeamFactory.CreateTeam(atc.Team{Name: "team-a"})
		if err != nil {
			return "", err
		}
		teamB, err := database.TeamFactory.CreateTeam(atc.Team{Name: "team-b"})
		if err != nil {
			return "", err
		}
		if _, err := save(worker); err != nil {
			return "", err
		}
		worker.Name = "team-a-worker"
		if _, err := teamA.SaveWorker(worker, 0); err != nil {
			return "", err
		}
		worker.Name = "team-b-worker"
		if _, err := teamB.SaveWorker(worker, 0); err != nil {
			return "", err
		}
		visible, err := database.WorkerFactory.VisibleWorkers([]string{"team-a"})
		names := workerFactoryNames(visible)
		return strings.Join(names, ","), err
	case "visible-empty":
		workers, err := database.WorkerFactory.VisibleWorkers([]string{"team-a"})
		return fmt.Sprintf("count=%d", len(workers)), err
	case "workers":
		if _, err := save(worker); err != nil {
			return "", err
		}
		worker.Name = "second"
		if _, err := save(worker); err != nil {
			return "", err
		}
		workers, err := database.WorkerFactory.Workers()
		return strings.Join(workerFactoryNames(workers), ","), err
	case "workers-empty":
		workers, err := database.WorkerFactory.Workers()
		return fmt.Sprintf("count=%d", len(workers)), err
	case "owner-check":
		return observeWorkerCheckOwner(database)
	case "owner-creating", "owner-created":
		return observeWorkerBuildOwner(database, profile == "owner-created")
	case "owner-missing":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "owner-missing-team"})
		if err != nil {
			return "", err
		}
		workers, err := database.WorkerFactory.FindWorkersForContainerByOwner(
			db.NewBuildStepContainerOwner(999999, atc.PlanID("missing"), team.ID()),
		)
		return fmt.Sprintf("count=%d", len(workers)), err
	case "build-counts":
		return observeWorkerBuildCounts(database)
	default:
		return "", fmt.Errorf("unknown worker factory profile %q", profile)
	}
}

func workerFactoryCapablePayload(name string) atc.Worker {
	return atc.Worker{
		Name: name, Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{
			Type: "some-base-resource-type", Image: "example/base", Version: "base-v1",
		}},
	}
}

func saveWorkerFactoryPayload(database JetbridgeDB, payload atc.Worker) (db.Worker, error) {
	if _, err := database.WorkerFactory.SaveWorker(payload, 0); err != nil {
		return nil, err
	}
	worker, found, err := database.WorkerFactory.GetWorker(payload.Name)
	if err != nil || !found {
		return nil, firstError(err, fmt.Errorf("worker %q was not found after save", payload.Name))
	}
	return worker, nil
}

func workerFactoryCheckOwner(database JetbridgeDB) (db.ContainerOwner, error) {
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).
		FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"repository": "example/image"}, nil)
	if err != nil {
		return nil, err
	}
	return db.NewResourceConfigCheckSessionContainerOwner(
		config.ID(), config.OriginBaseResourceType().ID,
		db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: 5 * time.Minute},
	), nil
}

func observeWorkerCheckOwner(database JetbridgeDB) (string, error) {
	owner, err := workerFactoryCheckOwner(database)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"first", "second", "third"} {
		worker, saveErr := saveWorkerFactoryPayload(database, workerFactoryCapablePayload(name))
		if saveErr != nil {
			return "", saveErr
		}
		if _, saveErr = worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeCheck}); saveErr != nil {
			return "", saveErr
		}
	}
	workers, err := database.WorkerFactory.FindWorkersForContainerByOwner(owner)
	return strings.Join(workerFactoryNames(workers), ","), err
}

func observeWorkerBuildOwner(database JetbridgeDB, created bool) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "owner-build-team"})
	if err != nil {
		return "", err
	}
	other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "owner-build-other-team"})
	if err != nil {
		return "", err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	worker, err := saveWorkerFactoryPayload(database, workerFactoryCapablePayload("owner-build-worker"))
	if err != nil {
		return "", err
	}
	owner := db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("plan"), team.ID())
	container, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeTask})
	if err != nil {
		return "", err
	}
	if created {
		if _, err = container.Created(); err != nil {
			return "", err
		}
	}
	mine, err := database.WorkerFactory.FindWorkersForContainerByOwner(owner)
	if err != nil {
		return "", err
	}
	theirs, err := database.WorkerFactory.FindWorkersForContainerByOwner(
		db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("plan"), other.ID()),
	)
	return fmt.Sprintf("mine=%d;other-team=%d", len(mine), len(theirs)), err
}

func observeWorkerBuildCounts(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "worker-count-team"})
	if err != nil {
		return "", err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	checkOwner, err := workerFactoryCheckOwner(database)
	if err != nil {
		return "", err
	}
	buildOwner := db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("plan"), team.ID())
	for _, name := range []string{"first", "second"} {
		worker, saveErr := saveWorkerFactoryPayload(database, workerFactoryCapablePayload(name))
		if saveErr != nil {
			return "", saveErr
		}
		if _, saveErr = worker.CreateContainer(buildOwner, db.ContainerMetadata{Type: db.ContainerTypeTask}); saveErr != nil {
			return "", saveErr
		}
		if _, saveErr = worker.CreateContainer(checkOwner, db.ContainerMetadata{Type: db.ContainerTypeCheck}); saveErr != nil {
			return "", saveErr
		}
	}
	counts, err := database.WorkerFactory.BuildContainersCountPerWorker()
	return fmt.Sprintf("first=%d;second=%d;workers=%d", counts["first"], counts["second"], len(counts)), err
}

func workerTypeCount(database JetbridgeDB, name string) (string, error) {
	var count int
	err := database.Conn.QueryRow(`SELECT count(*) FROM worker_base_resource_types WHERE worker_name = $1`, name).Scan(&count)
	return fmt.Sprintf("count=%d", count), err
}

func workerTypeIDs(database JetbridgeDB, name string) (map[string]int, error) {
	rows, err := database.Conn.Query(`
		SELECT w.id, b.name
		FROM worker_base_resource_types w
		JOIN base_resource_types b ON w.base_resource_type_id = b.id
		WHERE w.worker_name = $1`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]int{}
	for rows.Next() {
		var id int
		var typeName string
		if err := rows.Scan(&id, &typeName); err != nil {
			return nil, err
		}
		ids[typeName] = id
	}
	return ids, rows.Err()
}

func workerFactoryNames(workers []db.Worker) []string {
	names := make([]string, 0, len(workers))
	for _, worker := range workers {
		names = append(names, worker.Name())
	}
	sort.Strings(names)
	return names
}
