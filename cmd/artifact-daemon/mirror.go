package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/artifactwire"
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
	key            string       // artifact key (e.g., "handle/output")
	root           *os.Root     // storage root the tar walk goes through
	loc            RelKey       // location of the directory to mirror
	peers          []string     // peer hosts (no port)
	port           int          // daemon port on each peer
	scheme         string       // "http" or "https"
	client         *http.Client // pre-built client (TLS-aware where needed)
	logger         lager.Logger
	perPeerTimeout time.Duration // per-peer request timeout
	guard          *ReadGuard    // holds the handle's shared lock during tar walks (nil-safe)
}

// keyHandle returns the step handle segment of an artifact key
// ("handle/output" → "handle").
func keyHandle(key string) string {
	if i := strings.IndexByte(key, '/'); i > 0 {
		return key[:i]
	}
	return key
}

// Run mirrors the source directory to every peer concurrently. Returns a
// per-peer outcome list; never returns an error.
//
// Each peer gets its own tar walk streamed through an io.Pipe — the tar is
// never staged in memory. Buffering it (concurrency × artifact size of heap)
// is what drove daemon RSS past 4GB; disk reads are page-cache-cheap, so one
// walk per peer costs far less than one artifact-sized buffer per job.
func (j *mirrorJob) Run(ctx context.Context) []mirrorPeerOutcome {
	if len(j.peers) == 0 {
		return nil
	}

	out := make([]mirrorPeerOutcome, len(j.peers))
	var wg sync.WaitGroup
	for i, peer := range j.peers {
		wg.Add(1)
		go func(idx int, peerHost string) {
			defer wg.Done()
			out[idx] = j.putToPeer(ctx, peerHost)
		}(i, peer)
	}
	wg.Wait()
	return out
}

func (j *mirrorJob) putToPeer(ctx context.Context, peer string) mirrorPeerOutcome {
	timeout := j.perPeerTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Stream the tar through a pipe. A tar error surfaces through
	// CloseWithError as the request body error; on an early peer response
	// the transport closes the body, unblocking the writer goroutine.
	//
	// The walk holds the handle's shared lock: a tree walk silently omits
	// files deleted before it enumerates them, so tarring a tree that a
	// concurrent stream-in replace or sweep is deleting would mirror a
	// truncated copy as if complete.
	pr, pw := io.Pipe()
	go func() {
		release := j.guard.BeginRead(keyHandle(j.key))
		defer release()
		pw.CloseWithError(tarTree(pw, j.root, j.loc))
	}()

	url := peerURL(j.scheme, peer, j.port, artifactwire.StreamInPrefix, j.key)
	req, err := http.NewRequestWithContext(pctx, http.MethodPut, url, pr)
	if err != nil {
		pr.Close()
		return mirrorPeerOutcome{Peer: peer, Status: "unreachable", Err: err}
	}
	req.Header.Set("Content-Type", "application/x-tar")
	// Mark this write as mirror-originated so the receiving daemon does not
	// re-trigger its own mirror of it (feedback loop).
	req.Header.Set(MirrorOriginHeader, "true")

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

// ---------------------------------------------------------------------------
// Mirror: top-level mirror manager wiring everything together.
// ---------------------------------------------------------------------------

// Mirror is the artifact-daemon's outbound mirror manager. handleStreamIn
// (and handleMirrorTrigger from ATC) call Trigger after local data is
// settled; the worker pool fans out to peers in the background.
type Mirror struct {
	storagePath    string
	root           *os.Root
	port           int
	scheme         string
	replicas       int
	perPeerTimeout time.Duration

	pool   *WorkerPool
	peers  *PeerResolver
	client *http.Client
	logger lager.Logger
	guard  *ReadGuard
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
	Guard          *ReadGuard // coordinates tar walks with deleters (nil-safe)
	Root           *os.Root   // storage root; tar walks go through it
}

// NewMirror constructs a Mirror with its worker pool started. Caller must
// call Stop on shutdown.
// Root is required: the tar walk goes through it, and a nil one would surface
// as a panic inside a detached goroutine at the first mirror.
func NewMirror(cfg MirrorConfig) *Mirror {
	if cfg.Root == nil {
		panic("NewMirror: Root is required")
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Mirror{
		storagePath:    cfg.StoragePath,
		root:           cfg.Root,
		port:           cfg.Port,
		scheme:         cfg.Scheme,
		replicas:       cfg.Replicas,
		perPeerTimeout: cfg.PerPeerTimeout,
		pool:           NewWorkerPool(concurrency),
		peers:          cfg.Peers,
		client:         cfg.Client,
		logger:         cfg.Logger,
		guard:          cfg.Guard,
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
	// Routed through the same guard as every other join. This got the lexical
	// key check at the handler but no symlink resolution, so a link planted
	// under steps/ let POST /mirror tar a tree from outside the root and PUT it
	// to every peer — vector 6, re-opened by fixing only the sites the
	// reproduction used.
	stepsRoot := filepath.Join(m.storagePath, "steps")
	sourceDir, err := locateArtifact(stepsRoot, key)
	if err != nil {
		m.logger.Info("mirror-key-refused", lager.Data{"key": key, "reason": err.Error()})
		return
	}
	if _, err := os.Stat(sourceDir); err != nil {
		m.logger.Error("mirror-source-missing", err, lager.Data{
			"key": key,
			"src": sourceDir,
		})
		return
	}

	if m.peers == nil {
		m.logger.Debug("mirror-no-peer-resolver", lager.Data{"key": key})
		return
	}
	peerIPs, err := m.peers.peerIPs(ctx)
	if err != nil {
		m.logger.Error("mirror-peer-discovery-failed", err, lager.Data{"key": key})
		return
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
		root:           m.root,
		loc:            RelKey(path.Join("steps", key)),
		peers:          chosen,
		port:           m.port,
		scheme:         m.scheme,
		client:         m.client,
		logger:         m.logger.Session("mirror-job", lager.Data{"key": key}),
		perPeerTimeout: m.perPeerTimeout,
		guard:          m.guard,
	}
	outcomes := job.Run(ctx)

	m.logger.Info("mirror-complete", lager.Data{"key": key, "outcomes": outcomes})
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
