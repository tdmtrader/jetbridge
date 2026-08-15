package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

type metadataServiceState uint8

const (
	metadataUnavailable metadataServiceState = iota
	metadataAvailable
	metadataWaitingForChange
)

// metadataService models the metadata endpoint's availability and long-poll
// lifecycle. A non-preemption response moves it into a held second poll, which
// gives tests a deterministic barrier proving the first response was handled.
type metadataService struct {
	mu    sync.Mutex
	state metadataServiceState
	body  string

	metadataFlavor string
	waitForChange  string

	unavailableRequest chan struct{}
	repollStarted      chan struct{}
	unavailableOnce    sync.Once
	repollOnce         sync.Once
}

func newUnavailableMetadataService() *metadataService {
	return newMetadataService(metadataUnavailable, "")
}

func newAvailableMetadataService(body string) *metadataService {
	return newMetadataService(metadataAvailable, body)
}

func newMetadataService(state metadataServiceState, body string) *metadataService {
	return &metadataService{
		state:              state,
		body:               body,
		unavailableRequest: make(chan struct{}),
		repollStarted:      make(chan struct{}),
	}
}

func (service *metadataService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	service.mu.Lock()
	service.metadataFlavor = r.Header.Get("Metadata-Flavor")
	service.waitForChange = r.URL.Query().Get("wait_for_change")
	state := service.state
	body := service.body
	if state == metadataAvailable {
		service.state = metadataWaitingForChange
	}
	service.mu.Unlock()

	switch state {
	case metadataUnavailable:
		service.unavailableOnce.Do(func() { close(service.unavailableRequest) })
		w.WriteHeader(http.StatusInternalServerError)
	case metadataAvailable:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	case metadataWaitingForChange:
		service.repollOnce.Do(func() { close(service.repollStarted) })
		<-r.Context().Done()
	}
}

func (service *metadataService) MakeAvailable(body string) {
	service.mu.Lock()
	service.state = metadataAvailable
	service.body = body
	service.mu.Unlock()
}

func (service *metadataService) RequestContract() (string, string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.metadataFlavor, service.waitForChange
}

func startPreemptionWatcher(t *testing.T, watcher *PreemptionWatcher) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		watcher.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func waitForLifecycleSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(failure)
	}
}

func assertNoCallbackSignal(t *testing.T, callback <-chan struct{}) {
	t.Helper()
	select {
	case <-callback:
		t.Fatal("preemption callback fired more than once or for a non-preemption response")
	default:
	}
}

// ---------------------------------------------------------------------------
// preemption.Watcher — long-poll GCP metadata for spot preemption notice.
// ---------------------------------------------------------------------------

func TestPreemptionWatcher_FiresCallbackOnTrue(t *testing.T) {
	metadata := newAvailableMetadataService("TRUE")
	srv := httptest.NewServer(metadata)
	defer srv.Close()

	callbackSignals := make(chan struct{}, 2)
	logger := lagertest.NewTestLogger("preempt")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		callbackSignals <- struct{}{}
	})

	_, watcherDone := startPreemptionWatcher(t, watcher)
	waitForLifecycleSignal(t, callbackSignals, "expected onPreempted callback to fire on TRUE response")
	waitForLifecycleSignal(t, watcherDone, "watcher did not exit after preemption callback")
	assertNoCallbackSignal(t, callbackSignals)

	gotMetadataFlavor, gotWaitForChange := metadata.RequestContract()
	if gotMetadataFlavor != "Google" {
		t.Errorf("expected Metadata-Flavor: Google header, got %q", gotMetadataFlavor)
	}
	if gotWaitForChange != "true" {
		t.Errorf("expected ?wait_for_change=true, got %q", gotWaitForChange)
	}
}

func TestPreemptionWatcher_RetriesOnTransientError(t *testing.T) {
	metadata := newUnavailableMetadataService()
	srv := httptest.NewServer(metadata)
	defer srv.Close()

	callbackSignals := make(chan struct{}, 2)
	logger := lagertest.NewTestLogger("preempt")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		callbackSignals <- struct{}{}
	})
	watcher.errorBackoff = time.Millisecond

	_, watcherDone := startPreemptionWatcher(t, watcher)
	waitForLifecycleSignal(t, metadata.unavailableRequest, "metadata service was not polled while unavailable")
	metadata.MakeAvailable("TRUE")

	waitForLifecycleSignal(t, callbackSignals, "expected callback after metadata service became available")
	waitForLifecycleSignal(t, watcherDone, "watcher did not exit after recovered metadata response")
	assertNoCallbackSignal(t, callbackSignals)
}

// ---------------------------------------------------------------------------
// Behavioral: watcher fires Evacuate, which flushes unmirrored artifacts
// to a peer within budget (P3.8).
// ---------------------------------------------------------------------------

