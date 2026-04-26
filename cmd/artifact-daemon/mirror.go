package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// ---------------------------------------------------------------------------
// WorkerPool: bounded goroutine pool with clean drain semantics.
// ---------------------------------------------------------------------------

// WorkerPool runs at most `concurrency` jobs in parallel. Submit enqueues
// a job; Stop+Wait drain in-flight work and queued work; Submit after Stop
// returns false.
type WorkerPool struct {
	work     chan func()
	done     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewWorkerPool starts `concurrency` worker goroutines. Concurrency<=0 is
// clamped to 1 to avoid an unusable zero-worker pool.
func NewWorkerPool(concurrency int) *WorkerPool {
	if concurrency <= 0 {
		concurrency = 1
	}
	p := &WorkerPool{
		work: make(chan func(), 64),
		done: make(chan struct{}),
	}
	for i := 0; i < concurrency; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *WorkerPool) worker() {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.work:
			if job == nil {
				return
			}
			job()
		case <-p.done:
			// Drain queued work then exit.
			for {
				select {
				case job := <-p.work:
					if job == nil {
						return
					}
					job()
				default:
					return
				}
			}
		}
	}
}

// Submit enqueues a job. Returns false if the pool is stopped.
func (p *WorkerPool) Submit(job func()) bool {
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.work <- job:
		return true
	case <-p.done:
		return false
	}
}

// Stop signals workers to drain and exit. Idempotent.
func (p *WorkerPool) Stop() {
	p.stopOnce.Do(func() {
		close(p.done)
	})
}

// Wait blocks until all workers have exited. Call after Stop.
func (p *WorkerPool) Wait() {
	p.wg.Wait()
}

// ---------------------------------------------------------------------------
// peerSelector: deterministic subset selection via consistent hashing.
// ---------------------------------------------------------------------------

// peerSelector picks a deterministic subset of peers for a given key using
// fnv64 consistent hashing. Same key + same peer set → same subset (until
// peer membership changes); subsequent reads can probe peers in the same
// hash order without coordination.
type peerSelector struct{}

// Select returns up to (replicas-1) peers for the given key.
//
//	replicas == 0 → nil (mirror disabled)
//	replicas <  0 → all peers
//	replicas >  0 → up to (replicas-1) peers; if fewer peers exist than
//	                requested, returns all available peers (no error)
func (peerSelector) Select(key string, peers []string, replicas int) []string {
	if replicas == 0 || len(peers) == 0 {
		return nil
	}

	sorted := make([]string, len(peers))
	copy(sorted, peers)
	sort.Strings(sorted)

	if replicas < 0 {
		return sorted
	}

	want := replicas - 1
	if want <= 0 {
		return nil
	}
	if want >= len(sorted) {
		return sorted
	}

	h := fnv.New64a()
	h.Write([]byte(key))
	start := int(h.Sum64() % uint64(len(sorted)))

	out := make([]string, 0, want)
	for i := 0; i < want; i++ {
		out = append(out, sorted[(start+i)%len(sorted)])
	}
	return out
}

// ---------------------------------------------------------------------------
// mirrorJob: best-effort tar+PUT to N peers.
// ---------------------------------------------------------------------------

// mirrorPeerOutcome records the result of mirroring to one peer.
type mirrorPeerOutcome struct {
	Peer   string
	Status string // "ok" | "rejected" | "unreachable"
	Err    error
}

// mirrorJob mirrors a single source directory to a fixed list of peers via
// HTTP PUT to each peer's /stream-in/{key} endpoint. Best-effort: never
// returns a top-level error; per-peer outcomes are reported via the return
// value.
type mirrorJob struct {
	key            string        // artifact key (e.g., "handle/output")
	sourceDir      string        // absolute path to the directory to mirror
	peers          []string      // peer hosts (no port)
	port           int           // daemon port on each peer
	scheme         string        // "http" or "https"
	client         *http.Client  // pre-built client (TLS-aware where needed)
	logger         lager.Logger
	perPeerTimeout time.Duration // per-peer request timeout
}

