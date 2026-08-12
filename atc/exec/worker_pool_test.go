package exec_test

import (
	"context"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
)

// stubWorkerFactory stands in for the K8s-backed DefaultFactory at the seam the
// real pool uses to turn a database worker into a runtime one, so specs can run
// the real pool against a runtimetest worker.
type stubWorkerFactory func(db.Worker) runtime.Worker

func (f stubWorkerFactory) NewWorker(_ lager.Logger, dbWorker db.Worker) runtime.Worker {
	return f(dbWorker)
}

// recordingPool notes the arguments a step handed the real pool. The pool's own
// lookup does not check every one of them — LocateVolume resolves a handle
// without consulting the team — so the arguments stay worth asserting.
type recordingPool struct {
	exec.Pool

	mu                sync.Mutex
	locateVolumeCalls []locateVolumeCall
}

type locateVolumeCall struct {
	ctx    context.Context
	teamID int
	handle string
}

func (pool *recordingPool) LocateVolume(ctx context.Context, teamID int, handle string) (runtime.Volume, runtime.Worker, bool, error) {
	pool.mu.Lock()
	pool.locateVolumeCalls = append(pool.locateVolumeCalls, locateVolumeCall{
		ctx:    ctx,
		teamID: teamID,
		handle: handle,
	})
	pool.mu.Unlock()

	return pool.Pool.LocateVolume(ctx, teamID, handle)
}

func (pool *recordingPool) LocateVolumeCallCount() int {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.locateVolumeCalls)
}

func (pool *recordingPool) LocateVolumeArgsForCall(i int) (context.Context, int, string) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	call := pool.locateVolumeCalls[i]
	return call.ctx, call.teamID, call.handle
}

// scriptedPool answers a step's worker-selection calls with results the spec
// supplies, recording the arguments it was handed. Which worker gets picked is
// not what these specs cover — each has a single worker, so the real pool's
// choice is forced — but the arguments the step passes are worth asserting.
type scriptedPool struct {
	selectedWorker  runtime.Worker
	selectWorkerErr error

	cacheVolume runtime.Volume
	cacheFound  bool
	cacheErr    error

	mu                           sync.Mutex
	findOrSelectWorkerCalls      []findOrSelectWorkerCall
	findResourceCacheVolumeCalls []findResourceCacheVolumeCall
}

type findOrSelectWorkerCall struct {
	ctx           context.Context
	owner         db.ContainerOwner
	containerSpec runtime.ContainerSpec
	workerSpec    worker.Spec
}

type findResourceCacheVolumeCall struct {
	ctx                 context.Context
	resourceCache       db.ResourceCache
	workerSpec          worker.Spec
	workerName          string
	shouldBeValidBefore time.Time
}

func (pool *scriptedPool) FindOrSelectWorker(
	ctx context.Context,
	owner db.ContainerOwner,
	containerSpec runtime.ContainerSpec,
	workerSpec worker.Spec,
) (runtime.Worker, error) {
	pool.mu.Lock()
	pool.findOrSelectWorkerCalls = append(pool.findOrSelectWorkerCalls, findOrSelectWorkerCall{
		ctx:           ctx,
		owner:         owner,
		containerSpec: containerSpec,
		workerSpec:    workerSpec,
	})
	pool.mu.Unlock()

	return pool.selectedWorker, pool.selectWorkerErr
}

func (pool *scriptedPool) FindOrSelectWorkerReturns(selected runtime.Worker, err error) {
	pool.selectedWorker = selected
	pool.selectWorkerErr = err
}

func (pool *scriptedPool) FindOrSelectWorkerCallCount() int {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.findOrSelectWorkerCalls)
}

func (pool *scriptedPool) FindOrSelectWorkerArgsForCall(i int) (context.Context, db.ContainerOwner, runtime.ContainerSpec, worker.Spec) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	call := pool.findOrSelectWorkerCalls[i]
	return call.ctx, call.owner, call.containerSpec, call.workerSpec
}

func (pool *scriptedPool) FindResourceCacheVolumeOnWorker(
	ctx context.Context,
	resourceCache db.ResourceCache,
	workerSpec worker.Spec,
	workerName string,
	shouldBeValidBefore time.Time,
) (runtime.Volume, bool, error) {
	pool.mu.Lock()
	pool.findResourceCacheVolumeCalls = append(pool.findResourceCacheVolumeCalls, findResourceCacheVolumeCall{
		ctx:                 ctx,
		resourceCache:       resourceCache,
		workerSpec:          workerSpec,
		workerName:          workerName,
		shouldBeValidBefore: shouldBeValidBefore,
	})
	pool.mu.Unlock()

	return pool.cacheVolume, pool.cacheFound, pool.cacheErr
}

func (pool *scriptedPool) FindResourceCacheVolumeOnWorkerReturns(volume runtime.Volume, found bool, err error) {
	pool.cacheVolume = volume
	pool.cacheFound = found
	pool.cacheErr = err
}

func (pool *scriptedPool) FindResourceCacheVolumeOnWorkerCallCount() int {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.findResourceCacheVolumeCalls)
}

func (pool *scriptedPool) FindResourceCacheVolumeOnWorkerArgsForCall(i int) (context.Context, db.ResourceCache, worker.Spec, string, time.Time) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	call := pool.findResourceCacheVolumeCalls[i]
	return call.ctx, call.resourceCache, call.workerSpec, call.workerName, call.shouldBeValidBefore
}

func (pool *scriptedPool) LocateVolume(context.Context, int, string) (runtime.Volume, runtime.Worker, bool, error) {
	return nil, nil, false, nil
}
