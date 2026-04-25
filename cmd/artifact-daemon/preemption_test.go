package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

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
