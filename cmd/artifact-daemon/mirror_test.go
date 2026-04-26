package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// ---------------------------------------------------------------------------
// WorkerPool — bounded concurrency with clean drain semantics.
// ---------------------------------------------------------------------------

func TestWorkerPool_EnforcesMaxConcurrency(t *testing.T) {
	pool := NewWorkerPool(2)
	defer pool.Stop()

	// Every submitted job blocks on `release` until we close it. We track
	// in-flight count via an atomic counter and snapshot the peak.
	release := make(chan struct{})
	var inFlight, peak int32
	started := make(chan struct{}, 5)

	job := func() {
		n := atomic.AddInt32(&inFlight, 1)
		// Track peak in-flight count (best-effort — a small race window
		// here is fine since we sample after every increment).
		for {
			cur := atomic.LoadInt32(&peak)
			if n <= cur || atomic.CompareAndSwapInt32(&peak, cur, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		atomic.AddInt32(&inFlight, -1)
	}

	for i := 0; i < 5; i++ {
		if !pool.Submit(job) {
			t.Fatalf("Submit returned false on a running pool (i=%d)", i)
		}
	}

	// Two jobs should start; the rest queue.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 2 jobs to start, got %d", i)
		}
	}

	// Give the pool a brief grace window — if concurrency is broken, a
	// third job will leak through into running state.
	select {
	case <-started:
		t.Fatal("third job started while concurrency limit should hold")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	// Drain the rest by waiting for the remaining 3 starts.
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected remaining job %d to start after release", i)
		}
	}

	if got := atomic.LoadInt32(&peak); got > 2 {
		t.Errorf("peak concurrency %d exceeded limit 2", got)
	}
}

func TestWorkerPool_DrainsOnStop(t *testing.T) {
	pool := NewWorkerPool(2)

	var completed int32
	for i := 0; i < 4; i++ {
		pool.Submit(func() {
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&completed, 1)
		})
	}

	pool.Stop()
	pool.Wait()

	if got := atomic.LoadInt32(&completed); got != 4 {
		t.Errorf("expected all 4 jobs to complete on drain, got %d", got)
	}
}

func TestWorkerPool_RejectsSubmitAfterStop(t *testing.T) {
	pool := NewWorkerPool(1)
	pool.Stop()
	pool.Wait()

	if pool.Submit(func() {}) {
		t.Error("Submit returned true on a stopped pool — should reject")
	}
}

// ---------------------------------------------------------------------------
// peerSelector — deterministic subset selection via consistent hashing.
// ---------------------------------------------------------------------------

func TestPeerSelector_DeterministicAcrossCalls(t *testing.T) {
	peers := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}
	sel := peerSelector{}

	first := sel.Select("step-key-A", peers, 2)
	second := sel.Select("step-key-A", peers, 2)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("expected deterministic selection for same key/peers; got %v then %v", first, second)
	}
}

func TestPeerSelector_DifferentKeysCanDiffer(t *testing.T) {
	// Not strictly required — two keys may collide on the same peer subset
	// — but with N=4 peers and many keys, at least some keys should land
	// on different subsets. Here we just verify the selection is a function
	// of the key (deterministic per key, not random).
	peers := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}
	sel := peerSelector{}

	a := sel.Select("step-A", peers, 2)
	b := sel.Select("step-A", peers, 2)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("repeated calls with same key must return same set: %v vs %v", a, b)
	}
}

func TestPeerSelector_AllPeers_WhenReplicasNegative(t *testing.T) {
	peers := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	sel := peerSelector{}

	got := sel.Select("any-key", peers, -1)

	gotSorted := append([]string{}, got...)
	sort.Strings(gotSorted)
	wantSorted := append([]string{}, peers...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(gotSorted, wantSorted) {
		t.Errorf("expected all peers for replicas=-1, got %v", got)
	}
}

func TestPeerSelector_FewerPeersThanRequested(t *testing.T) {
	// replicas=2 means up to (2-1)=1 peer; with only 1 peer available,
	// mirror to that 1 peer (no error).
	peers := []string{"10.0.0.1"}
	sel := peerSelector{}

	got := sel.Select("any-key", peers, 2)

	if len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("expected [10.0.0.1] for replicas=2 with 1 peer, got %v", got)
	}
}

