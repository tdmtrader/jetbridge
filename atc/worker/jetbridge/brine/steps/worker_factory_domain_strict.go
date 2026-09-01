package steps

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func observeStrictWorkerFactory(database JetbridgeDB, profile string) (string, error) {
	cache, err := db.NewWorkerCache(lager.NewLogger("brine-worker-factory-domain"), database.Conn, 0)
	if err != nil {
		return "", err
	}
	database.WorkerFactory = db.NewWorkerFactory(database.Conn, cache)

	worker := atc.Worker{
		Ephemeral: true, ActiveContainers: 140, ActiveVolumes: 550,
		ResourceTypes: []atc.WorkerResourceType{
			{Type: "some-resource-type", Image: "some-image", Version: "some-version", Privileged: true},
			{Type: "other-resource-type", Image: "other-image", Version: "other-version"},
		},
		Platform: "some-platform", Tags: atc.Tags{"some", "tags"}, Name: "some-name",
		StartTime: 1565367209,
	}
	save := func(value atc.Worker) (db.Worker, error) {
		return database.WorkerFactory.SaveWorker(value, 5*time.Minute)
	}

	switch profile {
	case "save-existing-types":
		if _, err := save(worker); err != nil {
			return "", err
		}
		foundWorker, found, err := database.WorkerFactory.GetWorker(worker.Name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("equal=%t", found && reflect.DeepEqual(foundWorker.ResourceTypes(), worker.ResourceTypes)), nil
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
			worker.ResourceTypes[0].Image = "some-wild-new-image"
		} else {
			worker.ResourceTypes[0].Version = "some-wild-new-version"
		}
		if _, err := save(worker); err != nil {
			return "", err
		}
		after, err := workerTypeIDs(database, worker.Name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"before=%d;after=%d;maps-differ=%t;changed=%t;other-stable=%t",
			len(before), len(after), !reflect.DeepEqual(before, after),
			before["some-resource-type"] != after["some-resource-type"],
			before["other-resource-type"] == after["other-resource-type"],
		), nil
	case "update-version":
		first, err := save(worker)
		if err != nil {
			return "", err
		}
		worker.Version = "1.0.0"
		second, err := save(worker)
		if err != nil {
			return "", err
		}
		after := "nil"
		if second.Version() != nil {
			after = *second.Version()
		}
		return fmt.Sprintf("before-nil=%t;after=%s", first.Version() == nil, after), nil
	case "save-new":
		worker.Version = "1.0.0"
		saved, err := save(worker)
		if err != nil {
			return "", err
		}
		version := "nil"
		if saved.Version() != nil {
			version = *saved.Version()
		}
		return fmt.Sprintf("name=%s;state=%s;version=%s", saved.Name(), saved.State(), version), nil
	case "save-new-types":
		worker.Version = "1.0.0"
		if _, err := save(worker); err != nil {
			return "", err
		}
		return workerTypeCount(database, worker.Name)
	case "get":
		if _, err := save(worker); err != nil {
			return "", err
		}
		foundWorker, found, err := database.WorkerFactory.GetWorker(worker.Name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"found=%t;name=%s;state=%s;ephemeral=%t;containers=%d;volumes=%d;types-equal=%t;platform=%s;tags-equal=%t;start=%d;version-nil=%t",
			found, foundWorker.Name(), foundWorker.State(), foundWorker.Ephemeral(),
			foundWorker.ActiveContainers(), foundWorker.ActiveVolumes(),
			reflect.DeepEqual(foundWorker.ResourceTypes(), worker.ResourceTypes),
			foundWorker.Platform(), reflect.DeepEqual(foundWorker.Tags(), []string{"some", "tags"}),
			foundWorker.StartTime().Unix(), foundWorker.Version() == nil,
		), nil
	case "missing":
		missing, found, err := database.WorkerFactory.GetWorker("some-name")
		return fmt.Sprintf("found=%t;nil=%t", found, missing == nil), err
	case "visible":
		return observeStrictVisibleWorkers(database, worker)
	case "visible-empty":
		workers, err := database.WorkerFactory.VisibleWorkers([]string{"some-team"})
		return fmt.Sprintf("count=%d", len(workers)), err
	case "workers":
		if _, err := save(worker); err != nil {
			return "", err
		}
		worker.Name = "some-new-worker"
		if _, err := save(worker); err != nil {
			return "", err
		}
		workers, err := database.WorkerFactory.Workers()
		return fmt.Sprintf("count=%d;names=%s", len(workers), strings.Join(workerFactoryNames(workers), ",")), err
	case "workers-empty":
		workers, err := database.WorkerFactory.Workers()
		return fmt.Sprintf("count=%d", len(workers)), err
	case "owner-check":
		return observeStrictWorkerCheckOwner(database)
	case "owner-creating-return", "owner-creating-other", "owner-created-return", "owner-created-other":
		return observeStrictWorkerBuildOwner(database, profile)
	case "owner-missing":
		return observeStrictWorkerMissingOwner(database)
	case "build-counts":
		return observeStrictWorkerBuildCounts(database)
	default:
		return "", fmt.Errorf("unknown strict worker factory profile %q", profile)
	}
}

