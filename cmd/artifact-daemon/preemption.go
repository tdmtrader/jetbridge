package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
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

// maxPreemptionNoticeWait bounds one daemon notice long-poll below the GCE
// preemption warning window. Callers may reissue a request after a 204.
const maxPreemptionNoticeWait = 25 * time.Second

// preemptionNotice is node-local: it deliberately contains no attempt,
// checkpoint, or principal identity.
type preemptionNotice struct {
	Sequence   uint64    `json:"sequence"`
	ObservedAt time.Time `json:"observed_at"`
}

// preemptionNoticeLatch coalesces a real node warning into a single immutable
// notice. It neither initiates process shutdown nor fabricates notices.
type preemptionNoticeLatch struct {
	mu      sync.Mutex
	notice  preemptionNotice
	changed chan struct{}
}

func newPreemptionNoticeLatch() *preemptionNoticeLatch {
	return &preemptionNoticeLatch{changed: make(chan struct{})}
}

func (l *preemptionNoticeLatch) record(observedAt time.Time) bool {
	if l == nil {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.notice.Sequence != 0 {
		return false
	}
	l.notice = preemptionNotice{Sequence: 1, ObservedAt: observedAt.UTC()}
	close(l.changed)
	return true
}

func (l *preemptionNoticeLatch) wait(ctx context.Context, after uint64) (preemptionNotice, bool) {
	if l == nil {
		return preemptionNotice{}, false
	}
	for {
		l.mu.Lock()
		notice := l.notice
		changed := l.changed
		l.mu.Unlock()
		if notice.Sequence > after {
			return notice, true
		}
		if notice.Sequence != 0 {
			<-ctx.Done()
			return preemptionNotice{}, false
		}
		select {
		case <-ctx.Done():
			return preemptionNotice{}, false
		case <-changed:
		}
	}
}

// RecordPreemptionNotice records an externally observed node notice once. The
// watcher and metadata source remain explicitly injected by daemon startup.
func (s *Server) RecordPreemptionNotice(observedAt time.Time) bool {
	if s == nil {
		return false
	}
	return s.preemptionNotices.record(observedAt)
}

func (s *Server) handlePreemptionNotice(w http.ResponseWriter, r *http.Request) {
	after, err := parsePreemptionNoticeAfter(r.URL.Query().Get("after"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wait, err := parsePreemptionNoticeWait(r.URL.Query().Get("wait"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	notice, ok := s.preemptionNotices.wait(ctx, after)
	if !ok {
		if r.Context().Err() != nil {
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(notice); err != nil {
		s.logger.Debug("write-preemption-notice-failed", lager.Data{"error": err.Error()})
	}
}

func parsePreemptionNoticeAfter(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid preemption notice cursor")
	}
	return after, nil
}

func parsePreemptionNoticeWait(raw string) (time.Duration, error) {
	if raw == "" {
		return maxPreemptionNoticeWait, nil
	}
	wait, err := time.ParseDuration(raw)
	if err != nil || wait < 0 || wait > maxPreemptionNoticeWait {
		return 0, fmt.Errorf("preemption notice wait must be between 0s and %s", maxPreemptionNoticeWait)
	}
	return wait, nil
}

// startPreemptionWatcher connects the explicitly enabled metadata source to
// the daemon's node-local latch. Mirroring remains optional and is never the
// authority for publishing a preemption notice.
func startPreemptionWatcher(
	ctx context.Context,
	logger lager.Logger,
	server *Server,
	mirror *Mirror,
	budget time.Duration,
	metadataURL string,
) {
	watcher := NewPreemptionWatcher(logger.Session("preempt"), metadataURL, func(ctx context.Context) {
		latched := server.RecordPreemptionNotice(time.Now().UTC())
		logger.Info("preemption-notice-latched", lager.Data{"recorded": latched})
		if mirror == nil {
			return
		}
		logger.Info("evacuating-on-preemption", lager.Data{"budget": budget.String()})
		mirror.Evacuate(ctx, budget)
	})
	go watcher.Run(ctx)
}

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
