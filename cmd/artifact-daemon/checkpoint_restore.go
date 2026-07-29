package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
	"golang.org/x/sys/unix"
)

const checkpointRestoreGatesDirectory = ".checkpoint-restore-gates"

type checkpointRestoreMarker struct {
	MaterializationID string            `json:"materialization_id"`
	RequestHash       string            `json:"request_hash"`
	Object            hangar.Attributes `json:"object"`
	PodUID            string            `json:"pod_uid"`
}

func (s *Server) handleCheckpointRestore(w http.ResponseWriter, r *http.Request) {
	var request checkpoint.RestoreRequest
	if err := decodeCheckpointCaptureJSON(w, r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := request.Validate(); err != nil {
		http.Error(w, "invalid checkpoint restore request", http.StatusBadRequest)
		return
	}
	result, err := s.restoreCheckpoint(r.Context(), request)
	if err != nil {
		http.Error(w, "checkpoint restore failed", http.StatusUnprocessableEntity)
		return
	}
	writeCheckpointCaptureJSON(w, http.StatusOK, result)
}

func (s *Server) restoreCheckpoint(ctx context.Context, request checkpoint.RestoreRequest) (checkpoint.RestoreResult, error) {
	if s.hangar == nil {
		return checkpoint.RestoreResult{}, errors.New("durable checkpoint storage unavailable")
	}
	s.checkpointMu.Lock()
	maxBytes, maxEntries := minCheckpointCapture(request.MaxBytes, s.checkpointMaxBytes), minCheckpointCapture(request.MaxEntries, s.checkpointMaxEntries)
	s.checkpointMu.Unlock()
	if maxBytes <= 0 || maxEntries <= 0 {
		return checkpoint.RestoreResult{}, errors.New("checkpoint restore limits are unavailable")
	}
	archiveByteLimit, err := snapshot.CanonicalArchiveByteLimit(maxBytes, maxEntries)
	if err != nil {
		return checkpoint.RestoreResult{}, fmt.Errorf("derive checkpoint archive byte limit: %w", err)
	}
	request.MaxBytes, request.MaxEntries = maxBytes, maxEntries
	requestHash, err := checkpointRestoreRequestHash(request)
	if err != nil {
		return checkpoint.RestoreResult{}, err
	}

	s.checkpointRestoreMu.Lock()
	defer s.checkpointRestoreMu.Unlock()
	if err := s.requireCheckpointRestoreGateLeaf(request.ContainerHandle, request.MaterializationID); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	if marker, found, err := s.readCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID); err != nil {
		return checkpoint.RestoreResult{}, err
	} else if found {
		if marker.MaterializationID != request.MaterializationID || marker.RequestHash != requestHash || marker.PodUID != request.PodUID || marker.Object.Ref != request.Archive.Ref {
			return checkpoint.RestoreResult{}, errors.New("checkpoint restore marker does not match request")
		}
		result := checkpoint.RestoreResult{Object: marker.Object, MaterializationID: request.MaterializationID, PodUID: request.PodUID}
		return result, result.ValidateFor(request)
	}

	reader, attributes, err := s.hangar.Open(ctx, request.Archive.Ref, archiveByteLimit)
	if err != nil {
		return checkpoint.RestoreResult{}, err
	}
	defer reader.Close()
	if attributes.Ref != request.Archive.Ref || attributes.CompressedBytes <= 0 || attributes.UncompressedBytes <= 0 || attributes.UncompressedBytes > archiveByteLimit {
		return checkpoint.RestoreResult{}, errors.New("checkpoint archive attributes do not match exact bounded reference")
	}
	staging, cleanup, err := s.checkpointStagingDirectory()
	if err != nil {
		return checkpoint.RestoreResult{}, err
	}
	defer cleanup()
	tree, err := (snapshot.Canonicalizer{MaxEntries: maxEntries, MaxContentBytes: maxBytes, TempDir: staging}).Capture(ctx, reader)
	if err != nil {
		return checkpoint.RestoreResult{}, fmt.Errorf("canonicalize checkpoint archive: %w", err)
	}
	defer tree.Close()
	if hangar.Digest(tree.Digest) != request.Archive.Ref.Digest || tree.ByteSize != attributes.UncompressedBytes {
		return checkpoint.RestoreResult{}, errors.New("checkpoint archive canonical digest or byte accounting mismatch")
	}
	if err := validateCheckpointRestoreTopology(tree.Root, request); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	if err := s.copyCheckpointRestore(ctx, tree.Root, request); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	marker := checkpointRestoreMarker{MaterializationID: request.MaterializationID, RequestHash: requestHash, Object: attributes, PodUID: request.PodUID}
	if err := s.writeCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID, marker); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	result := checkpoint.RestoreResult{Object: attributes, MaterializationID: request.MaterializationID, PodUID: request.PodUID}
	return result, result.ValidateFor(request)
}

