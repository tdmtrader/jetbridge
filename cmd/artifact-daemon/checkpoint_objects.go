package main

import (
	"errors"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/hangar"
)

// checkpointObjectDigestRequest names one durable checkpoint object by content
// alone. The caller supplies no key, kind, or bound: the daemon derives the
// key from the digest and applies its own ceiling, so a request can identify
// an object but never widen what the daemon will read.
type checkpointObjectDigestRequest struct {
	Digest hangar.Digest `json:"digest"`
}

// handleInspectCheckpointObject answers whether one durable checkpoint object
// exists and at which generation. The ATC's reclamation pass needs this to
// settle an abandoned upload: the database recorded that bytes were on their
// way and never heard back, and only storage knows whether they landed.
func (s *Server) handleInspectCheckpointObject(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var request checkpointObjectDigestRequest
	if err := decodeCheckpointCaptureJSON(w, r, &request); err != nil {
		s.metrics.recordCheckpoint("inspect-object", "rejected", 0, time.Since(started))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := hangar.Key(hangar.KindCheckpoint, request.Digest); err != nil {
		s.metrics.recordCheckpoint("inspect-object", "rejected", 0, time.Since(started))
		http.Error(w, "malformed checkpoint object digest", http.StatusBadRequest)
		return
	}
	// A daemon with no durable store cannot prove absence, only ignorance.
	// Reporting that as 404 would be indistinguishable from a proven absence,
	// and the caller acts on absence by forgetting the row that is the last
	// remaining name for the object.
	if s.hangar == nil {
		s.metrics.recordCheckpoint("inspect-object", "unavailable", 0, time.Since(started))
		http.Error(w, "durable checkpoint storage is not configured", http.StatusServiceUnavailable)
		return
	}

	attributes, err := s.hangar.Inspect(r.Context(), hangar.KindCheckpoint, request.Digest, s.checkpointMaxBytes)
	switch {
	case err == nil:
	case errors.Is(err, hangar.ErrNotFound):
		s.metrics.recordCheckpoint("inspect-object", "not_found", 0, time.Since(started))
		http.Error(w, "not found", http.StatusNotFound)
		return
	default:
		s.logger.Error("inspect-checkpoint-object-failed", err, lager.Data{"digest": string(request.Digest)})
		s.metrics.recordCheckpoint("inspect-object", "failed", 0, time.Since(started))
		http.Error(w, "durable checkpoint inspection failed", http.StatusBadGateway)
		return
	}

	s.metrics.recordCheckpoint("inspect-object", "ok", 0, time.Since(started))
	writeCheckpointCaptureJSON(w, http.StatusOK, attributes.Ref)
}

// handleDeleteCheckpointObject releases one exact durable checkpoint object.
//
// The generation in the request is the whole safety argument. The ATC decided
// this object was unreferenced by reading a database row under a lease; that
// judgment is only valid for the object the row named. Pinning the delete to
// that generation means a checkpoint captured between the ATC's decision and
// this request fails the delete instead of silently losing its content, and
// the conflict is reported distinctly so the caller keeps the row that still
// names the object rather than forgetting it.
func (s *Server) handleDeleteCheckpointObject(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var ref hangar.ObjectRef
	if err := decodeCheckpointCaptureJSON(w, r, &ref); err != nil {
		s.metrics.recordCheckpoint("delete-object", "rejected", 0, time.Since(started))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ref.Kind != hangar.KindCheckpoint {
		s.metrics.recordCheckpoint("delete-object", "rejected", 0, time.Since(started))
		http.Error(w, "durable object is not a checkpoint", http.StatusBadRequest)
		return
	}
	if err := ref.Validate(); err != nil {
		s.metrics.recordCheckpoint("delete-object", "rejected", 0, time.Since(started))
		http.Error(w, "malformed checkpoint object reference", http.StatusBadRequest)
		return
	}
	if s.hangar == nil {
		s.metrics.recordCheckpoint("delete-object", "unavailable", 0, time.Since(started))
		http.Error(w, "durable checkpoint storage is not configured", http.StatusServiceUnavailable)
		return
	}

	err := s.hangar.Delete(r.Context(), ref)
	switch {
	case err == nil:
	case errors.Is(err, hangar.ErrConflict):
		// The object changed under the caller's judgment. Saying so distinctly
		// is what lets the ATC keep the row that still names it.
		s.metrics.recordCheckpoint("delete-object", "conflict", 0, time.Since(started))
		http.Error(w, "durable checkpoint object changed before deletion", http.StatusConflict)
		return
	default:
		s.logger.Error("delete-checkpoint-object-failed", err, lager.Data{"digest": string(ref.Digest)})
		s.metrics.recordCheckpoint("delete-object", "failed", 0, time.Since(started))
		http.Error(w, "durable checkpoint deletion failed", http.StatusBadGateway)
		return
	}

	s.logger.Info("reclaimed-checkpoint-object", lager.Data{
		"digest":     string(ref.Digest),
		"generation": ref.Generation,
	})
	s.metrics.recordCheckpoint("delete-object", "ok", 0, time.Since(started))
	w.WriteHeader(http.StatusNoContent)
}
