package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestPreemptionNoticeEndpointLatchesOneNoticeAndHonorsItsBoundedWait(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("preemption-notice"), t.TempDir(), "node-a")
	observed := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	if !server.RecordPreemptionNotice(observed) {
		t.Fatal("first preemption notice was not recorded")
	}
	if server.RecordPreemptionNotice(observed.Add(time.Second)) {
		t.Fatal("second preemption notice replaced the latched notice")
	}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkpoints/v1/preemption-notice?after=0&wait=0s", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("notice status = %d, want %d", response.Code, http.StatusOK)
	}
	if got, want := response.Body.String(), `{"sequence":1,"observed_at":"2026-07-29T12:00:00Z"}`+"\n"; got != want {
		t.Fatalf("notice body = %q, want %q", got, want)
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkpoints/v1/preemption-notice?after=1&wait=0s", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("acknowledged notice status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestPreemptionNoticeEndpointRequiresMTLSAndRejectsInvalidBounds(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("preemption-notice"), t.TempDir(), "node-a")
	handler := server.Handler(WithTLS())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkpoints/v1/preemption-notice?wait=0s", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("uncredentialed notice status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkpoints/v1/preemption-notice?wait=26s", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unbounded notice wait status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestPreemptionWatcherCanRecordOneInjectedNodeNotice(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("preemption-notice"), t.TempDir(), "node-a")
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("TRUE"))
	}))
	defer metadata.Close()

	watcher := NewPreemptionWatcher(lagertest.NewTestLogger("preemption-watcher"), metadata.URL, func(context.Context) {
		server.RecordPreemptionNotice(time.Time{})
	})
	watcher.Run(context.Background())

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkpoints/v1/preemption-notice?wait=0s", nil))
	if response.Code != http.StatusOK || response.Body.String() == "" {
		t.Fatalf("latched watcher notice = status %d body %q", response.Code, response.Body.String())
	}
}

func TestStartPreemptionWatcherLatchesNoticeWithoutMirror(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("preemption-notice"), t.TempDir(), "node-a")
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("TRUE"))
	}))
	defer metadata.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startPreemptionWatcher(ctx, lagertest.NewTestLogger("preemption-watcher"), server, nil, time.Second, metadata.URL)
	eventuallyPreemptionNotice(t, server)
}

func eventuallyPreemptionNotice(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkpoints/v1/preemption-notice?wait=0s", nil))
		if response.Code == http.StatusOK {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("watcher did not latch the injected preemption notice")
}

// ---------------------------------------------------------------------------
// preemption.Watcher — long-poll GCP metadata for spot preemption notice.
// ---------------------------------------------------------------------------

func TestPreemptionWatcher_FiresCallbackOnTrue(t *testing.T) {
	var (
		gotMetadataFlavor string
		gotWaitForChange  string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMetadataFlavor = r.Header.Get("Metadata-Flavor")
		gotWaitForChange = r.URL.Query().Get("wait_for_change")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("TRUE"))
	}))
	defer srv.Close()

	var fired int32
	callbackDone := make(chan struct{}, 1)
	logger := lagertest.NewTestLogger("preempt")

	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		atomic.AddInt32(&fired, 1)
		callbackDone <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watcher.Run(ctx)

	select {
	case <-callbackDone:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("expected onPreempted callback to fire on TRUE response")
	}

	if got := atomic.LoadInt32(&fired); got != 1 {
		t.Errorf("expected callback to fire exactly once, got %d", got)
	}
	if gotMetadataFlavor != "Google" {
		t.Errorf("expected Metadata-Flavor: Google header, got %q", gotMetadataFlavor)
	}
	if gotWaitForChange != "true" {
		t.Errorf("expected ?wait_for_change=true, got %q", gotWaitForChange)
	}
}

func TestPreemptionWatcher_RetriesOnTransientError(t *testing.T) {
	// Server returns 500 the first two times, then 200 TRUE. Watcher
	// should keep polling and eventually fire the callback.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("TRUE"))
	}))
	defer srv.Close()

	callbackDone := make(chan struct{}, 1)
	logger := lagertest.NewTestLogger("preempt")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		callbackDone <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watcher.Run(ctx)

	select {
	case <-callbackDone:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("expected callback to fire after server recovered from transient errors")
	}

	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Errorf("expected at least 3 polls (2 errors + 1 success), got %d", got)
	}
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

	// Fake metadata server that returns TRUE immediately.
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("TRUE"))
	}))
	defer metadata.Close()

	// Wire the watcher's callback to invoke Evacuate, mirroring main.go's
	// production wiring.
	evacuateDone := make(chan struct{}, 1)
	watcher := NewPreemptionWatcher(logger, metadata.URL, func(ctx context.Context) {
		mirror.Evacuate(ctx, 3*time.Second)
		evacuateDone <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watcher.Run(ctx)

	select {
	case <-evacuateDone:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("expected Evacuate to complete after preempt notice")
	}

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
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(body))
			}))
			defer srv.Close()

			var fired int32
			logger := lagertest.NewTestLogger("malformed")
			watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
				atomic.AddInt32(&fired, 1)
			})

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			watcher.Run(ctx)

			if got := atomic.LoadInt32(&fired); got != 0 {
				t.Errorf("body %q should NOT fire callback, but it did (count=%d)", body, got)
			}
		})
	}
}

// TestPreemptionWatcher_AllowsTrailingWhitespace verifies that "TRUE\n"
// (with a trailing newline — a common quirk of HTTP body responses)
// still fires the callback. The metadata server is documented to return
// the exact value but we should be tolerant of whitespace.
func TestPreemptionWatcher_AllowsTrailingWhitespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("TRUE\n"))
	}))
	defer srv.Close()

	fired := make(chan struct{}, 1)
	logger := lagertest.NewTestLogger("trailing-ws")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		fired <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watcher.Run(ctx)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("expected TRUE\\n (with trailing newline) to fire callback")
	}
}

func TestPreemptionWatcher_DoesNotFireOnFalse(t *testing.T) {
	// Server returns "FALSE" indefinitely. Watcher should keep polling
	// without firing the callback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("FALSE"))
	}))
	defer srv.Close()

	var fired int32
	logger := lagertest.NewTestLogger("preempt")
	watcher := NewPreemptionWatcher(logger, srv.URL, func(ctx context.Context) {
		atomic.AddInt32(&fired, 1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	watcher.Run(ctx)

	if got := atomic.LoadInt32(&fired); got != 0 {
		t.Errorf("expected no callback on FALSE responses, got %d", got)
	}
}
