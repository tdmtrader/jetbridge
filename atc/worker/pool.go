package worker

import (
	"context"
	"math/rand/v2"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
)

type Pool struct {
	factory Factory
	db      DB
}

func NewPool(factory Factory, db DB) Pool {
	return Pool{
		factory: factory,
		db:      db,
	}
}

func (pool Pool) FindOrSelectWorker(
	ctx context.Context,
	owner db.ContainerOwner,
	containerSpec runtime.ContainerSpec,
	workerSpec Spec,
) (runtime.Worker, error) {
	logger := lagerctx.FromContext(ctx)

	started := time.Now()
	labels := metric.StepsWaitingLabels{
		TeamId:   strconv.Itoa(workerSpec.TeamID),
		TeamName: containerSpec.TeamName,
		Type:     string(containerSpec.Type),
	}

	worker, err := pool.findOrSelectWorker(logger, owner, workerSpec)
	if err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, ErrNoWorkers
	}

	elapsed := time.Since(started)
	metric.StepsWaitingDuration{
		Labels:   labels,
		Duration: elapsed,
	}.Emit(logger)

	return pool.factory.NewWorker(logger, worker), nil
}

func (pool Pool) findOrSelectWorker(logger lager.Logger, owner db.ContainerOwner, workerSpec Spec) (db.Worker, error) {
	worker, compatibleWorkers, found, err := pool.findWorkerForContainer(logger, owner, workerSpec)
	if err != nil {
		return nil, err
	}
	if found {
		return worker, nil
	}
	if len(compatibleWorkers) == 0 {
		return nil, nil
	}
	return compatibleWorkers[0], nil
}

func (pool Pool) FindResourceCacheVolumeOnWorker(ctx context.Context, resourceCache db.ResourceCache, workerSpec Spec, workerName string, shouldBeValidBefore time.Time) (runtime.Volume, bool, error) {
	worker, found, err := pool.db.WorkerFactory.GetWorker(workerName)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	if !pool.isWorkerRunning(worker) {
		return nil, false, nil
	}
	return pool.findResourceCacheVolumeOnWorker(ctx, worker, resourceCache, shouldBeValidBefore)
}

func (pool Pool) findResourceCacheVolumeOnWorker(ctx context.Context, dbWorker db.Worker, resourceCache db.ResourceCache, shouldBeValidBefore time.Time) (runtime.Volume, bool, error) {
	volume, found, err := pool.db.VolumeRepo.FindResourceCacheVolume(dbWorker.Name(), resourceCache, shouldBeValidBefore)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	worker := pool.factory.NewWorker(lagerctx.FromContext(ctx), dbWorker)
	return worker.LookupVolume(ctx, volume.Handle())
}

func (pool Pool) FindWorkerForContainer(logger lager.Logger, owner db.ContainerOwner, workerSpec Spec) (runtime.Worker, bool, error) {
	worker, _, found, err := pool.findWorkerForContainer(logger, owner, workerSpec)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return pool.factory.NewWorker(logger, worker), true, nil
}

func (pool Pool) findWorkerForContainer(logger lager.Logger, owner db.ContainerOwner, workerSpec Spec) (db.Worker, []db.Worker, bool, error) {
	workersWithContainer, err := pool.db.WorkerFactory.FindWorkersForContainerByOwner(owner)
	if err != nil {
		return nil, nil, false, err
	}

	// In K8s, global resources is always the effective behavior — a check
	// container may run on any compatible worker for the same scope.
	for _, w := range workersWithContainer {
		if pool.isWorkerRunning(w) {
			return w, nil, true, nil
		}
	}

	compatibleWorkers, err := pool.allRunningWorkers(logger, workerSpec)
	if err != nil {
		return nil, nil, false, err
	}

	return nil, compatibleWorkers, false, nil
}

func (pool Pool) FindWorker(logger lager.Logger, name string) (runtime.Worker, bool, error) {
	worker, found, err := pool.db.WorkerFactory.GetWorker(name)
	if err != nil {
		logger.Error("failed-to-get-worker", err)
		return nil, false, err
	}
	if !found {
		logger.Info("worker-not-found", lager.Data{"worker": name})
		return nil, false, nil
	}
	return pool.factory.NewWorker(logger, worker), true, nil
}