func checkpointRestoreRequestHash(request checkpoint.RestoreRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateCheckpointRestoreTopology(root string, request checkpoint.RestoreRequest) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 2 || entries[0].Name() != "session" || entries[1].Name() != "workspace" {
		return errors.New("checkpoint archive must contain exactly workspace and session roots")
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("checkpoint archive root is not a real directory")
		}
	}
	for _, section := range []struct {
		name  string
		roots []string
	}{{"workspace", request.WorkspaceRoots}, {"session", request.SessionRoots}} {
		sectionPath := filepath.Join(root, section.name)
		if err := rejectCheckpointRestoreTypes(sectionPath); err != nil {
			return err
		}
		if len(section.roots) > 1 {
			expectedChildren := make(map[string]struct{})
			for _, declared := range section.roots {
				expectedChildren[strings.Split(declared, "/")[0]] = struct{}{}
				info, err := os.Stat(filepath.Join(sectionPath, filepath.FromSlash(declared)))
				if err != nil || !info.IsDir() {
					return fmt.Errorf("checkpoint archive is missing declared %s root %q", section.name, declared)
				}
			}
			children, err := os.ReadDir(sectionPath)
			if err != nil || len(children) != len(expectedChildren) {
				return fmt.Errorf("checkpoint archive has undeclared %s roots", section.name)
			}
			for _, child := range children {
				if _, found := expectedChildren[child.Name()]; !found {
					return fmt.Errorf("checkpoint archive has undeclared %s root %q", section.name, child.Name())
				}
			}
		}
	}
	return nil
}

func rejectCheckpointRestoreTypes(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("checkpoint archive contains unsupported entry %q", path)
		}
		return nil
	})
}

func (s *Server) copyCheckpointRestore(ctx context.Context, source string, request checkpoint.RestoreRequest) error {
	release, err := s.guard.BeginSweepContext(ctx, request.ContainerHandle)
	if err != nil {
		return err
	}
	defer release()
	storage, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		return err
	}
	defer storage.Close()
	steps, err := openDirAtNoFollow(storage, "steps", false)
	if err != nil {
		return err
	}
	defer steps.Close()
	destination, err := openDirAtNoFollow(steps, request.ContainerHandle, false)
	if err != nil {
		return err
	}
	defer destination.Close()
	sourceRoot, err := openDirectoryNoFollow(source)
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	for _, section := range []struct {
		name  string
		roots []string
	}{{"workspace", request.WorkspaceRoots}, {"session", request.SessionRoots}} {
		sectionSource, err := openDirAtNoFollow(sourceRoot, section.name, false)
		if err != nil {
			return err
		}
		for _, root := range section.roots {
			sourceDirectory := sectionSource
			if len(section.roots) > 1 {
				sourceDirectory, err = openDirAtNoFollow(sectionSource, root, false)
				if err != nil {
					sectionSource.Close()
					return err
				}
			}
			destinationDirectory, openErr := openDirAtNoFollow(destination, root, false)
			if openErr == nil {
				openErr = removeOpenedDirectoryContentsContext(ctx, destinationDirectory)
			}
			if openErr == nil {
				openErr = copyOpenedCheckpointTree(ctx, sourceDirectory, destinationDirectory, "")
			}
			if section.name == "session" && openErr == nil {
				openErr = s.normalizeCheckpointSession(destinationDirectory)
			}
			var closeErr error
			if destinationDirectory != nil {
				closeErr = destinationDirectory.Close()
			}
			if len(section.roots) > 1 {
				closeErr = errors.Join(closeErr, sourceDirectory.Close())
			}
			if openErr != nil || closeErr != nil {
				sectionSource.Close()
				return errors.Join(openErr, closeErr)
			}
		}
		if err := sectionSource.Close(); err != nil {
			return err
		}
	}
	return unix.Fsync(int(destination.Fd()))
}

func copyOpenedCheckpointTree(ctx context.Context, source, destination *os.File, prefix string) error {
	entries, err := source.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return errors.New("unsafe checkpoint entry name")
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(source.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		entryPath := filepath.Join(prefix, name)
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := unix.Mkdirat(int(destination.Fd()), name, uint32(stat.Mode&0777)); err != nil {
				return err
			}
			sourceChild, err := openDirAtNoFollow(source, name, false)
			if err != nil {
				return err
			}
			destinationChild, err := openDirAtNoFollow(destination, name, false)
			if err != nil {
				sourceChild.Close()
				return err
			}
			copyErr := copyOpenedCheckpointTree(ctx, sourceChild, destinationChild, entryPath)
			syncErr := unix.Fsync(int(destinationChild.Fd()))
			closeErr := errors.Join(sourceChild.Close(), destinationChild.Close())
			if copyErr != nil || syncErr != nil || closeErr != nil {
				return errors.Join(copyErr, syncErr, closeErr)
			}
		case unix.S_IFREG:
			sourceFD, err := unix.Openat(int(source.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			destinationFD, err := unix.Openat(int(destination.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(stat.Mode&0777))
			if err != nil {
				unix.Close(sourceFD)
				return err
			}
			sourceFile, destinationFile := os.NewFile(uintptr(sourceFD), entryPath), os.NewFile(uintptr(destinationFD), entryPath)
			copyErr := copyOpenedFileContext(ctx, destinationFile, sourceFile)
			syncErr := destinationFile.Sync()
			closeErr := errors.Join(sourceFile.Close(), destinationFile.Close())
			if copyErr != nil || syncErr != nil || closeErr != nil {
				return errors.Join(copyErr, syncErr, closeErr)
			}
		default:
			return fmt.Errorf("checkpoint archive entry %q is not a regular file or directory", entryPath)
		}
	}
	return nil
}

func normalizeCheckpointSessionTree(directory *os.File) error {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &rootStat); err != nil {
		return err
	}
	if err := unix.Fchown(int(directory.Fd()), -1, 65534); err != nil {
		return err
	}
	if err := unix.Fchmod(int(directory.Fd()), uint32(rootStat.Mode&0777)|0070); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		var stat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
			return errors.New("checkpoint session contains symlink")
		}
		if err := unix.Fchownat(int(directory.Fd()), name, -1, 65534, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		mode := checkpointSessionMode(uint32(stat.Mode&0777), stat.Mode&unix.S_IFMT == unix.S_IFDIR)
		if err := unix.Fchmodat(int(directory.Fd()), name, mode, 0); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			child, err := openDirAtNoFollow(directory, name, false)
			if err != nil {
				return err
			}
			err = normalizeCheckpointSessionTree(child)
			closeErr := child.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
	}
	return nil
}

