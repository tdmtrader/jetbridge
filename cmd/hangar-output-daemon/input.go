package main

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

type inputStages struct {
	mu     sync.Mutex
	stages map[output.ReservationID]*stagedInput
}

type stagedInput struct {
	identity output.InputStage
	tree     *hangar.CapturedTree
	release  func()
	timer    *time.Timer
}

func (s *stagedInput) close() {
	_ = s.tree.Close()
	s.release()
}

func (s *Server) stageInput(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRead(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), min(s.daemon.operationTimeout, 2*time.Minute))
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := http.NewResponseController(w).SetReadDeadline(deadline); err != nil {
		readRefusal(w, output.ErrInfrastructure)
		return
	}
	defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	release, err := s.spooling(ctx)
	if err != nil {
		readRefusal(w, err)
		return
	}
	owned := false
	defer func() {
		if !owned {
			release()
		}
	}()
	limit, _ := hangar.CanonicalArchiveByteLimit(output.MaxInputContentBytes, hangar.DefaultMaxTreeEntries)
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	canonicalizer := s.daemon.canonicalizer
	canonicalizer.MaxContentBytes = output.MaxInputContentBytes
	tree, err := canonicalizer.Capture(ctx, body)
	if err != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	now := nowUTC()
	stage := output.InputStage{Version: output.InputPublicationVersion,
		ReservationID: output.ReservationID(uuid.NewString()), NodeUID: s.daemon.nodeUID,
		ActivationEpoch: s.daemon.epoch, Scope: s.daemon.namespace.Scope(),
		Digest: tree.Digest, Bytes: tree.ByteSize, CreatedAt: output.NewTimestamp(now),
		ExpiresAt: output.NewTimestamp(now.Add(min(s.daemon.operationTimeout, 2*time.Minute)))}
	if err := stage.Validate(); err != nil {
		_ = tree.Close()
		readRefusal(w, err)
		return
	}
	entry := &stagedInput{identity: stage, tree: tree, release: release}
	s.inputs.mu.Lock()
	if s.inputs.stages == nil {
		s.inputs.stages = make(map[output.ReservationID]*stagedInput)
	}
	s.inputs.stages[stage.ReservationID] = entry
	entry.timer = time.AfterFunc(time.Until(stage.ExpiresAt.Time), func() {
		s.inputs.mu.Lock()
		present := s.inputs.stages[stage.ReservationID] == entry
		if present {
			delete(s.inputs.stages, stage.ReservationID)
		}
		s.inputs.mu.Unlock()
		if present {
			entry.close()
		}
	})
	s.inputs.mu.Unlock()
	owned = true
	w.Header().Set("Cache-Control", "no-store")
	_ = http.NewResponseController(w).SetWriteDeadline(deadline)
	writeJSON(w, http.StatusOK, stage)
}

func (s *Server) publishInput(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRead(w, r) {
		return
	}
	var request output.InputPublishRequest
	if decodeRead(r, &request) != nil || request.Validate() != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	s.inputs.mu.Lock()
	entry := s.inputs.stages[request.ReservationID]
	if entry != nil {
		delete(s.inputs.stages, request.ReservationID)
		entry.timer.Stop()
	}
	s.inputs.mu.Unlock()
	if entry == nil {
		readRefusal(w, output.ErrNotFound)
		return
	}
	defer entry.close()
	if !nowUTC().Before(entry.identity.ExpiresAt.Time) {
		readRefusal(w, output.ErrNotFound)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), entry.identity.ExpiresAt.Time)
	defer cancel()
	archive, err := os.Open(entry.tree.ArchivePath)
	if err != nil {
		readRefusal(w, output.ErrInfrastructure)
		return
	}
	defer archive.Close()
	object, err := s.daemon.publisher.EnsurePublication(ctx, entry.identity.ObjectMarker(), archive, entry.tree.ByteSize)
	if err != nil {
		readRefusal(w, err)
		return
	}
	publication, err := s.daemon.signer.SignInputPublication(output.InputPublication{
		Stage: entry.identity, Nonce: request.Nonce, Attributes: output.AttributesFromFoundation(object.Attributes),
		Metageneration: object.Metageneration, Marker: object.Marker.Metadata(),
	})
	if err != nil {
		readRefusal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = http.NewResponseController(w).SetWriteDeadline(entry.identity.ExpiresAt.Time)
	writeJSON(w, http.StatusOK, publication)
}
