package api_test

import (
	"context"
	"fmt"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
)

// apiWorkerRuntime is the half of a worker that is not the database: the
// volumes and containers it holds by handle, plus the real volume repository
// used to create artifact volumes on demand.
// Every decision the API asks a worker.Pool to make -- which worker holds a
// handle, whether that worker is running, which worker a new artifact lands on
// -- is left to the real pool over the real database. Only this last hop is
// stood in for, because a unit test has no worker process to talk to.
type apiWorkerRuntime struct {
	mu sync.Mutex

	volumes                  map[string]runtime.Volume
	containers               map[string]runtime.Container
	artifactVolumeRepository db.VolumeRepository
}

func newAPIWorkerRuntime() *apiWorkerRuntime {
	return &apiWorkerRuntime{
		volumes:    map[string]runtime.Volume{},
		containers: map[string]runtime.Container{},
	}
}

func (rt *apiWorkerRuntime) addVolume(volume runtime.Volume) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.volumes[volume.Handle()] = volume
}

func (rt *apiWorkerRuntime) addContainer(handle string, container runtime.Container) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.containers[handle] = container
}

func (rt *apiWorkerRuntime) connectArtifactVolumeRepository(repository db.VolumeRepository) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.artifactVolumeRepository = repository
}

func (rt *apiWorkerRuntime) volumeByHandle(handle string) (runtime.Volume, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	volume, found := rt.volumes[handle]
	return volume, found
}

// poolWorkerFactory is the worker.Factory the pool builds workers with.
type poolWorkerFactory struct {
	runtime *apiWorkerRuntime
}

func (factory poolWorkerFactory) NewWorker(_ lager.Logger, dbWorker db.Worker) runtime.Worker {
	return poolWorker{
		Worker:  runtimetest.NewWorker(dbWorker.Name()),
		runtime: factory.runtime,
	}
}

// poolWorker fills in the three methods a pool drives on the API's behalf.
// runtimetest.Worker panics in LookupContainer and CreateVolumeForArtifact,
// and its LookupVolume has no way to report an unreachable worker.
type poolWorker struct {
	*runtimetest.Worker

	runtime *apiWorkerRuntime
}

func (w poolWorker) LookupVolume(_ context.Context, handle string) (runtime.Volume, bool, error) {
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()

	volume, found := w.runtime.volumes[handle]
	return volume, found, nil
}

func (w poolWorker) LookupContainer(_ context.Context, handle string) (runtime.Container, bool, error) {
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()

	container, found := w.runtime.containers[handle]
	return container, found, nil
}

func (w poolWorker) CreateVolumeForArtifact(_ context.Context, teamID int) (runtime.Volume, db.WorkerArtifact, error) {
	w.runtime.mu.Lock()
	repository := w.runtime.artifactVolumeRepository
	w.runtime.mu.Unlock()

	if repository == nil {
		return nil, nil, fmt.Errorf("artifact volume repository is not connected")
	}

	creating, err := repository.CreateVolume(teamID, w.Name(), db.VolumeTypeArtifact)
	if err != nil {
		return nil, nil, fmt.Errorf("create artifact volume: %w", err)
	}
	created, err := creating.Created()
	if err != nil {
		return nil, nil, fmt.Errorf("transition artifact volume: %w", err)
	}
	artifact, err := created.InitializeArtifact("", 0)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize artifact: %w", err)
	}

	volume := runtimetest.NewVolume(created.Handle())
	w.runtime.addVolume(volume)
	return volume, artifact, nil
}