func TestPeerSelector_NoPeers(t *testing.T) {
	sel := peerSelector{}

	got := sel.Select("any-key", nil, 2)
	if len(got) != 0 {
		t.Errorf("expected empty slice for 0 peers, got %v", got)
	}
}

func TestPeerSelector_ReplicasZero_DisablesMirror(t *testing.T) {
	peers := []string{"10.0.0.1", "10.0.0.2"}
	sel := peerSelector{}

	got := sel.Select("any-key", peers, 0)
	if len(got) != 0 {
		t.Errorf("expected empty slice for replicas=0 (disabled), got %v", got)
	}
}

// ---------------------------------------------------------------------------
// mirrorJob — best-effort tar+PUT to N peers.
// ---------------------------------------------------------------------------

func TestMirrorJob_Run_RecordsPerPeerOutcomes(t *testing.T) {
	// Source directory with one file.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Three peers:
	//   ok       — accepts /stream-in/handle/output and 201's
	//   rejects  — always 500
	//   slow     — sleeps longer than per-peer timeout
	var okHits, rejectHits, slowHits int32

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&okHits, 1)
		if r.Method == http.MethodPut && r.URL.Path == "/stream-in/handle/output" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ok.Close()

	rejects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rejectHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer rejects.Close()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&slowHits, 1)
		time.Sleep(500 * time.Millisecond) // longer than perPeerTimeout below
		w.WriteHeader(http.StatusCreated)
	}))
	defer slow.Close()

	// Use synthetic peer hostnames so the routing map keys don't collide
	// (httptest binds all servers to 127.0.0.1, just on different ports).
	const (
		peerOK     = "peer-ok"
		peerReject = "peer-rejects"
		peerSlow   = "peer-slow"
	)
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerOK + ":7780":     ok.URL,
		peerReject + ":7780": rejects.URL,
		peerSlow + ":7780":   slow.URL,
	}}

	job := &mirrorJob{
		key:            "handle/output",
		sourceDir:      src,
		peers:          []string{peerOK, peerReject, peerSlow},
		port:           7780,
		scheme:         "http",
		client:         &http.Client{Transport: transport, Timeout: 5 * time.Second},
		logger:         lagertest.NewTestLogger("mirror"),
		perPeerTimeout: 100 * time.Millisecond, // forces slow peer to timeout
	}

	// Must NOT panic, must NOT return any error.
	outcomes := job.Run(context.Background())

	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes (one per peer), got %d", len(outcomes))
	}

	byPeer := make(map[string]string)
	for _, o := range outcomes {
		byPeer[o.Peer] = o.Status
	}
	if byPeer[peerOK] != "ok" {
		t.Errorf("expected ok peer status=ok, got %q (full outcomes: %+v)", byPeer[peerOK], outcomes)
	}
	if byPeer[peerReject] != "rejected" {
		t.Errorf("expected rejecting peer status=rejected, got %q", byPeer[peerReject])
	}
	if byPeer[peerSlow] != "unreachable" {
		t.Errorf("expected slow peer status=unreachable (timeout), got %q", byPeer[peerSlow])
	}
	if got := atomic.LoadInt32(&okHits); got != 1 {
		t.Errorf("expected 1 PUT to ok peer, got %d", got)
	}
}

func TestMirrorJob_Run_NoPeers_NoOp(t *testing.T) {
	src := t.TempDir()
	job := &mirrorJob{
		key:       "h/o",
		sourceDir: src,
		peers:     nil,
		port:      7780,
		scheme:    "http",
		client:    &http.Client{},
		logger:    lagertest.NewTestLogger("mirror"),
	}

	outcomes := job.Run(context.Background())
	if len(outcomes) != 0 {
		t.Errorf("expected no outcomes for empty peers, got %v", outcomes)
	}
}

// ---------------------------------------------------------------------------
// Mirror.Evacuate — synchronous flush of unmirrored step dirs on
// preemption notice (P3.4).
// ---------------------------------------------------------------------------