func (pool Pool) LocateVolume(ctx context.Context, teamID int, handle string) (runtime.Volume, runtime.Worker, bool, error) {
	logger := lagerctx.FromContext(ctx).Session("worker-for-volume", lager.Data{"handle": handle, "team-id": teamID})
	team := pool.db.TeamFactory.GetByID(teamID)

	dbWorker, found, err := team.FindWorkerForVolume(handle)
	if err != nil {
		logger.Error("failed-to-find-worker", err)
		return nil, nil, false, err
	}
	if !found {
		return nil, nil, false, nil
	}
	if !pool.isWorkerRunning(dbWorker) {
		return nil, nil, false, nil
	}

	logger = logger.WithData(lager.Data{"worker": dbWorker.Name()})
	logger.Debug("found-volume-on-worker")

	worker := pool.factory.NewWorker(logger, dbWorker)

	volume, found, err := worker.LookupVolume(ctx, handle)
	if err != nil {
		logger.Error("failed-to-lookup-volume", err)
		return nil, nil, false, err
	}
	if !found {
		logger.Info("volume-disappeared-from-worker")
		return nil, nil, false, nil
	}

	return volume, worker, true, nil
}

func (pool Pool) LocateContainer(ctx context.Context, teamID int, handle string) (runtime.Container, runtime.Worker, bool, error) {
	logger := lagerctx.FromContext(ctx).Session("worker-for-container", lager.Data{"handle": handle, "team-id": teamID})
	team := pool.db.TeamFactory.GetByID(teamID)

	dbWorker, found, err := team.FindWorkerForContainer(handle)
	if err != nil {
		logger.Error("failed-to-find-worker", err)
		return nil, nil, false, err
	}
	if !found {
		return nil, nil, false, nil
	}
	if !pool.isWorkerRunning(dbWorker) {
		return nil, nil, false, nil
	}

	logger = logger.WithData(lager.Data{"worker": dbWorker.Name()})
	logger.Debug("found-container-on-worker")

	worker := pool.factory.NewWorker(logger, dbWorker)

	container, found, err := worker.LookupContainer(ctx, handle)
	if err != nil {
		logger.Error("failed-to-lookup-container", err)
		return nil, nil, false, err
	}
	if !found {
		logger.Info("container-disappeared-from-worker")
		return nil, nil, false, nil
	}

	return container, worker, true, nil
}

func (pool Pool) CreateVolumeForArtifact(ctx context.Context, spec Spec) (runtime.Volume, db.WorkerArtifact, error) {
	logger := lagerctx.FromContext(ctx)
	runningWorkers, err := pool.allRunningWorkers(logger, spec)
	if err != nil {
		return nil, nil, err
	}

	worker := pool.factory.NewWorker(logger, runningWorkers[rand.IntN(len(runningWorkers))])
	return worker.CreateVolumeForArtifact(ctx, spec.TeamID)
}

// allRunningWorkers returns all running workers, preferring team-scoped
// workers over general workers when both exist.
func (pool Pool) allRunningWorkers(logger lager.Logger, spec Spec) ([]db.Worker, error) {
	workers, err := pool.db.WorkerFactory.Workers()
	if err != nil {
		return nil, err
	}

	if len(workers) == 0 {
		return nil, ErrNoWorkers
	}

	var teamWorkers []db.Worker
	var generalWorkers []db.Worker
	for _, worker := range workers {
		if !pool.isWorkerRunning(worker) {
			continue
		}
		if worker.TeamID() != 0 {
			if spec.TeamID == worker.TeamID() {
				teamWorkers = append(teamWorkers, worker)
			}
		} else {
			generalWorkers = append(generalWorkers, worker)
		}
	}

	if len(teamWorkers) != 0 {
		return teamWorkers, nil
	}

	if len(generalWorkers) != 0 {
		return generalWorkers, nil
	}

	return nil, ErrNoWorkers
}

// isWorkerRunning checks that a worker is in the running state.
func (pool Pool) isWorkerRunning(worker db.Worker) bool {
	return worker.State() == db.WorkerStateRunning
}
