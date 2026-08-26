package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// DurableTierHeader is set on every /resource-caches/ response when a durable
// tier is configured, whatever the status.
//
// This is how an ATC learns the cluster can serve a warm at all, and it rides a
// request the ATC already sends rather than needing a probe of its own. An ATC
// talking to daemons that predate the tier therefore issues zero requests to a
// route they do not have.
//
// It is set on error responses too. A daemon that answers 404 for one key is
// still the daemon that can warm it, and a transient 500 must not make a node
// look permanently tier-incapable.
const DurableTierHeader = "X-Durable-Tier"

// ArtifactTierHeader says where a restore's bytes came from: "durable" if this
// call fetched them, "local" if they were already here.
const ArtifactTierHeader = "X-Artifact-Tier"

// durableRestoreRequest is the body of POST /durable/restore.
//
// The two names are different namespaces and must not be conflated. Key is the
// node-local alias, and becomes a direct child of steps/ — which is the only
// thing the sweeper reclaims, so it must be a single path segment. DurableKey
// names an object in a bucket and carries a retention-class prefix that an
// object lifecycle rule acts on.
type durableRestoreRequest struct {
	Key        string `json:"key"`
	DurableKey string `json:"durable_key"`
}

type durableRestoreResponse struct {
	Restored bool   `json:"restored"`
	Node     string `json:"node,omitempty"`
	Path     string `json:"path,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// handleDurableRestore pulls an object out of the durable store and registers
// it locally, so that the caller may then read it from this daemon exactly as if
// it had been here all along.
//
// This is deliberately a separate verb from HEAD /resource-caches/. A HEAD 200
// means "these bytes are on this node's disk right now", which is what makes the
// responding pod worth binding to. Every daemon sees the same bucket, so if HEAD
// also answered from the durable store they would all say yes for anything ever
// stored, and the ATC's choice of pod would become arbitrary — losing the node
// affinity the probe exists to provide. This verb answers the different question
// "who can get it", and makes its own answer true before returning.
func (s *Server) handleDurableRestore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req durableRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	// SHAPE checks, not meaning checks: can these strings be joined onto a root
	// without escaping it. They say nothing about what the keys name, so this is
	// not the classification the daemon is forbidden to do. Without them, "../.."
	// would name any directory on the host -- and this process runs as root with
	// CAP_DAC_OVERRIDE.
	//
	// Reusing the store's validator rather than restating the pattern: two copies
	// of a key format in one binary is how they drift apart.
	if err := durable.ValidateKey(req.DurableKey); err != nil {
		http.Error(w, fmt.Sprintf("invalid durable_key: %v", err), http.StatusBadRequest)
		return
	}

	// The local alias is stricter: it becomes a direct child of steps/, and a
	// direct child is the only thing the sweeper reclaims. A nested one would
	// never be swept, and node disk would grow without bound -- which is exactly
	// how task caches on hostPath became monotonic.
	if err := durable.ValidateKey(req.Key); err != nil || strings.Contains(req.Key, "/") {
		http.Error(w, "invalid key: must be a single path segment", http.StatusBadRequest)
		return
	}

	// Distinguished from 404 so a half-configured cluster does not read as a
	// permanently cold cache in the logs.
	if s.durable == nil {
		http.Error(w, "durable tier not configured", http.StatusNotImplemented)
		return
	}

	logger := s.logger.Session("durable-restore", lager.Data{"key": req.Key, "durable_key": req.DurableKey})
	start := time.Now()

	if loc, found := s.lookupRegistry(req.Key); found {
		if _, err := s.root.Stat(osName(loc)); err == nil {
			// Path in the response is the ABSOLUTE form: it is response JSON
			// the caller acts on, an external contract rather than an internal
			// representation.
			s.writeRestoreResult(w, http.StatusOK, "local", durableRestoreResponse{
				Restored: false, Node: s.nodeName, Path: s.registry.AmbientPath(loc),
			})
			return
		}
		// A stale alias. Fall through and restore over it.
		s.registry.Remove(req.Key)
	}

	dest := filepath.Join(s.storagePath, "steps", req.Key)

	// Collapse concurrent restores of one key. Several builds on this node can
	// want the same cache at once, and each would otherwise pull its own copy
	// out of the bucket and race to rename onto the same destination.
	restored, _, _ := s.restoreFlight.Do(req.Key, func() (any, error) {
		// Hold the sweep side: the restore rename and the sweeper's removal
		// both operate on this directory, and the sweeper must not delete a
		// tree that is being assembled.
		release := s.guard.BeginSweep(req.Key)
		defer release()

		// The request context dies when this handler returns, but the caller
		// is waiting on the response, so the caller's deadline is the right
		// one. DurableTier.Restore applies its own timeout on top.
		if !s.durable.Restore(r.Context(), req.DurableKey, dest) {
			return false, nil
		}
		if _, err := s.registry.RegisterAlias(req.Key, dest); err != nil {
			// The tree was just renamed into place under the steps root, so a
			// refusal cannot mean an escape. Reporting success for something no
			// later lookup can find would show up as an unexplained repeat
			// restore of the same key on every build.
			logger.Error("failed-to-register-restored-alias", err, lager.Data{"dest": dest})
			return false, nil
		}

		return true, nil
	})

	if ok, _ := restored.(bool); !ok {
		// A store miss and a store failure are deliberately indistinguishable:
		// the caller's next move — re-run the get step — is identical, and
		// making them distinguishable invites a caller to treat one as fatal.
		logger.Debug("not-restored")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	logger.Info("restored", lager.Data{"duration": time.Since(start).String()})
	s.writeRestoreResult(w, http.StatusCreated, "durable", durableRestoreResponse{
		Restored: true, Node: s.nodeName, Path: dest, Duration: time.Since(start).String(),
	})
}

func (s *Server) writeRestoreResult(w http.ResponseWriter, status int, tier string, body durableRestoreResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(ArtifactTierHeader, tier)
	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// promoteToDurable uploads a just-registered artifact to the durable store.
//
// Detached, because the build's next step is blocked on the register response
// and gigabytes must not sit in that path. Bounded, because DurableTier collapses
// concurrent uploads per key only: N distinct caches finishing at once would
// each spool a full copy to temporary storage and could evict the DaemonSet on
// disk pressure.
//
// It deliberately does NOT skip an upload when the key already exists. A
// cleanup init container can truncate a tar from outside any guard this daemon
// holds; what makes that survivable is the next producer overwriting it.
// Skipping would make one truncated object permanent and cluster-wide.
//
// The guard key comes from loc, not from a path the caller holds: this runs
// detached and only logs, so a key that locks nothing is invisible until a
// later restore finds a short object.
func (s *Server) promoteToDurable(ctx context.Context, key string, loc RelKey) {
	if s.durable == nil {
		return
	}

	// Detach from the request: the response returns long before this finishes.
	ctx = context.WithoutCancel(ctx)

	go func() {
		select {
		case s.uploadSem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-s.uploadSem }()

		// The walk must hold the read side. tarDirectory only errors when the
		// walk callback does; a subtree deleted before it is enumerated yields
		// no entries and no error, and the archive closes cleanly — a short tar
		// that reads as a complete one.
		release := s.guard.BeginRead(s.stepHandle(loc))
		defer release()
		s.touchStepDir(loc)

		s.durable.Store(ctx, key, func(w io.Writer) error {
			return s.tarDirectory(w, loc)
		})
	}()
}

// advertiseDurableTier tells the caller this daemon can serve a warm.
//
// Called before any status is written, on every path including errors, because
// it describes the daemon rather than the key.
func (s *Server) advertiseDurableTier(w http.ResponseWriter) {
	if s.durable != nil {
		w.Header().Set(DurableTierHeader, "enabled")
	}
}