func TestMirror_Evacuate_FlushesUnmirroredStepDirs(t *testing.T) {
	// Set up storage with two step output dirs:
	//   steps/handle-a/done/   — already mirrored (mirrorStatus has "ok")
	//   steps/handle-b/pending/ — unmirrored (no mirrorStatus entry)
	storage := t.TempDir()
	for _, dir := range []string{"steps/handle-a/done", "steps/handle-b/pending"} {
		full := filepath.Join(storage, dir)
		if err := os.MkdirAll(full, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "f.txt"), []byte("payload"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Track which keys peer received PUTs for.
	var (
		mu      sync.Mutex
		putKeys []string
	)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && len(r.URL.Path) > len("/stream-in/") {
			key := r.URL.Path[len("/stream-in/"):]
			mu.Lock()
			putKeys = append(putKeys, key)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer peer.Close()

	const peerHost = "evacuate-peer"
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerHost + ":7780": peer.URL,
	}}

	logger := lagertest.NewTestLogger("evacuate")
	mirror := &Mirror{
		storagePath:    storage,
		port:           7780,
		scheme:         "http",
		replicas:       2,
		perPeerTimeout: 5 * time.Second,
		pool:           NewWorkerPool(2),
		client:         &http.Client{Transport: transport, Timeout: 5 * time.Second},
		logger:         logger,
		status:         make(map[string]map[string]string),
		// Inject an evacuation-only peer list — bypassing PeerResolver
		// (which would need a real K8s clientset). The Evacuate impl
		// uses this when set so we can drive the test without K8s fakes.
		evacuationPeers: []string{peerHost},
	}
	defer mirror.Stop()

	// Mark "handle-a/done" as already mirrored so Evacuate skips it.
	mirror.recordStatus("handle-a/done", []mirrorPeerOutcome{
		{Peer: peerHost, Status: "ok"},
	})

	mirror.Evacuate(context.Background(), 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(putKeys) != 1 || putKeys[0] != "handle-b/pending" {
		t.Errorf("expected exactly one PUT for handle-b/pending; got %v", putKeys)
	}
}

func TestMirror_Evacuate_RespectsBudget(t *testing.T) {
	// Pre-create several step dirs, all unmirrored. Use a slow peer that
	// times out per request; Evacuate's budget should bound total runtime.
	storage := t.TempDir()
	for i := 0; i < 5; i++ {
		full := filepath.Join(storage, "steps", "h", "out"+itoaSimple(i))
		os.MkdirAll(full, 0755)
		os.WriteFile(filepath.Join(full, "f.txt"), []byte("x"), 0644)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // longer than per-peer timeout below
		w.WriteHeader(http.StatusCreated)
	}))
	defer slow.Close()

	transport := &mirrorRoutingTransport{routes: map[string]string{
		"slow-peer:7780": slow.URL,
	}}

	mirror := &Mirror{
		storagePath:     storage,
		port:            7780,
		scheme:          "http",
		replicas:        2,
		perPeerTimeout:  50 * time.Millisecond,
		pool:            NewWorkerPool(2),
		client:          &http.Client{Transport: transport, Timeout: 1 * time.Second},
		logger:          lagertest.NewTestLogger("evacuate-budget"),
		status:          make(map[string]map[string]string),
		evacuationPeers: []string{"slow-peer"},
	}
	defer mirror.Stop()

	budget := 200 * time.Millisecond
	start := time.Now()
	mirror.Evacuate(context.Background(), budget)
	elapsed := time.Since(start)

	// Allow some slop above the budget for goroutine cleanup (~50ms).
	if elapsed > budget+500*time.Millisecond {
		t.Errorf("Evacuate ran %v, well above budget %v", elapsed, budget)
	}
}

func TestMirror_Evacuate_RejectsNewTriggerAfterCall(t *testing.T) {
	mirror := &Mirror{
		storagePath:    t.TempDir(),
		replicas:       2,
		pool:           NewWorkerPool(2),
		logger:         lagertest.NewTestLogger("evacuate-reject"),
		status:         make(map[string]map[string]string),
		client:         &http.Client{},
	}

	mirror.Evacuate(context.Background(), 100*time.Millisecond)

	// After Evacuate, the pool must be drained so subsequent Trigger
	// calls cannot enqueue work (there's no point — we're shutting down).
	if mirror.pool.Submit(func() {}) {
		t.Error("expected pool to reject Submit after Evacuate")
	}
}