func observeStrictVisibleWorkers(database JetbridgeDB, worker atc.Worker) (string, error) {
	team1, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return "", err
	}
	team2, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-other-team"})
	if err != nil {
		return "", err
	}
	team3, err := database.TeamFactory.CreateTeam(atc.Team{Name: "not-this-team"})
	if err != nil {
		return "", err
	}
	if _, err := database.WorkerFactory.SaveWorker(worker, 0); err != nil {
		return "", err
	}
	worker.Name = "some-new-worker"
	if _, err := team1.SaveWorker(worker, 0); err != nil {
		return "", err
	}
	worker.Name = "some-other-new-worker"
	if _, err := team2.SaveWorker(worker, 0); err != nil {
		return "", err
	}
	worker.Name = "not-this-worker"
	if _, err := team3.SaveWorker(worker, 0); err != nil {
		return "", err
	}
	workers, err := database.WorkerFactory.VisibleWorkers([]string{"some-team", "some-other-team"})
	if err != nil {
		return "", err
	}
	names := workerFactoryNames(workers)
	excluded := true
	for _, name := range names {
		if name == "not-this-worker" {
			excluded = false
		}
	}
	return fmt.Sprintf("count=%d;names=%s;excluded=%t", len(workers), strings.Join(names, ","), excluded), nil
}

func strictWorkerFactoryPayload(name string) atc.Worker {
	return atc.Worker{
		Name: name,
		ResourceTypes: []atc.WorkerResourceType{{
			Type: "some-base-resource-type", Image: "example/base", Version: "base-v1",
		}},
	}
}

func strictWorkerCheckOwner(database JetbridgeDB) (db.ContainerOwner, error) {
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

func persistStrictWorker(database JetbridgeDB, name string) (db.Worker, error) {
	if _, err := database.WorkerFactory.SaveWorker(strictWorkerFactoryPayload(name), 0); err != nil {
		return nil, err
	}
	worker, found, err := database.WorkerFactory.GetWorker(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("worker %q not found after save", name)
	}
	return worker, nil
}

func observeStrictWorkerCheckOwner(database JetbridgeDB) (string, error) {
	owner, err := strictWorkerCheckOwner(database)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"some-other-name", "some-tagged-name", "some-team-name"} {
		worker, err := persistStrictWorker(database, name)
		if err != nil {
			return "", err
		}
		if _, err = worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeCheck}); err != nil {
			return "", err
		}
	}
	workers, err := database.WorkerFactory.FindWorkersForContainerByOwner(owner)
	return strings.Join(workerFactoryNames(workers), ","), err
}

func observeStrictWorkerBuildOwner(database JetbridgeDB, profile string) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "default-team"})
	if err != nil {
		return "", err
	}
	otherTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
	if err != nil {
		return "", err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	worker, err := persistStrictWorker(database, "default-worker")
	if err != nil {
		return "", err
	}
	owner := db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("simple-plan"), team.ID())
	container, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "some-task"})
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(profile, "owner-created") {
		if _, err = container.Created(); err != nil {
			return "", err
		}
	}
	queryOwner := owner
	if strings.HasSuffix(profile, "-other") {
		queryOwner = db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("simple-plan"), otherTeam.ID())
	}
	workers, err := database.WorkerFactory.FindWorkersForContainerByOwner(queryOwner)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(profile, "-other") {
		return fmt.Sprintf("count=%d", len(workers)), nil
	}
	name := ""
	if len(workers) > 0 {
		name = workers[0].Name()
	}
	return fmt.Sprintf("count=%d;name=%s", len(workers), name), nil
}

func observeStrictWorkerMissingOwner(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "default-team"})
	if err != nil {
		return "", err
	}
	if _, err = persistStrictWorker(database, "default-worker"); err != nil {
		return "", err
	}
	workers, err := database.WorkerFactory.FindWorkersForContainerByOwner(
		db.NewBuildStepContainerOwner(999999, atc.PlanID("how-could-this-happen-to-me"), team.ID()),
	)
	return fmt.Sprintf("count=%d", len(workers)), err
}

func observeStrictWorkerBuildCounts(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "default-team"})
	if err != nil {
		return "", err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	checkOwner, err := strictWorkerCheckOwner(database)
	if err != nil {
		return "", err
	}
	buildOwner := db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("simple-plan"), team.ID())
	for i, name := range []string{"default-worker", "some-name"} {
		worker, err := persistStrictWorker(database, name)
		if err != nil {
			return "", err
		}
		buildContainer, err := worker.CreateContainer(buildOwner, db.ContainerMetadata{Type: db.ContainerTypeTask})
		if err != nil {
			return "", err
		}
		if i == 0 {
			if _, err = buildContainer.Created(); err != nil {
				return "", err
			}
		}
		if _, err = worker.CreateContainer(checkOwner, db.ContainerMetadata{Type: db.ContainerTypeCheck}); err != nil {
			return "", err
		}
	}
	counts, err := database.WorkerFactory.BuildContainersCountPerWorker()
	return fmt.Sprintf("default-worker=%d;some-name=%d;workers=%d", counts["default-worker"], counts["some-name"], len(counts)), err
}
