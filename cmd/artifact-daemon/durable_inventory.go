package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/hangar"
)

// DurableSnapshotObject is one durable snapshot object as storage holds it.
// Generation travels to the caller and back so a reclamation decision made
// about these exact bytes cannot be applied to a later re-upload.
type DurableSnapshotObject struct {
	Digest     string `json:"digest"`
	Key        string `json:"key"`
	Generation int64  `json:"generation"`
	Bytes      int64  `json:"bytes"`
	CreatedAt  string `json:"created_at"`
}

type DurableSnapshotInventory struct {
	Objects []DurableSnapshotObject `json:"objects"`
}

// handleDurableSnapshotInventory answers "what does the durable store actually
// hold". The ATC cannot derive this from its own tables: an object whose row
// was never written is invisible to every metadata query by construction, and
// enumerating storage is the only way to see it. The daemon reports; it never
// decides. Only the ATC knows what is referenced.
func (s *Server) handleDurableSnapshotInventory(w http.ResponseWriter, r *http.Request) {
	if s.hangar == nil {
		http.Error(w, "durable storage is not configured", http.StatusNotFound)
		return
	}
	inventory := DurableSnapshotInventory{Objects: []DurableSnapshotObject{}}
	err := s.hangar.List(r.Context(), hangar.KindSnapshot, func(attributes hangar.Attributes) error {
		inventory.Objects = append(inventory.Objects, DurableSnapshotObject{
			Digest:     string(attributes.Ref.Digest),
			Key:        attributes.Ref.Key,
			Generation: attributes.Ref.Generation,
			Bytes:      attributes.CompressedBytes,
			CreatedAt:  attributes.CreatedAt.UTC().Format(durableInventoryTimeLayout),
		})
		return nil
	})
	if err != nil {
		s.logger.Error("list-durable-snapshot-inventory-failed", err)
		http.Error(w, "durable snapshot inventory failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(inventory)
}

// handleDeleteDurableSnapshotObject deletes exactly one generation. The
// generation is mandatory: an unpinned durable delete would let a decision made
// about an orphan destroy whatever occupies that digest by the time the request
// lands.
func (s *Server) handleDeleteDurableSnapshotObject(w http.ResponseWriter, r *http.Request) {
	if s.hangar == nil {
		http.Error(w, "durable storage is not configured", http.StatusNotFound)
		return
	}
	digest := hangar.Digest(strings.TrimSpace(r.PathValue("digest")))
	if err := digest.Validate(); err != nil {
		http.Error(w, "malformed durable snapshot digest", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("generation"))
	if raw == "" {
		http.Error(w, "durable snapshot deletion requires a generation", http.StatusBadRequest)
		return
	}
	generation, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || generation <= 0 {
		http.Error(w, "malformed durable snapshot generation", http.StatusBadRequest)
		return
	}
	ref, err := hangar.NewObjectRef(hangar.KindSnapshot, digest, generation)
	if err != nil {
		http.Error(w, "malformed durable snapshot reference", http.StatusBadRequest)
		return
	}
	if err := s.hangar.Delete(r.Context(), ref); err != nil {
		if errors.Is(err, hangar.ErrConflict) {
			// The digest was rewritten after it was judged. Refusing is the
			// point of pinning the generation.
			http.Error(w, "durable snapshot generation no longer matches", http.StatusConflict)
			return
		}
		s.logger.Error("delete-durable-snapshot-object-failed", err)
		http.Error(w, "durable snapshot deletion failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const durableInventoryTimeLayout = "2006-01-02T15:04:05.999999999Z07:00"