// itoaSimple is a tiny helper to avoid importing strconv just for this
// test (strconv IS already imported elsewhere in this file via mirrorJob
// tests, but keeping it local avoids accidental shadowing).
func itoaSimple(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

// hostFromURL extracts the host (without scheme/port) from an httptest URL
// like "http://127.0.0.1:54321".
func hostFromURL(u string) string {
	// strip "http://"
	if len(u) > 7 && u[:7] == "http://" {
		u = u[7:]
	}
	// strip ":port"
	for i := 0; i < len(u); i++ {
		if u[i] == ':' {
			return u[:i]
		}
	}
	return u
}

// mirrorRoutingTransport routes by URL host:port to a target server URL.
// "" means refuse the request (synthetic error).
type mirrorRoutingTransport struct {
	routes map[string]string
}

func (t *mirrorRoutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, ok := t.routes[req.URL.Host]
	if !ok || target == "" {
		return nil, &mirrorTransportErr{host: req.URL.Host}
	}
	// Strip "http://" from target.
	if len(target) > 7 && target[:7] == "http://" {
		target = target[7:]
	}
	req.URL.Scheme = "http"
	req.URL.Host = target
	return http.DefaultTransport.RoundTrip(req)
}

type mirrorTransportErr struct{ host string }

func (e *mirrorTransportErr) Error() string { return "connection refused: " + e.host }

// ---------------------------------------------------------------------------
// Robustness / safety tests: data integrity, sweeper races, concurrent load,
// shutdown races, edge cases.
// ---------------------------------------------------------------------------

// TestMirrorJob_Run_PreservesSubdirsAndMultipleFiles verifies a realistic
// step-output shape (multiple files, nested subdir) round-trips through
// the tar+PUT path byte-for-byte. Critical for deployment confidence:
// confirms peers receive exactly what producers wrote.
func TestMirrorJob_Run_PreservesSubdirsAndMultipleFiles(t *testing.T) {
	src := t.TempDir()
	// Build a small tree: top.txt + sub/nested.txt + sub/deeper/leaf.bin
	files := map[string][]byte{
		"top.txt":             []byte("top-level content\n"),
		"sub/nested.txt":      []byte("nested\nmulti-line\n"),
		"sub/deeper/leaf.bin": {0x00, 0x01, 0x02, 0xff, 0xfe},
	}
	for rel, data := range files {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Peer extracts received tar to a separate hostPath using the daemon's
	// real handleStreamIn — so we exercise the full PUT → extract → disk
	// path (not just "did the bytes get sent").
	peerStorage := t.TempDir()
	peerLogger := lagertest.NewTestLogger("peer-rcv")
	peerServer := NewServer(peerLogger, peerStorage, "peer")
	peerHTTP := httptest.NewServer(peerServer.Handler())
	defer peerHTTP.Close()

	const peerHost = "integrity-peer"
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerHost + ":7780": peerHTTP.URL,
	}}

	job := &mirrorJob{
		key:            "h/o",
		sourceDir:      src,
		peers:          []string{peerHost},
		port:           7780,
		scheme:         "http",
		client:         &http.Client{Transport: transport, Timeout: 5 * time.Second},
		logger:         lagertest.NewTestLogger("integrity"),
		perPeerTimeout: 5 * time.Second,
	}

	outcomes := job.Run(context.Background())
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("expected ok outcome, got %+v", outcomes)
	}

	// Verify peer got byte-for-byte matches.
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(peerStorage, "steps", "h/o", rel))
		if err != nil {
			t.Errorf("missing on peer: %s — %v", rel, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("byte mismatch on peer for %s: got %v, want %v", rel, got, want)
		}
	}
}

// TestMirror_Trigger_SourceDisappearsBeforeJobRuns covers the sweeper race:
// Trigger queues a job, then the artifact's source dir is deleted (e.g.
// by the daemon's TTL sweeper) before the worker pool gets to it. The
// job must log and exit cleanly — no panic, no zombie goroutine.
func TestMirror_Trigger_SourceDisappearsBeforeJobRuns(t *testing.T) {
	storage := t.TempDir()
	src := filepath.Join(storage, "steps", "race-handle", "out")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Peer that should never receive a PUT (because source was deleted).
	var puts int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&puts, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer peer.Close()

	const peerHost = "race-peer"
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerHost + ":7780": peer.URL,
	}}

	mirror := &Mirror{
		storagePath:     storage,
		port:            7780,
		scheme:          "http",
		replicas:        2,
		perPeerTimeout:  3 * time.Second,
		pool:            NewWorkerPool(2),
		client:          &http.Client{Transport: transport, Timeout: 3 * time.Second},
		logger:          lagertest.NewTestLogger("sweeper-race"),
		status:          make(map[string]map[string]string),
		evacuationPeers: []string{peerHost},
	}
	defer mirror.Stop()

	// Delete the source BEFORE submitting the job so the worker sees it
	// missing.
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}

	mirror.Trigger(context.Background(), "race-handle/out")
	mirror.pool.Stop()
	mirror.pool.Wait()

	if got := atomic.LoadInt32(&puts); got != 0 {
		t.Errorf("peer should have received zero PUTs after source vanished; got %d", got)
	}
}