// Run mirrors the source directory to every peer concurrently. Returns a
// per-peer outcome list; never returns an error.
func (j *mirrorJob) Run(ctx context.Context) []mirrorPeerOutcome {
	if len(j.peers) == 0 {
		return nil
	}

	// Tar the source once into a buffer so we can fan out cheaply. Step
	// outputs are typically small enough that holding the tar in memory is
	// fine; for very large artifacts the consumer's stream-in path is the
	// bottleneck, not our buffer.
	var tarBuf bytes.Buffer
	if err := tarDir(&tarBuf, j.sourceDir); err != nil {
		j.logger.Error("tar-source-dir-failed", err, lager.Data{
			"src": j.sourceDir,
		})
		out := make([]mirrorPeerOutcome, 0, len(j.peers))
		for _, p := range j.peers {
			out = append(out, mirrorPeerOutcome{Peer: p, Status: "unreachable", Err: err})
		}
		return out
	}
	body := tarBuf.Bytes()

	out := make([]mirrorPeerOutcome, len(j.peers))
	var wg sync.WaitGroup
	for i, peer := range j.peers {
		wg.Add(1)
		go func(idx int, peerHost string) {
			defer wg.Done()
			out[idx] = j.putToPeer(ctx, peerHost, body)
		}(i, peer)
	}
	wg.Wait()
	return out
}

func (j *mirrorJob) putToPeer(ctx context.Context, peer string, body []byte) mirrorPeerOutcome {
	timeout := j.perPeerTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("%s://%s:%d/stream-in/%s", j.scheme, peer, j.port, j.key)
	req, err := http.NewRequestWithContext(pctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return mirrorPeerOutcome{Peer: peer, Status: "unreachable", Err: err}
	}
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := j.client.Do(req)
	if err != nil {
		j.logger.Debug("peer-unreachable", lager.Data{"peer": peer, "error": err.Error()})
		return mirrorPeerOutcome{Peer: peer, Status: "unreachable", Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		j.logger.Debug("peer-ok", lager.Data{"peer": peer})
		return mirrorPeerOutcome{Peer: peer, Status: "ok"}
	}
	j.logger.Info("peer-rejected", lager.Data{"peer": peer, "status": resp.StatusCode})
	return mirrorPeerOutcome{Peer: peer, Status: "rejected", Err: fmt.Errorf("HTTP %d", resp.StatusCode)}
}

// tarDir streams a tar archive of src into w. Directories, regular files,
// and symlinks are preserved; everything else is skipped.
func tarDir(w io.Writer, src string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		hdr := &tar.Header{
			Name:    rel,
			Mode:    int64(info.Mode().Perm()),
			ModTime: info.ModTime(),
		}
		switch {
		case info.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
			return tw.WriteHeader(hdr)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
			hdr.Size = 0
			return tw.WriteHeader(hdr)
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
	})
}

// ---------------------------------------------------------------------------
// Mirror: top-level mirror manager wiring everything together.
// ---------------------------------------------------------------------------

// Mirror is the artifact-daemon's outbound mirror manager. handleStreamIn
// (and handleMirrorTrigger from ATC) call Trigger after local data is
// settled; the worker pool fans out to peers in the background.
type Mirror struct {
	storagePath    string
	port           int
	scheme         string
	replicas       int
	perPeerTimeout time.Duration

	pool   *WorkerPool
	peers  *PeerResolver
	client *http.Client
	logger lager.Logger

	mu     sync.RWMutex
	status map[string]map[string]string // key → peer → status (for Phase 3 evacuation)

	// evacuationPeers is an optional explicit peer list used by both
	// Trigger's run loop and Evacuate's synchronous path. Tests set this
	// to bypass PeerResolver + EndpointSlice (which would need a real
	// K8s fake to wire). When empty (production default), the code
	// falls back to peers.peerIPs.
	evacuationPeers []string
}

