package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
)

const checkpointCaptureRequestLimit = 64 << 10

type preparedCheckpointCapture struct {
	archive  *checkpoint.CapturedArchive
	maxBytes int64
	timer    *time.Timer
	cleanup  func()
}

// ConfigureCheckpointCapture sets daemon-owned bounds for capture staging.
// The request can only narrow the archive byte cap; it cannot select roots,
// exclusions, a host path, or a durable store.
func (s *Server) ConfigureCheckpointCapture(maxBytes, maxEntries int64, maxPrepared int, maxStaged int64, ttl time.Duration, exclusions []string) error {
	if maxBytes <= 0 || maxEntries <= 0 || maxPrepared <= 0 || maxStaged <= 0 || ttl <= 0 {
		return errors.New("checkpoint capture limits must be positive")
	}
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	s.checkpointMaxBytes = maxBytes
	s.checkpointMaxEntries = maxEntries
	s.checkpointMaxPrepared = maxPrepared
	s.checkpointMaxStaged = maxStaged
	s.checkpointPreparedTTL = ttl
	s.checkpointExclusions = append([]string(nil), exclusions...)
	return nil
}

func (s *Server) handleCheckpointPrepare(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var request checkpoint.ArchiveRequest
	if err := decodeCheckpointCaptureJSON(w, r, &request); err != nil || request.Validate() != nil {
		s.metrics.recordCheckpoint("prepare", "rejected", 0, time.Since(started))
		if err == nil {
			err = errors.New("invalid checkpoint archive request")
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.checkpointMu.Lock()
	if len(s.checkpointPrepared) >= s.checkpointMaxPrepared {
		s.checkpointMu.Unlock()
		s.metrics.recordCheckpoint("prepare", "unavailable", 0, time.Since(started))
		http.Error(w, "checkpoint staging is full", http.StatusTooManyRequests)
		return
	}
	maxBytes := minCheckpointCapture(request.MaxBytes, s.checkpointMaxBytes)
	stagingBytes, err := snapshot.CanonicalArchiveByteLimit(maxBytes, s.checkpointMaxEntries)
	if err != nil {
		s.checkpointMu.Unlock()
		s.metrics.recordCheckpoint("prepare", "rejected", 0, time.Since(started))
		http.Error(w, "checkpoint archive bound is invalid", http.StatusBadRequest)
		return
	}
	if s.checkpointPreparedBytes > s.checkpointMaxStaged-stagingBytes {
		s.checkpointMu.Unlock()
		s.metrics.recordCheckpoint("prepare", "unavailable", 0, time.Since(started))
		http.Error(w, "checkpoint staging byte admission rejected", http.StatusTooManyRequests)
		return
	}
	// Reserve before filesystem work. The reservation is released on every
	// failure and tightened to the actual canonical archive size on success.
	s.checkpointPreparedBytes += stagingBytes
	entries, ttl := s.checkpointMaxEntries, s.checkpointPreparedTTL
	exclusions := append([]string(nil), s.checkpointExclusions...)
	s.checkpointMu.Unlock()

	staging, cleanupStaging, err := s.checkpointStagingDirectory()
	if err != nil {
		s.releaseCheckpointReservation(stagingBytes)
		s.metrics.recordCheckpoint("prepare", "failed", 0, time.Since(started))
		http.Error(w, "checkpoint staging unavailable", http.StatusServiceUnavailable)
		return
	}
	archive, err := checkpoint.CaptureArchive(r.Context(), request, checkpoint.ArchiveSource{
		ContainerRoot: filepath.Join(s.storagePath, "steps", request.ContainerHandle),
		ExcludedRoots: exclusions,
		MaxBytes:      maxBytes,
		MaxEntries:    entries,
		TempDir:       staging,
	})
	if err != nil {
		cleanupStaging()
		s.releaseCheckpointReservation(stagingBytes)
		s.metrics.recordCheckpoint("prepare", "failed", 0, time.Since(started))
		http.Error(w, "checkpoint archive capture failed", http.StatusUnprocessableEntity)
		return
	}
	if err := s.storePreparedCheckpoint(archive, stagingBytes, ttl, cleanupStaging); err != nil {
		_ = archive.Close()
		cleanupStaging()
		s.releaseCheckpointReservation(stagingBytes)
		s.metrics.recordCheckpoint("prepare", "unavailable", 0, time.Since(started))
		http.Error(w, "checkpoint staging unavailable", http.StatusTooManyRequests)
		return
	}
	s.metrics.recordCheckpoint("prepare", "ok", archive.Prepared.Bytes, time.Since(started))
	writeCheckpointCaptureJSON(w, http.StatusCreated, archive.Prepared)
}

func (s *Server) handleCheckpointUpload(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	handle := r.PathValue("preparedHandle")
	if !validPreparedCheckpointHandle(handle) {
		s.metrics.recordCheckpoint("upload", "rejected", 0, time.Since(started))
		http.Error(w, "invalid prepared archive handle", http.StatusBadRequest)
		return
	}
	var ticket checkpoint.ObjectUploadTicket
	if err := decodeCheckpointCaptureJSON(w, r, &ticket); err != nil {
		s.metrics.recordCheckpoint("upload", "rejected", 0, time.Since(started))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prepared := s.lookupPreparedCheckpoint(handle)
	if prepared == nil {
		s.metrics.recordCheckpoint("upload", "expired", 0, time.Since(started))
		http.Error(w, "prepared checkpoint archive is unavailable", http.StatusNotFound)
		return
	}
	if err := validateCheckpointTicket(ticket, prepared.archive.Prepared); err != nil {
		s.metrics.recordCheckpoint("upload", "rejected", 0, time.Since(started))
		http.Error(w, "checkpoint upload ticket does not match prepared archive", http.StatusForbidden)
		return
	}
	prepared = s.consumePreparedCheckpoint(handle)
	if prepared == nil {
		s.metrics.recordCheckpoint("upload", "expired", 0, time.Since(started))
		http.Error(w, "prepared checkpoint archive is unavailable", http.StatusNotFound)
		return
	}
	defer func() {
		_ = prepared.archive.Close()
		prepared.cleanup()
	}()

	if ticket.AlreadyAvailable {
		object, err := hangar.NewObjectRef(hangar.KindCheckpoint, ticket.Digest, ticket.AvailableGeneration)
		if err != nil {
			s.metrics.recordCheckpoint("upload", "rejected", 0, time.Since(started))
			http.Error(w, "invalid available checkpoint ticket", http.StatusForbidden)
			return
		}
		s.metrics.recordCheckpoint("upload", "ok", prepared.archive.Prepared.Bytes, time.Since(started))
		writeCheckpointCaptureJSON(w, http.StatusOK, checkpoint.ArchiveResult{Object: hangar.Attributes{Ref: object}, Files: prepared.archive.Prepared.Files, Bytes: prepared.archive.Prepared.Bytes})
		return
	}
	if s.hangar == nil {
		s.metrics.recordCheckpoint("upload", "unavailable", 0, time.Since(started))
		http.Error(w, "durable checkpoint storage unavailable", http.StatusServiceUnavailable)
		return
	}
	archive, err := prepared.archive.Open()
	if err != nil {
		s.metrics.recordCheckpoint("upload", "failed", 0, time.Since(started))
		http.Error(w, "prepared checkpoint archive unavailable", http.StatusConflict)
		return
	}
	attributes, ensureErr := s.hangar.Ensure(r.Context(), hangar.KindCheckpoint, ticket.Digest, archive, prepared.maxBytes)
	closeErr := archive.Close()
	if err := errors.Join(ensureErr, closeErr); err != nil {
		s.metrics.recordCheckpoint("upload", "failed", 0, time.Since(started))
		http.Error(w, "durable checkpoint upload failed", http.StatusServiceUnavailable)
		return
	}
	if attributes.Ref.Kind != hangar.KindCheckpoint || attributes.Ref.Digest != ticket.Digest || attributes.Ref.Key != ticket.Key || attributes.Ref.Generation <= 0 || attributes.UncompressedBytes != prepared.archive.Prepared.Bytes || attributes.CompressedBytes <= 0 {
		s.metrics.recordCheckpoint("upload", "failed", 0, time.Since(started))
		http.Error(w, "durable checkpoint identity mismatch", http.StatusBadGateway)
		return
	}
	s.metrics.recordCheckpoint("upload", "ok", prepared.archive.Prepared.Bytes, time.Since(started))
	writeCheckpointCaptureJSON(w, http.StatusOK, checkpoint.ArchiveResult{Object: attributes, Files: prepared.archive.Prepared.Files, Bytes: prepared.archive.Prepared.Bytes})
}

func (s *Server) storePreparedCheckpoint(archive *checkpoint.CapturedArchive, reserved int64, ttl time.Duration, cleanup func()) error {
	if archive == nil || archive.Prepared.Bytes <= 0 || archive.Prepared.Bytes > reserved {
		return errors.New("invalid checkpoint archive reservation")
	}
	entry := &preparedCheckpointCapture{archive: archive, maxBytes: reserved, cleanup: cleanup}
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	if len(s.checkpointPrepared) >= s.checkpointMaxPrepared || s.checkpointPreparedBytes-reserved > s.checkpointMaxStaged-archive.Prepared.Bytes {
		return errors.New("checkpoint staging full")
	}
	s.checkpointPreparedBytes += archive.Prepared.Bytes - reserved
	s.checkpointPrepared[archive.Prepared.Handle] = entry
	entry.timer = time.AfterFunc(ttl, func() { s.expirePreparedCheckpoint(archive.Prepared.Handle, entry) })
	return nil
}

// checkpointStagingDirectory creates a unique child through the checked daemon
// staging descriptor, then returns that child's stable reserved pathname. The
// public artifact router rejects this namespace, so generic artifact requests
// cannot create or replace capture scratch state.
func (s *Server) checkpointStagingDirectory() (string, func(), error) {
	storage, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		return "", nil, err
	}
	defer storage.Close()
	staging, err := openDaemonStagingAt(storage)
	if err != nil {
		return "", nil, err
	}
	defer staging.Close()
	name, directory, err := randomDirectoryAt(staging, ".checkpoint-capture-")
	if err != nil {
		return "", nil, err
	}
	if err := directory.Close(); err != nil {
		return "", nil, err
	}
	path := filepath.Join(s.storagePath, daemonStagingDirectory, name)
	return path, func() { _ = os.RemoveAll(path) }, nil
}

func (s *Server) lookupPreparedCheckpoint(handle string) *preparedCheckpointCapture {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	return s.checkpointPrepared[handle]
}
func (s *Server) consumePreparedCheckpoint(handle string) *preparedCheckpointCapture {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	entry := s.checkpointPrepared[handle]
	if entry == nil {
		return nil
	}
	delete(s.checkpointPrepared, handle)
	if entry.timer != nil {
		entry.timer.Stop()
	}
	s.checkpointPreparedBytes -= entry.archive.Prepared.Bytes
	return entry
}
func (s *Server) expirePreparedCheckpoint(handle string, expected *preparedCheckpointCapture) {
	s.checkpointMu.Lock()
	entry := s.checkpointPrepared[handle]
	if entry == expected {
		delete(s.checkpointPrepared, handle)
		s.checkpointPreparedBytes -= entry.archive.Prepared.Bytes
	}
	s.checkpointMu.Unlock()
	if entry == expected {
		_ = entry.archive.Close()
		entry.cleanup()
	}
}
func (s *Server) releaseCheckpointReservation(bytes int64) {
	s.checkpointMu.Lock()
	s.checkpointPreparedBytes -= bytes
	s.checkpointMu.Unlock()
}

func validateCheckpointTicket(ticket checkpoint.ObjectUploadTicket, prepared checkpoint.PreparedArchive) error {
	if ticket.ObjectID <= 0 || ticket.StagedCheckpointID <= 0 || ticket.Kind != hangar.KindCheckpoint || ticket.Digest != prepared.Digest {
		return errors.New("invalid checkpoint upload ticket")
	}
	key, err := hangar.Key(hangar.KindCheckpoint, ticket.Digest)
	if err != nil || ticket.Key != key {
		return errors.New("checkpoint upload key mismatch")
	}
	if ticket.AlreadyAvailable {
		if ticket.AvailableGeneration <= 0 || ticket.UploadToken != "" {
			return errors.New("checkpoint available generation is invalid")
		}
		return nil
	}
	if strings.TrimSpace(ticket.UploadToken) == "" || ticket.AvailableGeneration != 0 {
		return errors.New("checkpoint upload token is required")
	}
	return nil
}

func decodeCheckpointCaptureJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	if r.ContentLength > checkpointCaptureRequestLimit {
		return errors.New("checkpoint request body is too large")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkpointCaptureRequestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("malformed checkpoint request")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("checkpoint request contains trailing data")
	}
	return nil
}

func writeCheckpointCaptureJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func validPreparedCheckpointHandle(handle string) bool {
	return strings.HasPrefix(handle, "prepared-") && len(handle) == len("prepared-")+48 && checkpoint.PreparedArchive{Handle: handle, Digest: hangar.Digest("sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"), Files: 1, Bytes: 1}.Validate() == nil
}
func minCheckpointCapture(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