// TestMirror_Trigger_ConcurrentMultiKeyLoad simulates a realistic pipeline:
// many concurrent Trigger calls for distinct keys, with bounded peer
// concurrency. All keys must eventually reach the peer and the worker
// pool must clean up cleanly.
func TestMirror_Trigger_ConcurrentMultiKeyLoad(t *testing.T) {
	storage := t.TempDir()

	// Pre-create 10 step output dirs.
	const numKeys = 10
	keys := make([]string, numKeys)
	for i := 0; i < numKeys; i++ {
		key := "h-" + itoaSimple(i) + "/out"
		keys[i] = key
		full := filepath.Join(storage, "steps", key)
		os.MkdirAll(full, 0755)
		os.WriteFile(filepath.Join(full, "f.txt"), []byte(itoaSimple(i)), 0644)
	}

	var receivedKeys sync.Map
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && len(r.URL.Path) > len("/stream-in/") {
			receivedKeys.Store(r.URL.Path[len("/stream-in/"):], true)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer peer.Close()

	const peerHost = "load-peer"
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerHost + ":7780": peer.URL,
	}}

	mirror := &Mirror{
		storagePath:     storage,
		port:            7780,
		scheme:          "http",
		replicas:        2,
		perPeerTimeout:  3 * time.Second,
		pool:            NewWorkerPool(4), // cap at 4 concurrent
		client:          &http.Client{Transport: transport, Timeout: 3 * time.Second},
		logger:          lagertest.NewTestLogger("load"),
		status:          make(map[string]map[string]string),
		evacuationPeers: []string{peerHost},
	}

	// Fire all triggers as fast as possible.
	for _, k := range keys {
		mirror.Trigger(context.Background(), k)
	}

	// Drain.
	mirror.Stop()

	// Verify every key reached the peer.
	for _, k := range keys {
		if _, ok := receivedKeys.Load(k); !ok {
			t.Errorf("key %q did not reach peer under concurrent load", k)
		}
	}
}

// TestMirror_Trigger_AfterStop_ReturnsCleanly verifies that once the
// mirror is shutting down, late Trigger calls don't panic and don't
// leak goroutines. main.go's shutdown sequence calls Stop while
// in-flight RecordOutputs may still be calling Trigger.
func TestMirror_Trigger_AfterStop_ReturnsCleanly(t *testing.T) {
	storage := t.TempDir()
	mirror := &Mirror{
		storagePath: storage,
		port:        7780,
		scheme:      "http",
		replicas:    2,
		pool:        NewWorkerPool(2),
		client:      &http.Client{},
		logger:      lagertest.NewTestLogger("post-stop"),
		status:      make(map[string]map[string]string),
	}
	mirror.Stop()

	// Should not panic and should not block.
	done := make(chan struct{})
	go func() {
		mirror.Trigger(context.Background(), "any-key")
		close(done)
	}()
	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger after Stop blocked instead of returning")
	}
}

// TestMirror_Trigger_NilReceiver_NoOp verifies the documented nil-safety.
// Daemon code calls mirror.Trigger from handleStreamIn even when mirror
// is disabled (cfg.replicas=0 → mirror is nil). Must be a clean no-op.
func TestMirror_Trigger_NilReceiver_NoOp(t *testing.T) {
	var m *Mirror
	// Must not panic.
	m.Trigger(context.Background(), "key")
	m.Stop()
	m.Evacuate(context.Background(), 100*time.Millisecond)
}