// MirrorConfig configures a Mirror manager.
type MirrorConfig struct {
	StoragePath    string
	Port           int
	Scheme         string
	Replicas       int           // 0 = disabled, -1 = all peers, N = local + (N-1) peers
	Concurrency    int           // worker pool size
	PerPeerTimeout time.Duration // per-peer PUT timeout
	Peers          *PeerResolver
	Client         *http.Client
	Logger         lager.Logger
}

// NewMirror constructs a Mirror with its worker pool started. Caller must
// call Stop on shutdown.
func NewMirror(cfg MirrorConfig) *Mirror {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Mirror{
		storagePath:    cfg.StoragePath,
		port:           cfg.Port,
		scheme:         cfg.Scheme,
		replicas:       cfg.Replicas,
		perPeerTimeout: cfg.PerPeerTimeout,
		pool:           NewWorkerPool(concurrency),
		peers:          cfg.Peers,
		client:         cfg.Client,
		logger:         cfg.Logger,
		status:         make(map[string]map[string]string),
	}
}

// Trigger schedules a mirror job for the given key. Returns immediately;
// the actual PUTs run on the worker pool. Best-effort — errors are logged
// inside the job, never returned. Safe to call on a nil receiver (no-op)
// so the daemon can run with mirror disabled.
func (m *Mirror) Trigger(ctx context.Context, key string) {
	if m == nil || m.replicas == 0 {
		return
	}
	if !m.pool.Submit(func() {
		m.run(context.Background(), key)
	}) {
		m.logger.Debug("mirror-pool-stopped", lager.Data{"key": key})
	}
}

func (m *Mirror) run(ctx context.Context, key string) {
	sourceDir := filepath.Join(m.storagePath, "steps", key)
	if _, err := os.Stat(sourceDir); err != nil {
		m.logger.Error("mirror-source-missing", err, lager.Data{
			"key": key,
			"src": sourceDir,
		})
		return
	}

	peerIPs := m.evacuationPeers
	if len(peerIPs) == 0 {
		if m.peers == nil {
			m.logger.Debug("mirror-no-peer-resolver", lager.Data{"key": key})
			return
		}
		ips, err := m.peers.peerIPs(ctx)
		if err != nil {
			m.logger.Error("mirror-peer-discovery-failed", err, lager.Data{"key": key})
			return
		}
		peerIPs = ips
	}

	chosen := peerSelector{}.Select(key, peerIPs, m.replicas)
	if len(chosen) == 0 {
		m.logger.Debug("mirror-no-peers-selected", lager.Data{
			"key":         key,
			"peers_total": len(peerIPs),
			"replicas":    m.replicas,
		})
		return
	}

	job := &mirrorJob{
		key:            key,
		sourceDir:      sourceDir,
		peers:          chosen,
		port:           m.port,
		scheme:         m.scheme,
		client:         m.client,
		logger:         m.logger.Session("mirror-job", lager.Data{"key": key}),
		perPeerTimeout: m.perPeerTimeout,
	}
	outcomes := job.Run(ctx)

	m.recordStatus(key, outcomes)
	m.logger.Info("mirror-complete", lager.Data{"key": key, "outcomes": outcomes})
}

func (m *Mirror) recordStatus(key string, outcomes []mirrorPeerOutcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status[key] == nil {
		m.status[key] = make(map[string]string)
	}
	for _, o := range outcomes {
		m.status[key][o.Peer] = o.Status
	}
}

// Stop drains in-flight mirror jobs and prevents new ones. Safe to call on
// a nil receiver.
func (m *Mirror) Stop() {
	if m == nil {
		return
	}
	m.pool.Stop()
	m.pool.Wait()
}