// checkpointSessionMode applies g+rwX to restored session content without
// granting group execute to ordinary non-executable files.
func checkpointSessionMode(mode uint32, directory bool) uint32 {
	if directory || mode&0111 != 0 {
		return mode | 0070
	}
	return mode | 0060
}

func (s *Server) checkpointRestoreGates() (*os.File, error) {
	storage, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	if err := unix.Mkdirat(int(storage.Fd()), checkpointRestoreGatesDirectory, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	return openDirAtNoFollow(storage, checkpointRestoreGatesDirectory, false)
}

// requireCheckpointRestoreGateLeaf proves the scheduled Pod has already
// created and mounted its exact private gate directory. The daemon may create
// the common daemon-owned namespace, but never a handle or materialization
// leaf: doing so would let an arbitrary request manufacture launch authority.
func (s *Server) requireCheckpointRestoreGateLeaf(containerHandle, materializationID string) error {
	gates, err := s.checkpointRestoreGates()
	if err != nil {
		return err
	}
	defer gates.Close()
	leaf, err := checkpointRestoreMarkerDirectory(gates, containerHandle, materializationID)
	if err != nil {
		return fmt.Errorf("checkpoint restore gate leaf is not pre-created: %w", err)
	}
	return leaf.Close()
}

func checkpointRestoreMarkerName(materializationID string) string {
	sum := sha256.Sum256([]byte(materializationID))
	return hex.EncodeToString(sum[:])
}

func checkpointRestoreMarkerDirectory(gates *os.File, containerHandle, materializationID string) (*os.File, error) {
	handle, err := openDirAtNoFollow(gates, containerHandle, false)
	if err != nil {
		return nil, err
	}
	leaf, err := openDirAtNoFollow(handle, checkpointRestoreMarkerName(materializationID), false)
	closeErr := handle.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	return leaf, nil
}

func (s *Server) readCheckpointRestoreMarker(containerHandle, materializationID string) (checkpointRestoreMarker, bool, error) {
	gates, err := s.checkpointRestoreGates()
	if err != nil {
		return checkpointRestoreMarker{}, false, err
	}
	defer gates.Close()
	leaf, err := checkpointRestoreMarkerDirectory(gates, containerHandle, materializationID)
	if errors.Is(err, unix.ENOENT) {
		return checkpointRestoreMarker{}, false, nil
	}
	if err != nil {
		return checkpointRestoreMarker{}, false, err
	}
	defer leaf.Close()
	fd, err := unix.Openat(int(leaf.Fd()), "ready", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return checkpointRestoreMarker{}, false, nil
	}
	if err != nil {
		return checkpointRestoreMarker{}, false, err
	}
	file := os.NewFile(uintptr(fd), "ready")
	raw, readErr := io.ReadAll(io.LimitReader(file, checkpointCaptureRequestLimit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return checkpointRestoreMarker{}, false, errors.Join(readErr, closeErr)
	}
	if len(raw) > checkpointCaptureRequestLimit {
		return checkpointRestoreMarker{}, false, errors.New("checkpoint restore marker exceeds size limit")
	}
	var marker checkpointRestoreMarker
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return checkpointRestoreMarker{}, false, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF || marker.MaterializationID == "" || marker.RequestHash == "" || marker.PodUID == "" {
		return checkpointRestoreMarker{}, false, errors.New("invalid checkpoint restore marker")
	}
	return marker, true, nil
}

func (s *Server) writeCheckpointRestoreMarker(containerHandle, materializationID string, marker checkpointRestoreMarker) error {
	gates, err := s.checkpointRestoreGates()
	if err != nil {
		return err
	}
	defer gates.Close()
	leaf, err := checkpointRestoreMarkerDirectory(gates, containerHandle, materializationID)
	if err != nil {
		return err
	}
	defer leaf.Close()
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return writeExclusiveFileAt(leaf, "ready", raw, 0600)
}
