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
// volumes and containers it holds by handle, plus artifact volumes keyed by
// their owning team.
// Every decision the API asks a worker.Pool to make -- which worker holds a
// handle, whether that worker is running, which worker a new artifact lands on
// -- is left to the real pool over the real database. Only this last hop is
// stood in for, because a unit test has no worker process to talk to.
type apiWorkerRuntime struct {
	mu sync.Mutex

	volumes           map[string]runtime.Volume
	containers        map[string]runtime.Container
	artifactsByTeamID map[int]apiArtifact
}

type apiArtifact struct {
	volume   runtime.Volume
	artifact db.WorkerArtifact
}

func newAPIWorkerRuntime() *apiWorkerRuntime {
	return &apiWorkerRuntime{
		volumes:           map[string]runtime.Volume{},
		containers:        map[string]runtime.Container{},
		artifactsByTeamID: map[int]apiArtifact{},
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

func (rt *apiWorkerRuntime) addArtifact(teamID int, volume runtime.Volume, artifact db.WorkerArtifact) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.artifactsByTeamID[teamID] = apiArtifact{volume: volume, artifact: artifact}
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
	defer w.runtime.mu.Unlock()

	artifact, found := w.runtime.artifactsByTeamID[teamID]
	if !found {
		return nil, nil, fmt.Errorf("artifact for team %d not found", teamID)
	}
	return artifact.volume, artifact.artifact, nil
}
