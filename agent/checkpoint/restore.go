package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
)

const restoreReceiptPrefix = "checkpoint-restore-v1:"

// RestoreRequest carries only server-derived recovery authority. In
// particular, it contains no host destination path, prior pod identity, or
// node-local source path.
type RestoreRequest struct {
	ContainerHandle   string   `json:"container_handle"`
	MaterializationID string   `json:"materialization_id"`
	PodUID            string   `json:"pod_uid"`
	Archive           Archive  `json:"archive"`
	WorkspaceRoots    []string `json:"workspace_roots"`
	SessionRoots      []string `json:"session_roots"`
	MaxBytes          int64    `json:"max_bytes"`
	MaxEntries        int64    `json:"max_entries"`
}

func (request RestoreRequest) Clone() RestoreRequest {
	cloned := request
	cloned.WorkspaceRoots = append([]string(nil), request.WorkspaceRoots...)
	cloned.SessionRoots = append([]string(nil), request.SessionRoots...)
	return cloned
}

// RestoreReceiptSeed is the descriptor-bound portion of a gate receipt. The
// Pod UID is intentionally omitted because kubelet assigns it after Pod
// construction; the init gate appends its downward-API UID before comparing.
func RestoreReceiptSeed(request RestoreRequest) (string, error) {
	request.PodUID = "receipt"
	if err := request.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return restoreReceiptPrefix + hex.EncodeToString(sum[:]), nil
}

// RestoreReceipt is the fixed-format gate acknowledgement for one exact
// server-derived restore request. It is deliberately independent of the
// private daemon marker: the Pod gate needs only this opaque value and cannot
// accidentally accept a syntactically similar marker envelope.
func RestoreReceipt(request RestoreRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	seed, err := RestoreReceiptSeed(request)
	if err != nil {
		return "", err
	}
	return seed + ":" + request.PodUID, nil
}

func (request RestoreRequest) Validate() error {
	if !validCaptureHandle(request.ContainerHandle) || !validRestoreIdentifier(request.MaterializationID) || !validRestoreIdentifier(request.PodUID) || request.MaxBytes <= 0 || request.MaxEntries <= 0 {
		return errors.New("checkpoint: invalid restore request")
	}
	if err := request.Archive.Validate(); err != nil {
		return err
	}
	if err := validateCaptureRoots("workspace", request.WorkspaceRoots); err != nil {
		return err
	}
	if err := validateCaptureRoots("session", request.SessionRoots); err != nil {
		return err
	}
	if !sort.StringsAreSorted(request.WorkspaceRoots) || !sort.StringsAreSorted(request.SessionRoots) {
		return errors.New("checkpoint: restore roots must be sorted")
	}
	for _, workspace := range request.WorkspaceRoots {
		for _, session := range request.SessionRoots {
			if captureRootsOverlap(workspace, session) {
				return errors.New("checkpoint: workspace and session roots overlap")
			}
		}
	}
	return nil
}

func validRestoreIdentifier(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// RestoreGateLeafName is the opaque, deterministic materialization leaf used
// by both Pod construction and the daemon's descriptor-anchored marker store.
// It intentionally exposes no source path or pod identity.
func RestoreGateLeafName(materializationID string) string {
	sum := sha256.Sum256([]byte(materializationID))
	return hex.EncodeToString(sum[:])
}

// ValidRestoreMaterializationID exposes the narrow identifier validation used
// by server-owned recovery descriptors without exposing filesystem authority.
func ValidRestoreMaterializationID(value string) bool {
	return validRestoreIdentifier(value)
}

// RestoreResult is the daemon's bounded acknowledgement. The client verifies
// every field against the request before Jetbridge can release an init gate.
type RestoreResult struct {
	Object            hangar.Attributes `json:"object"`
	MaterializationID string            `json:"materialization_id"`
	PodUID            string            `json:"pod_uid"`
}

func (result RestoreResult) ValidateFor(request RestoreRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.MaterializationID != request.MaterializationID || result.PodUID != request.PodUID || result.Object.Ref != request.Archive.Ref {
		return errors.New("checkpoint: restore result does not match request")
	}
	archiveByteLimit, err := snapshot.CanonicalArchiveByteLimit(request.MaxBytes, request.MaxEntries)
	if err != nil {
		return fmt.Errorf("checkpoint: derive restore archive limit: %w", err)
	}
	if result.Object.CompressedBytes <= 0 || result.Object.UncompressedBytes <= 0 || result.Object.UncompressedBytes > archiveByteLimit {
		return fmt.Errorf("checkpoint: restore result accounting is invalid")
	}
	return nil
}