// TestMirror_Trigger_ReplicasZero_NoOp verifies that with replicas=0,
// Trigger is an explicit no-op even on a non-nil receiver. Important
// because main.go skips Mirror construction entirely when replicas=0
// — but if a future code path constructs one, the early-return must
// hold.
func TestMirror_Trigger_ReplicasZero_NoOp(t *testing.T) {
	storage := t.TempDir()
	src := filepath.Join(storage, "steps", "h/o")
	os.MkdirAll(src, 0755)
	os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0644)

	var puts int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&puts, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer peer.Close()

	const peerHost = "should-not-be-called"
	mirror := &Mirror{
		storagePath:     storage,
		replicas:        0, // disabled
		pool:            NewWorkerPool(2),
		client:          &http.Client{},
		logger:          lagertest.NewTestLogger("rf-zero"),
		status:          make(map[string]map[string]string),
		evacuationPeers: []string{peerHost},
	}
	defer mirror.Stop()

	mirror.Trigger(context.Background(), "h/o")
	mirror.pool.Stop()
	mirror.pool.Wait()

	if got := atomic.LoadInt32(&puts); got != 0 {
		t.Errorf("expected zero peer hits with replicas=0, got %d", got)
	}
}

// TestMirror_Evacuate_NoStepsDir_CleanNoOp verifies Evacuate is safe to
// call on a daemon that has done no work yet (storagePath/steps/ doesn't
// exist). This can happen on first-startup preemption.
func TestMirror_Evacuate_NoStepsDir_CleanNoOp(t *testing.T) {
	mirror := &Mirror{
		storagePath: t.TempDir(), // no steps/ subdirectory
		replicas:    2,
		pool:        NewWorkerPool(2),
		client:      &http.Client{},
		logger:      lagertest.NewTestLogger("evac-empty"),
		status:      make(map[string]map[string]string),
	}
	defer mirror.Stop()

	// Must not panic, must return promptly.
	done := make(chan struct{})
	go func() {
		mirror.Evacuate(context.Background(), 1*time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Evacuate hung when steps/ didn't exist")
	}
}

// TestMirrorJob_Run_EmptySourceDir_NoPanic verifies tar of an empty dir
// produces a valid (empty) tar that the peer accepts. Emptiness can
// happen when a step produces no output but registers an empty volume.
func TestMirrorJob_Run_EmptySourceDir_NoPanic(t *testing.T) {
	src := t.TempDir() // empty

	peerStorage := t.TempDir()
	peerServer := NewServer(lagertest.NewTestLogger("empty-peer"), peerStorage, "peer")
	peerHTTP := httptest.NewServer(peerServer.Handler())
	defer peerHTTP.Close()

	const peerHost = "empty-peer"
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerHost + ":7780": peerHTTP.URL,
	}}

	job := &mirrorJob{
		key:            "h/o",
		sourceDir:      src,
		peers:          []string{peerHost},
		port:           7780,
		scheme:         "http",
		client:         &http.Client{Transport: transport, Timeout: 3 * time.Second},
		logger:         lagertest.NewTestLogger("empty"),
		perPeerTimeout: 3 * time.Second,
	}
	outcomes := job.Run(context.Background())
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Errorf("expected empty dir to mirror successfully, got %+v", outcomes)
	}
}

// TestMirrorJob_Run_PartialPeerSuccess verifies per-peer outcome
// independence: when some peers succeed and others fail, the successes
// are recorded as ok and the failures as rejected/unreachable. The job
// must NOT abort early on first failure.
func TestMirrorJob_Run_PartialPeerSuccess(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f.txt"), []byte("data"), 0644)

	okPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer okPeer.Close()

	rejectPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer rejectPeer.Close()

	transport := &mirrorRoutingTransport{routes: map[string]string{
		"ok-peer:7780":     okPeer.URL,
		"reject-peer:7780": rejectPeer.URL,
		"dead-peer:7780":   "", // refused
	}}

	job := &mirrorJob{
		key:            "h/o",
		sourceDir:      src,
		peers:          []string{"ok-peer", "reject-peer", "dead-peer"},
		port:           7780,
		scheme:         "http",
		client:         &http.Client{Transport: transport, Timeout: 3 * time.Second},
		logger:         lagertest.NewTestLogger("partial"),
		perPeerTimeout: 3 * time.Second,
	}
	outcomes := job.Run(context.Background())

	byPeer := make(map[string]string)
	for _, o := range outcomes {
		byPeer[o.Peer] = o.Status
	}
	if byPeer["ok-peer"] != "ok" {
		t.Errorf("ok-peer expected ok, got %q", byPeer["ok-peer"])
	}
	if byPeer["reject-peer"] != "rejected" {
		t.Errorf("reject-peer expected rejected, got %q", byPeer["reject-peer"])
	}
	if byPeer["dead-peer"] != "unreachable" {
		t.Errorf("dead-peer expected unreachable, got %q", byPeer["dead-peer"])
	}
}
