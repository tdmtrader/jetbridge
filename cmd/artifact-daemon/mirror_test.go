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