// Evacuate is the synchronous evacuation path fired from the preemption
// watcher's callback. It:
//
//  1. Stops the worker pool (Trigger calls hereafter return false).
//  2. Walks {storagePath}/steps/ looking for step output directories
//     ({handle}/{output}) that have not been confirmed mirrored to at
//     least one peer (per the in-memory mirrorStatus map).
//  3. Synchronously runs a mirror job for each such directory, capping
//     the total runtime at `budget`.
//
// On budget exhaustion, returns whatever artifacts have been pushed.
// The remaining unmirrored data falls back to today's behavior — a
// build that needs it after node loss must rerun. This is the
// acceptable cost per the track's failure-mode budget.
func (m *Mirror) Evacuate(ctx context.Context, budget time.Duration) {
	if m == nil {
		return
	}

	logger := m.logger.Session("evacuate", lager.Data{"budget": budget.String()})
	logger.Info("starting")

	// Drain in-flight jobs and disable Trigger.
	m.pool.Stop()
	m.pool.Wait()

	deadline := time.Now().Add(budget)
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	keys := m.findUnmirroredKeys()
	logger.Info("unmirrored-keys", lager.Data{"count": len(keys)})

	for _, key := range keys {
		if pctx.Err() != nil {
			logger.Info("budget-exhausted", lager.Data{"remaining": len(keys)})
			return
		}
		m.evacuateOne(pctx, key)
	}

	logger.Info("complete")
}

// evacuateOne is the synchronous version of run — it runs the mirror job
// directly on the calling goroutine instead of via the (already-stopped)
// worker pool.
func (m *Mirror) evacuateOne(ctx context.Context, key string) {
	sourceDir := filepath.Join(m.storagePath, "steps", key)
	if _, err := os.Stat(sourceDir); err != nil {
		m.logger.Debug("evacuate-source-missing", lager.Data{"key": key})
		return
	}

	peerIPs := m.evacuationPeers
	if len(peerIPs) == 0 && m.peers != nil {
		ips, err := m.peers.peerIPs(ctx)
		if err != nil {
			m.logger.Error("evacuate-peer-discovery-failed", err, lager.Data{"key": key})
			return
		}
		peerIPs = ips
	}

	chosen := peerSelector{}.Select(key, peerIPs, m.replicas)
	if len(chosen) == 0 {
		m.logger.Debug("evacuate-no-peers-selected", lager.Data{
			"key":         key,
			"peers_total": len(peerIPs),
		})
		return
	}

	job := &mirrorJob{
		key:            key,
		sourceDir:      sourceDir,
		peers:          chosen,
		port:           m.port,
		scheme:         m.scheme,
		client:         m.client,
		logger:         m.logger.Session("evacuate-job", lager.Data{"key": key}),
		perPeerTimeout: m.perPeerTimeout,
	}
	outcomes := job.Run(ctx)
	m.recordStatus(key, outcomes)
}

// findUnmirroredKeys walks {storagePath}/steps/ and returns the
// {handle}/{output} keys whose status map doesn't contain at least one
// "ok" outcome. Used by Evacuate to prioritize what to flush.
func (m *Mirror) findUnmirroredKeys() []string {
	stepsRoot := filepath.Join(m.storagePath, "steps")
	var keys []string

	entries, err := os.ReadDir(stepsRoot)
	if err != nil {
		// steps/ doesn't exist yet (clean daemon, no work done) — nothing
		// to evacuate.
		return nil
	}
	for _, handleEntry := range entries {
		if !handleEntry.IsDir() {
			continue
		}
		handle := handleEntry.Name()
		outputs, err := os.ReadDir(filepath.Join(stepsRoot, handle))
		if err != nil {
			continue
		}
		for _, outputEntry := range outputs {
			if !outputEntry.IsDir() {
				continue
			}
			key := handle + "/" + outputEntry.Name()
			if !m.isAtLeastOnePeerOK(key) {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// isAtLeastOnePeerOK reports whether the in-memory status map records
// at least one peer outcome=ok for the given key. RF=2 considers this
// "mirrored" (data survives single-node loss).
func (m *Mirror) isAtLeastOnePeerOK(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, status := range m.status[key] {
		if status == "ok" {
			return true
		}
	}
	return false
}
