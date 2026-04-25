package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// DefaultPreemptionMetadataURL is the GCP metadata endpoint that signals
// spot/preemptible VM preemption. Reads return "TRUE" once preemption is
// imminent (~30s warning) and "FALSE" otherwise. The query parameter
// `?wait_for_change=true` causes the metadata server to hold the
// connection open until the value transitions, providing efficient
// long-polling without a busy loop.
const DefaultPreemptionMetadataURL = "http://metadata.google.internal/computeMetadata/v1/instance/preempted"

// PreemptionWatcher long-polls the GCP metadata server's `preempted`
// endpoint and fires a callback exactly once when the value transitions
// to TRUE. The callback should drain the mirror queue and synchronously
// flush any unmirrored artifacts to peers within the preemption budget
// (~25s, leaving slack against the ~30s GCP warning window).
//
// On transport errors or non-2xx responses, the watcher logs and retries
// with a short backoff so a transient metadata-server hiccup doesn't
// disable preemption protection.
type PreemptionWatcher struct {
	metadataURL string
	onPreempted func(ctx context.Context)
	logger      lager.Logger
	client      *http.Client

	// errorBackoff is the pause between retries after a transport error
	// or non-2xx response. Default is short (~500ms) — the metadata
	// server is local-machine, so transient errors should clear quickly.
	errorBackoff time.Duration
}

// NewPreemptionWatcher constructs a watcher polling metadataURL.
// metadataURL is overridable so tests can point at a fake httptest
// server; production wires DefaultPreemptionMetadataURL.
func NewPreemptionWatcher(logger lager.Logger, metadataURL string, onPreempted func(ctx context.Context)) *PreemptionWatcher {
	return &PreemptionWatcher{
		metadataURL: metadataURL,
		onPreempted: onPreempted,
		logger:      logger,
		// Long timeout — wait_for_change can hold the connection for
		// several minutes if the value never changes.
		client:       &http.Client{Timeout: 10 * time.Minute},
		errorBackoff: 500 * time.Millisecond,
	}
}

// Run long-polls until preemption is signalled or ctx is cancelled.
// Fires the registered callback exactly once on the first TRUE response,
// then returns. Cancelling ctx returns without firing.
func (w *PreemptionWatcher) Run(ctx context.Context) {
	logger := w.logger.Session("preemption-watcher")
	logger.Info("starting", lager.Data{"url": w.metadataURL})

	for {
		select {
		case <-ctx.Done():
			logger.Info("stopped", lager.Data{"reason": ctx.Err().Error()})
			return
		default:
		}

		preempted, err := w.poll(ctx)
		if err != nil {
			// ctx cancellation surfaces as an error inside http; treat
			// that as a clean exit, not a transient error.
			if ctx.Err() != nil {
				logger.Info("stopped", lager.Data{"reason": ctx.Err().Error()})
				return
			}
			logger.Debug("poll-failed", lager.Data{"error": err.Error()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.errorBackoff):
			}
			continue
		}

		if preempted {
			logger.Info("preemption-notice-received")
			w.onPreempted(ctx)
			return
		}
		// FALSE → loop and re-issue wait_for_change request. The
		// metadata server will hold the connection until the value
		// changes, so this is not a busy-loop.
	}
}

// poll issues one wait_for_change request and reports whether the
// returned value is "TRUE". Non-2xx responses are returned as errors so
// the caller can apply its retry policy.
func (w *PreemptionWatcher) poll(ctx context.Context) (bool, error) {
	url := w.metadataURL
	if !strings.Contains(url, "?") {
		url += "?wait_for_change=true"
	} else {
		url += "&wait_for_change=true"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := w.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return false, &metadataPollError{status: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(string(body)) == "TRUE", nil
}

// metadataPollError is a typed error for non-2xx metadata responses so
// callers (and log scrapers) can distinguish them from transport errors.
type metadataPollError struct {
	status int
}

func (e *metadataPollError) Error() string {
	return "metadata server returned status " + http.StatusText(e.status)
}
