package db

import (
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// WorkerCache provides a thread-safe, in-memory cache of worker information
// from the database. It refreshes on notification signals and on a periodic
// interval as a fallback.
//
// All notification consumers use coalescing signals (NotifySignal). This means
// notifications are treated as "something changed" hints, not messages. On each
// signal, the cache does a full refresh from the database. Dropped notifications
// are harmless because the next signal that arrives triggers a complete rescan.
type WorkerCache struct {
	conn   DbConn
	logger lager.Logger

	// dataMut protects access to the cached data
	dataMut sync.RWMutex

	// refreshMut serializes refresh operations
	refreshMut sync.Mutex

	// Use atomic for refresh state to avoid locking
	refreshing      atomic.Bool
	lastRefresh     atomic.Int64 // Unix timestamp in nanoseconds
	refreshInterval time.Duration

	// Cached data
	workers               map[string]Worker
	workerContainerCounts map[string]int
}

func NewWorkerCache(logger lager.Logger, conn DbConn, refreshInterval time.Duration) (*WorkerCache, error) {
	cache := NewStaticWorkerCache(logger, conn, refreshInterval)

	workerSignal, err := conn.Bus().ListenSignal("worker_events_channel")
	if err != nil {
		return nil, err
	}

	containerSignal, err := conn.Bus().ListenSignal("container_events_channel")
	if err != nil {
		return nil, err
	}

	go cache.listenForChanges(workerSignal, containerSignal)

	return cache, nil
}

// NewStaticWorkerCache returns a WorkerCache that doesn't subscribe to changes
// to the workers/containers table, so its data is likely to be stale until
// the next refresh.
func NewStaticWorkerCache(logger lager.Logger, conn DbConn, refreshInterval time.Duration) *WorkerCache {
	cache := &WorkerCache{
		logger:                logger,
		conn:                  conn,
		refreshInterval:       refreshInterval,
		workers:               make(map[string]Worker),
		workerContainerCounts: make(map[string]int),
	}

	cache.lastRefresh.Store(0)

	return cache
}

func (cache *WorkerCache) copyWorkers() []Worker {
	cache.dataMut.RLock()
	defer cache.dataMut.RUnlock()

	workers := make([]Worker, 0, len(cache.workers))
	for _, worker := range cache.workers {
		workers = append(workers, worker)
	}
	return workers
}

func (cache *WorkerCache) copyWorkerContainerCounts() map[string]int {
	cache.dataMut.RLock()
	defer cache.dataMut.RUnlock()

	counts := make(map[string]int, len(cache.workerContainerCounts))
	maps.Copy(counts, cache.workerContainerCounts)
	return counts
}

func (cache *WorkerCache) Workers() ([]Worker, error) {
	if cache.needsRefresh() {
		err := cache.refreshWorkerData()
		if err != nil {
			return nil, err
		}
	}

	return cache.copyWorkers(), nil
}

func (cache *WorkerCache) WorkerContainerCounts() (map[string]int, error) {
	if cache.needsRefresh() {
		err := cache.refreshWorkerData()
		if err != nil {
			return nil, err
		}
	}

	return cache.copyWorkerContainerCounts(), nil
}

// listenForChanges waits for either worker or container change signals and
// triggers a full cache refresh. Both signals coalesce — multiple rapid
// changes produce a single refresh.
func (cache *WorkerCache) listenForChanges(workerSignal, containerSignal *NotifySignal) {
	for {
		select {
		case <-workerSignal.C():
		case <-containerSignal.C():
		}

		cache.ensureRefresh()

		// Eagerly refresh so the data is ready for the next reader,
		// rather than waiting for a Workers()/WorkerContainerCounts() call.
		if err := cache.refreshWorkerData(); err != nil {
			cache.logger.Error("failed-to-refresh-on-signal", err)
		}
	}
}

func (cache *WorkerCache) refreshWorkerData() error {
	cache.refreshMut.Lock()
	defer cache.refreshMut.Unlock()

	if !cache.needsRefresh() {
		return nil
	}

	if !cache.startRefresh() {
		return nil
	}

	defer cache.endRefresh()

	cache.logger.Debug("refreshing")

	workers, err := getWorkers(cache.conn, workersQuery)
	if err != nil {
		return err
	}

	rows, err := psql.Select("worker_name, COUNT(*)").
		From("containers").
		Where("build_id IS NOT NULL").
		GroupBy("worker_name").
		RunWith(cache.conn).
		Query()
	if err != nil {
		return err
	}
	defer Close(rows)

	newWorkers := make(map[string]Worker, len(workers))
	for _, worker := range workers {
		newWorkers[worker.Name()] = worker
	}

	newCountByWorker := make(map[string]int, len(workers))
	for rows.Next() {
		var workerName string
		var containersCount int

		err = rows.Scan(&workerName, &containersCount)
		if err != nil {
			return err
		}

		newCountByWorker[workerName] = containersCount
	}

	cache.dataMut.Lock()
	cache.workers = newWorkers
	cache.workerContainerCounts = newCountByWorker
	cache.dataMut.Unlock()

	cache.lastRefresh.Store(time.Now().UnixNano())

	return nil
}

func (cache *WorkerCache) needsRefresh() bool {
	lastRefreshTime := time.Unix(0, cache.lastRefresh.Load())
	return time.Since(lastRefreshTime) >= cache.refreshInterval
}

func (cache *WorkerCache) startRefresh() bool {
	return cache.refreshing.CompareAndSwap(false, true)
}

func (cache *WorkerCache) endRefresh() {
	cache.refreshing.Store(false)
}

func (cache *WorkerCache) ensureRefresh() {
	cache.lastRefresh.Store(0)
}