func TestPreemptionWatcher_FiresEvacuate_FlushesUnmirroredToPeer(t *testing.T) {
	// Storage with one unmirrored step dir.
	storage := t.TempDir()
	stepDir := filepath.Join(storage, "steps", "unflushed-handle", "result")
	if err := os.MkdirAll(stepDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "out.txt"), []byte("evacuated"), 0644); err != nil {
		t.Fatal(err)
	}

	// Live peer that records PUTs.
	var (
		mu          sync.Mutex
		receivedKey string
	)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && len(r.URL.Path) > len("/stream-in/") {
			mu.Lock()
			receivedKey = r.URL.Path[len("/stream-in/"):]
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer peer.Close()

	const peerHost = "evac-peer"
	transport := &mirrorRoutingTransport{routes: map[string]string{
		peerHost + ":7780": peer.URL,
	}}

	logger := lagertest.NewTestLogger("preempt-evacuate")
	mirror := &Mirror{
		storagePath:     storage,
		port:            7780,
		scheme:          "http",
		replicas:        2,
		perPeerTimeout:  3 * time.Second,
		pool:            NewWorkerPool(2),
		client:          &http.Client{Transport: transport, Timeout: 3 * time.Second},
		logger:          logger,
		status:          make(map[string]map[string]string),
		evacuationPeers: []string{peerHost},
	}
	defer mirror.Stop()

	metadataService := newAvailableMetadataService("TRUE")
	metadata := httptest.NewServer(metadataService)
	defer metadata.Close()

	// Wire the watcher's callback to invoke Evacuate, mirroring main.go's
	// production wiring.
	evacuateSignals := make(chan struct{}, 2)
	watcher := NewPreemptionWatcher(logger, metadata.URL, func(ctx context.Context) {
		mirror.Evacuate(ctx, 3*time.Second)
		evacuateSignals <- struct{}{}
	})

	_, watcherDone := startPreemptionWatcher(t, watcher)
	waitForLifecycleSignal(t, evacuateSignals, "expected Evacuate to complete after preempt notice")
	waitForLifecycleSignal(t, watcherDone, "watcher did not exit after evacuation")
	assertNoCallbackSignal(t, evacuateSignals)

	mu.Lock()
	defer mu.Unlock()
	if receivedKey != "unflushed-handle/result" {
		t.Errorf("expected peer to receive PUT for unflushed-handle/result, got %q", receivedKey)
	}
}

// TestPreemptionWatcher_IgnoresMalformedBody verifies that surprising
// metadata responses (extra whitespace, unexpected case, garbage) do
// NOT fire the evacuation callback. Only the literal "TRUE" should
// trigger — anything else is treated as not-yet-preempted.
func TestPreemptionWatcher_IgnoresMalformedBody(t *testing.T) {
	cases := []string{
		"true",      // wrong case
		"PREEMPTED", // wrong word
		"",          // empty
		"\x00\x01",  // garbage bytes
		"YES",
	}
	for _, body := range cases {
		t.Run("body="+body, func(t *testing.T) {
			metadata := newAvailableMetadataService(body)
			srv := httptest.NewServer(metadata)
			defer srv.Close()

			callbackSignals := make(chan struct{}, 1)
			logger := lagertest.NewTestLogger("malformed")
			watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
				callbackSignals <- struct{}{}
			})

			cancel, watcherDone := startPreemptionWatcher(t, watcher)
			waitForLifecycleSignal(t, metadata.repollStarted, "watcher did not continue polling after malformed metadata")
			cancel()
			waitForLifecycleSignal(t, watcherDone, "watcher did not stop after malformed metadata test cancellation")
			assertNoCallbackSignal(t, callbackSignals)
		})
	}
}

// TestPreemptionWatcher_AllowsTrailingWhitespace verifies that "TRUE\n"
// (with a trailing newline — a common quirk of HTTP body responses)
// still fires the callback. The metadata server is documented to return
// the exact value but we should be tolerant of whitespace.
func TestPreemptionWatcher_AllowsTrailingWhitespace(t *testing.T) {
	metadata := newAvailableMetadataService("TRUE\n")
	srv := httptest.NewServer(metadata)
	defer srv.Close()

	callbackSignals := make(chan struct{}, 2)
	logger := lagertest.NewTestLogger("trailing-ws")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		callbackSignals <- struct{}{}
	})

	_, watcherDone := startPreemptionWatcher(t, watcher)
	waitForLifecycleSignal(t, callbackSignals, "expected TRUE\\n (with trailing newline) to fire callback")
	waitForLifecycleSignal(t, watcherDone, "watcher did not exit after whitespace-tolerant preemption callback")
	assertNoCallbackSignal(t, callbackSignals)
}

func TestPreemptionWatcher_DoesNotFireOnFalse(t *testing.T) {
	metadata := newAvailableMetadataService("FALSE")
	srv := httptest.NewServer(metadata)
	defer srv.Close()

	callbackSignals := make(chan struct{}, 1)
	logger := lagertest.NewTestLogger("preempt")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		callbackSignals <- struct{}{}
	})

	cancel, watcherDone := startPreemptionWatcher(t, watcher)
	waitForLifecycleSignal(t, metadata.repollStarted, "watcher did not continue long-polling after FALSE metadata")
	cancel()
	waitForLifecycleSignal(t, watcherDone, "watcher did not stop after FALSE metadata test cancellation")
	assertNoCallbackSignal(t, callbackSignals)
}
