package api_test

import (
	"context"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
)

// apiWorkerRuntime is the half of a worker that is not the database: the
// volumes and containers it holds by handle, and whether reaching it fails.
// Every decision the API asks a worker.Pool to make -- which worker holds a
// handle, whether that worker is running, which worker a new artifact lands on
// -- is left to the real pool over the real database. Only this last hop is
// stood in for, because a unit test has no worker process to talk to.
type apiWorkerRuntime struct {
	mu sync.Mutex

	volumes    map[string]runtime.Volume
	containers map[string]runtime.Container

	volumeErr    error
	containerErr error

	artifactVolume  runtime.Volume
	artifact        db.WorkerArtifact
	artifactTeamIDs []int
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

func (rt *apiWorkerRuntime) failVolumeLookup(err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.volumeErr = err
}

func (rt *apiWorkerRuntime) failContainerLookup(err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.containerErr = err
}

func (rt *apiWorkerRuntime) createsArtifact(volume runtime.Volume, artifact db.WorkerArtifact) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.artifactVolume = volume
	rt.artifact = artifact
}

func (rt *apiWorkerRuntime) artifactTeamIDSnapshot() []int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]int(nil), rt.artifactTeamIDs...)
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

	if w.runtime.volumeErr != nil {
		return nil, false, w.runtime.volumeErr
	}

	volume, found := w.runtime.volumes[handle]
	return volume, found, nil
}

func (w poolWorker) LookupContainer(_ context.Context, handle string) (runtime.Container, bool, error) {
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()

	if w.runtime.containerErr != nil {
		return nil, false, w.runtime.containerErr
	}

	container, found := w.runtime.containers[handle]
	return container, found, nil
}

func (w poolWorker) CreateVolumeForArtifact(_ context.Context, teamID int) (runtime.Volume, db.WorkerArtifact, error) {
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()

	w.runtime.artifactTeamIDs = append(w.runtime.artifactTeamIDs, teamID)
	return w.runtime.artifactVolume, w.runtime.artifact, nil
}
