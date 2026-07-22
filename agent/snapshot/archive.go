package snapshot

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxSnapshotEntries      int64 = 100_000
	DefaultMaxSnapshotContentBytes int64 = 10 << 30
)

// Canonicalizer safely materializes a tar stream and emits its deterministic
// filesystem-tree representation. Zero limits select the secure defaults.
//
// Snapshot paths form a bytewise POSIX namespace. Snapshot storage execution
// is supported on Linux and Darwin; Windows is not a storage execution target.
// Host filesystems that alias distinct POSIX names fail closed at extraction.
type Canonicalizer struct {
	MaxEntries      int64
	MaxContentBytes int64

	// TempDir selects the parent of the private capture directory. It is useful
	// for constraining storage and observing cleanup; an empty value uses the
	// operating system's default temporary directory.
	TempDir string

	removeAll             func(string) error
	beforeMaterialize     func(*os.Root, string) error
	beforeCanonicalOpen   func(*os.Root, string) error
	beforePreEmitVerify   func(*os.Root) error
	beforePostEmitVerify  func(*os.Root) error
	beforeCaptureBoundary func(*os.Root, string) error
	beforeAnchoredCleanup func(*os.Root) error
}

// CapturedTree owns both its extracted root and canonical archive. Call Close
// when neither is needed; Close is safe to call repeatedly. Capture verifies
// the returned object paths at its success boundary. Same-UID mutation of those
// paths after Capture returns is outside the object-path API guarantee; Close
// still wipes the originally captured directory through its anchored handle.
type CapturedTree struct {
	Root        string
	ArchivePath string
	Digest      Digest
	ByteSize    int64
	FileCount   int64

	privateRoot string
	closeMu     sync.Mutex
	closed      bool
	rootedWiped bool
	captureRoot *os.Root
	removeAll   func(string) error
}

func (t *CapturedTree) Close() error {
	if t == nil {
		return nil
	}
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed || t.privateRoot == "" {
		return nil
	}
	if !t.rootedWiped && t.captureRoot != nil {
		if err := wipeCaptureRoot(t.captureRoot); err != nil {
			return err
		}
		t.rootedWiped = true
	}
	if t.captureRoot != nil {
		if err := t.captureRoot.Close(); err != nil {
			return err
		}
		t.captureRoot = nil
	}
	removeAll := t.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	if err := removeAll(t.privateRoot); err != nil {
		return err
	}
	t.closed = true
	return nil
}

// Capture extracts rawTar into a private directory, emits a canonical tar next
// to it, and hashes the exact emitted bytes. No caller-visible ownership is
// transferred unless all extraction and canonicalization steps succeed.
// Cancellation between Read calls is cooperative for an arbitrary io.Reader.
// When rawTar is an io.ReadCloser, cancellation closes it to unblock Read; an
// ordinary successful capture does not close caller-owned input.
func (c Canonicalizer) Capture(ctx context.Context, rawTar io.Reader) (tree *CapturedTree, err error) {
	if rawTar == nil {
		return nil, fmt.Errorf("snapshot: tar reader is required")
	}
	maxEntries, maxContent, err := c.limits()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateConfiguredTempDir(c.TempDir); err != nil {
		return nil, err
	}

	privateRoot, err := os.MkdirTemp(c.TempDir, "concourse-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("snapshot: create private capture directory: %w", err)
	}
	removeAll := c.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	var captureRoot *os.Root
	var extractionRoot *os.Root
	var spool *os.File
	var archive *os.File
	defer func() {
		if tree != nil && err == nil {
			return
		}
		tree = nil
		if archive != nil {
			err = errors.Join(err, archive.Close())
		}
		if spool != nil {
			err = errors.Join(err, spool.Close())
		}
		if extractionRoot != nil {
			err = errors.Join(err, extractionRoot.Close())
		}
		if captureRoot != nil {
			if c.beforeAnchoredCleanup != nil {
				err = errors.Join(err, c.beforeAnchoredCleanup(captureRoot))
			}
			err = errors.Join(err, wipeCaptureRoot(captureRoot), captureRoot.Close())
		}
		err = errors.Join(err, removeAll(privateRoot))
	}()
	stopCancelClose := closeReadCloserOnCancel(ctx, rawTar)
	defer stopCancelClose()

	captureRoot, err = os.OpenRoot(privateRoot)
	if err != nil {
		return nil, fmt.Errorf("snapshot: anchor private capture directory: %w", err)
	}
	captureInfo, err := captureRoot.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("snapshot: stat private capture directory: %w", err)
	}
	if err := captureRoot.Mkdir("root", 0700); err != nil {
		return nil, fmt.Errorf("snapshot: create extraction root: %w", err)
	}
	extractionRoot, err = captureRoot.OpenRoot("root")
	if err != nil {
		return nil, fmt.Errorf("snapshot: open extraction root: %w", err)
	}
	extractionInfo, err := extractionRoot.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("snapshot: stat extraction root: %w", err)
	}
	spool, err = captureRoot.OpenFile("content.spool", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("snapshot: create content spool: %w", err)
	}
	if err := spool.Chmod(0600); err != nil {
		return nil, fmt.Errorf("snapshot: normalize content spool: %w", err)
	}
	index, err := extractTar(ctx, rawTar, extractionRoot, spool, maxEntries, maxContent, c.beforeMaterialize)
	if err != nil {
		return nil, err
	}
	if c.beforePreEmitVerify != nil {
		if err := c.beforePreEmitVerify(extractionRoot); err != nil {
			return nil, fmt.Errorf("snapshot: before pre-emission verification: %w", err)
		}
	}
	if err := verifyCaptureIndex(ctx, extractionRoot, index, maxEntries); err != nil {
		return nil, fmt.Errorf("snapshot: verify captured tree before emission: %w", err)
	}

	archive, err = captureRoot.OpenFile("canonical.tar", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("snapshot: create canonical archive: %w", err)
	}
	if err := archive.Chmod(0600); err != nil {
		return nil, fmt.Errorf("snapshot: normalize canonical archive: %w", err)
	}
	digest, byteSize, err := writeCanonicalTar(ctx, archive, spool, extractionRoot, index, c.beforeCanonicalOpen)
	if err != nil {
		return nil, err
	}
	archiveInfo, err := archive.Stat()
	if err != nil {
		return nil, fmt.Errorf("snapshot: stat canonical archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("snapshot: close canonical archive: %w", err)
	}
	archive = nil
	if c.beforePostEmitVerify != nil {
		if err := c.beforePostEmitVerify(extractionRoot); err != nil {
			return nil, fmt.Errorf("snapshot: before post-emission verification: %w", err)
		}
	}
	if err := verifyCaptureIndex(ctx, extractionRoot, index, maxEntries); err != nil {
		return nil, fmt.Errorf("snapshot: verify captured tree after emission: %w", err)
	}
	if err := spool.Close(); err != nil {
		return nil, fmt.Errorf("snapshot: close content spool: %w", err)
	}
	spool = nil
	if err := captureRoot.Remove("content.spool"); err != nil {
		return nil, fmt.Errorf("snapshot: remove content spool: %w", err)
	}
	if c.beforeCaptureBoundary != nil {
		if err := c.beforeCaptureBoundary(captureRoot, privateRoot); err != nil {
			return nil, fmt.Errorf("snapshot: before capture boundary verification: %w", err)
		}
	}
	if err := verifyCaptureBoundary(captureRoot, captureInfo, extractionRoot, extractionInfo, archiveInfo, privateRoot); err != nil {
		return nil, err
	}
	if err := verifyCaptureIndex(ctx, extractionRoot, index, maxEntries); err != nil {
		return nil, fmt.Errorf("snapshot: verify captured tree at success boundary: %w", err)
	}
	if err := extractionRoot.Close(); err != nil {
		return nil, fmt.Errorf("snapshot: close extraction root: %w", err)
	}
	extractionRoot = nil

	rootPath := filepath.Join(privateRoot, "root")
	archivePath := filepath.Join(privateRoot, "canonical.tar")
	tree = &CapturedTree{
		Root:        rootPath,
		ArchivePath: archivePath,
		Digest:      digest,
		ByteSize:    byteSize,
		FileCount:   int64(len(index.entries)),
		privateRoot: privateRoot,
		captureRoot: captureRoot,
		removeAll:   removeAll,
	}
	captureRoot = nil
	return tree, nil
}

func closeReadCloserOnCancel(ctx context.Context, reader io.Reader) func() {
	closer, ok := reader.(io.ReadCloser)
	if !ok {
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = closer.Close()
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (c Canonicalizer) limits() (int64, int64, error) {
	maxEntries := c.MaxEntries
	if maxEntries == 0 {
		maxEntries = DefaultMaxSnapshotEntries
	}
	if maxEntries < 0 {
		return 0, 0, fmt.Errorf("snapshot: maximum entries must be positive")
	}
	maxContent := c.MaxContentBytes
	if maxContent == 0 {
		maxContent = DefaultMaxSnapshotContentBytes
	}
	if maxContent < 0 {
		return 0, 0, fmt.Errorf("snapshot: maximum content bytes must be positive")
	}
	return maxEntries, maxContent, nil
}

func validateConfiguredTempDir(tempDir string) error {
	if tempDir == "" {
		return nil
	}
	info, err := os.Stat(tempDir)
	if err != nil {
		return fmt.Errorf("snapshot: inspect trusted temporary parent %q: %w", tempDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("snapshot: trusted temporary parent %q is not a directory", tempDir)
	}
	if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("snapshot: trusted temporary parent %q is group- or other-writable without the sticky bit", tempDir)
	}
	return nil
}

func wipeCaptureRoot(root *os.Root) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("snapshot: list anchored capture directory for cleanup: %w", err)
	}
	var cleanupErr error
	for _, entry := range entries {
		if err := root.RemoveAll(entry.Name()); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("snapshot: remove anchored capture path %q: %w", entry.Name(), err))
		}
	}
	return cleanupErr
}

func verifyCaptureBoundary(
	captureRoot *os.Root,
	captureInfo fs.FileInfo,
	extractionRoot *os.Root,
	extractionInfo fs.FileInfo,
	archiveInfo fs.FileInfo,
	privateRoot string,
) error {
	anchoredCaptureInfo, err := captureRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("snapshot: verify capture path identity through anchored handle: %w", err)
	}
	pathCaptureInfo, err := os.Lstat(privateRoot)
	if err != nil {
		return fmt.Errorf("snapshot: verify capture path identity at %q: %w", privateRoot, err)
	}
	if !os.SameFile(captureInfo, anchoredCaptureInfo) || !os.SameFile(captureInfo, pathCaptureInfo) {
		return fmt.Errorf("snapshot: capture path identity changed before success")
	}

	anchoredExtractionInfo, err := extractionRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("snapshot: verify extraction path identity through anchored handle: %w", err)
	}
	captureExtractionInfo, err := captureRoot.Lstat("root")
	if err != nil {
		return fmt.Errorf("snapshot: verify extraction path identity through capture root: %w", err)
	}
	pathExtractionInfo, err := os.Lstat(filepath.Join(privateRoot, "root"))
	if err != nil {
		return fmt.Errorf("snapshot: verify extraction path identity at object path: %w", err)
	}
	if !os.SameFile(extractionInfo, anchoredExtractionInfo) ||
		!os.SameFile(extractionInfo, captureExtractionInfo) ||
		!os.SameFile(extractionInfo, pathExtractionInfo) {
		return fmt.Errorf("snapshot: extraction path identity changed before success")
	}

	anchoredArchiveInfo, err := captureRoot.Lstat("canonical.tar")
	if err != nil {
		return fmt.Errorf("snapshot: verify archive path identity through capture root: %w", err)
	}
	pathArchiveInfo, err := os.Lstat(filepath.Join(privateRoot, "canonical.tar"))
	if err != nil {
		return fmt.Errorf("snapshot: verify archive path identity at object path: %w", err)
	}
	if !os.SameFile(archiveInfo, anchoredArchiveInfo) ||
		!os.SameFile(archiveInfo, pathArchiveInfo) ||
		archiveInfo.Size() != anchoredArchiveInfo.Size() ||
		archiveInfo.Size() != pathArchiveInfo.Size() {
		return fmt.Errorf("snapshot: archive path identity changed before success")
	}
	return nil
}

type extractedKind uint8

const (
	extractedRegular extractedKind = iota + 1
	extractedDirectory
	extractedSymlink
)

type capturedEntry struct {
	name        string
	kind        extractedKind
	mode        int64
	target      string
	size        int64
	spoolOffset int64
	info        fs.FileInfo
}

type captureIndex struct {
	entries   map[string]capturedEntry
	spoolSize int64
}

func extractTar(
	ctx context.Context,
	rawTar io.Reader,
	root *os.Root,
	spool *os.File,
	maxEntries int64,
	maxContent int64,
	beforeMaterialize func(*os.Root, string) error,
) (*captureIndex, error) {
	tr := tar.NewReader(contextReader{ctx: ctx, reader: rawTar})
	seenHeaders := make(map[string]struct{})
	index := &captureIndex{entries: make(map[string]capturedEntry)}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return index, nil
		}
		if err != nil {
			return nil, fmt.Errorf("snapshot: read tar header: %w", err)
		}
		if err := validateHeader(hdr); err != nil {
			return nil, err
		}
		name := hdr.Name
		if _, exists := seenHeaders[name]; exists {
			return nil, fmt.Errorf("snapshot: duplicate canonical path %q", name)
		}
		seenHeaders[name] = struct{}{}
		kind := headerKind(hdr.Typeflag)
		planned, err := planMaterialization(name, kind, index.entries)
		if err != nil {
			return nil, err
		}
		if int64(len(index.entries))+int64(len(planned)) > maxEntries {
			return nil, fmt.Errorf("snapshot: archive exceeds entry limit of %d", maxEntries)
		}
		for _, plannedEntry := range planned {
			if plannedEntry.name == name {
				continue
			}
			entry, err := materializeDirectory(root, plannedEntry.name, beforeMaterialize)
			if err != nil {
				return nil, fmt.Errorf("snapshot: extract implicit directory %q: %w", plannedEntry.name, err)
			}
			index.entries[entry.name] = entry
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			entry, err := extractRegular(ctx, tr, root, spool, hdr, maxContent, index.spoolSize, beforeMaterialize)
			if err != nil {
				return nil, err
			}
			index.entries[name] = entry
			index.spoolSize += entry.size
		case tar.TypeDir:
			isNew := isPlanned(planned, name)
			if isNew {
				entry, err := materializeDirectory(root, name, beforeMaterialize)
				if err != nil {
					return nil, fmt.Errorf("snapshot: extract directory %q: %w", name, err)
				}
				index.entries[name] = entry
			} else {
				info, err := normalizeExistingDirectory(root, name)
				if err != nil {
					return nil, fmt.Errorf("snapshot: extract directory %q: %w", name, err)
				}
				captured := index.entries[name]
				if !os.SameFile(captured.info, info) {
					return nil, fmt.Errorf("snapshot: implicit directory %q changed before explicit header", name)
				}
			}
		case tar.TypeSymlink:
			target, err := cleanSymlinkTarget(name, hdr.Linkname)
			if err != nil {
				return nil, err
			}
			if beforeMaterialize != nil {
				if err := beforeMaterialize(root, name); err != nil {
					return nil, fmt.Errorf("snapshot: before materializing %q: %w", name, err)
				}
			}
			if err := validateHostParents(root, name); err != nil {
				return nil, err
			}
			if err := ensureUnmaterialized(root, name); err != nil {
				return nil, err
			}
			if err := root.Symlink(target, name); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return nil, hostEquivalentCollision(name, err)
				}
				return nil, fmt.Errorf("snapshot: create symlink %q: %w", name, err)
			}
			info, err := root.Lstat(name)
			if err != nil {
				return nil, fmt.Errorf("snapshot: stat symlink %q: %w", name, err)
			}
			index.entries[name] = capturedEntry{
				name: name, kind: extractedSymlink, mode: 0777, target: target, info: info,
			}
		}
	}
}

func isPlanned(planned []materialization, name string) bool {
	for _, entry := range planned {
		if entry.name == name {
			return true
		}
	}
	return false
}

type materialization struct {
	name string
}

func headerKind(typeflag byte) extractedKind {
	switch typeflag {
	case tar.TypeReg, tar.TypeRegA:
		return extractedRegular
	case tar.TypeDir:
		return extractedDirectory
	case tar.TypeSymlink:
		return extractedSymlink
	default:
		panic("validated tar type is unsupported")
	}
}

func planMaterialization(name string, kind extractedKind, materialized map[string]capturedEntry) ([]materialization, error) {
	segments := strings.Split(name, "/")
	planned := make([]materialization, 0, len(segments))
	for i := 1; i < len(segments); i++ {
		parent := strings.Join(segments[:i], "/")
		if existing, found := materialized[parent]; found {
			if existing.kind == extractedSymlink {
				return nil, fmt.Errorf("snapshot: path %q has symlink parent %q", name, parent)
			}
			if existing.kind != extractedDirectory {
				return nil, fmt.Errorf("snapshot: path %q has non-directory parent %q", name, parent)
			}
			continue
		}
		planned = append(planned, materialization{name: parent})
	}
	if existing, found := materialized[name]; found {
		if existing.kind == extractedDirectory && kind == extractedDirectory {
			return planned, nil
		}
		return nil, fmt.Errorf("snapshot: path %q conflicts with an already materialized path", name)
	}
	return append(planned, materialization{name: name}), nil
}

func validateHeader(hdr *tar.Header) error {
	if err := validateArchivePath(hdr.Name); err != nil {
		return err
	}
	if len(hdr.Xattrs) != 0 {
		return fmt.Errorf("snapshot: PAX and extended metadata are not supported for %q", hdr.Name)
	}
	for key, value := range hdr.PAXRecords {
		switch key {
		case "path":
			// archive/tar exposes the effective PAX value in both fields. This
			// equality check detects inconsistent readers; it does not prove what
			// the hidden base header contained. The effective Name was validated
			// above and is the only name we materialize.
			if value != hdr.Name {
				return fmt.Errorf("snapshot: inconsistent PAX path metadata for %q", hdr.Name)
			}
		case "linkpath":
			if hdr.Typeflag != tar.TypeSymlink {
				return fmt.Errorf("snapshot: PAX linkpath is only permitted for symlink entries")
			}
			if value != hdr.Linkname {
				return fmt.Errorf("snapshot: inconsistent PAX link metadata for %q", hdr.Name)
			}
		default:
			return fmt.Errorf("snapshot: unsupported PAX metadata %q for %q", key, hdr.Name)
		}
	}
	if hdr.Mode&06000 != 0 {
		return fmt.Errorf("snapshot: setuid or setgid bits are not permitted for %q", hdr.Name)
	}
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		if hdr.Size < 0 {
			return fmt.Errorf("snapshot: regular file %q has negative size", hdr.Name)
		}
	case tar.TypeDir, tar.TypeSymlink:
		if hdr.Size != 0 {
			return fmt.Errorf("snapshot: non-regular entry %q declares content", hdr.Name)
		}
	default:
		return fmt.Errorf("snapshot: unsupported tar entry type %q for %q", hdr.Typeflag, hdr.Name)
	}
	return nil
}

func validateArchivePath(name string) error {
	if name == "" {
		return fmt.Errorf("snapshot: archive path is empty")
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("snapshot: archive path %q contains a backslash", name)
	}
	if path.IsAbs(name) {
		return fmt.Errorf("snapshot: archive path %q is absolute", name)
	}
	if containsDriveLikeSegment(name) {
		return fmt.Errorf("snapshot: archive path %q is drive-like", name)
	}
	if strings.HasSuffix(name, "/") {
		return fmt.Errorf("snapshot: archive path %q has a trailing separator", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("snapshot: archive path %q contains an empty, dot, or traversal segment", name)
		}
	}
	return nil
}

func containsDriveLikeSegment(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if len(segment) >= 2 && ((segment[0] >= 'a' && segment[0] <= 'z') || (segment[0] >= 'A' && segment[0] <= 'Z')) && segment[1] == ':' {
			return true
		}
	}
	return false
}

func ensureUnmaterialized(root *os.Root, name string) error {
	_, err := root.Lstat(name)
	if err == nil {
		return hostEquivalentCollision(name, fs.ErrExist)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("snapshot: inspect unmaterialized path %q: %w", name, err)
}

func hostEquivalentCollision(name string, err error) error {
	return fmt.Errorf("snapshot: host-equivalent collision at POSIX path %q: %w", name, err)
}

func materializeDirectory(root *os.Root, name string, beforeMaterialize func(*os.Root, string) error) (capturedEntry, error) {
	if beforeMaterialize != nil {
		if err := beforeMaterialize(root, name); err != nil {
			return capturedEntry{}, fmt.Errorf("snapshot: before materializing %q: %w", name, err)
		}
	}
	if err := validateHostParents(root, name); err != nil {
		return capturedEntry{}, err
	}
	if err := ensureUnmaterialized(root, name); err != nil {
		return capturedEntry{}, err
	}
	if err := root.Mkdir(name, 0755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return capturedEntry{}, hostEquivalentCollision(name, err)
		}
		return capturedEntry{}, err
	}
	info, err := normalizeExistingDirectory(root, name)
	if err != nil {
		return capturedEntry{}, err
	}
	return capturedEntry{name: name, kind: extractedDirectory, mode: 0755, info: info}, nil
}

func validateHostParents(root *os.Root, name string) error {
	segments := strings.Split(name, "/")
	for i := 1; i < len(segments); i++ {
		parent := strings.Join(segments[:i], "/")
		info, err := root.Lstat(parent)
		if err != nil {
			return fmt.Errorf("snapshot: inspect parent %q for %q: %w", parent, name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot: path %q has symlink parent %q", name, parent)
		}
		if !info.IsDir() {
			return fmt.Errorf("snapshot: path %q has non-directory parent %q", name, parent)
		}
	}
	return nil
}

func normalizeExistingDirectory(root *os.Root, name string) (fs.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("path is not the materialized directory")
	}
	directory, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := directory.Stat()
	if statErr == nil && (!openedInfo.IsDir() || !os.SameFile(info, openedInfo)) {
		statErr = fmt.Errorf("directory changed while opening")
	}
	chmodErr := error(nil)
	if statErr == nil {
		chmodErr = directory.Chmod(0755)
	}
	if err := errors.Join(statErr, chmodErr, directory.Close()); err != nil {
		return nil, err
	}
	return openedInfo, nil
}

func extractRegular(
	ctx context.Context,
	tr *tar.Reader,
	root *os.Root,
	spool *os.File,
	hdr *tar.Header,
	maxContent int64,
	spoolOffset int64,
	beforeMaterialize func(*os.Root, string) error,
) (capturedEntry, error) {
	remaining := maxContent - spoolOffset
	if remaining < 0 {
		return capturedEntry{}, fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	if hdr.Size > remaining {
		return capturedEntry{}, fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	if beforeMaterialize != nil {
		if err := beforeMaterialize(root, hdr.Name); err != nil {
			return capturedEntry{}, fmt.Errorf("snapshot: before materializing %q: %w", hdr.Name, err)
		}
	}
	if err := validateHostParents(root, hdr.Name); err != nil {
		return capturedEntry{}, err
	}
	if err := ensureUnmaterialized(root, hdr.Name); err != nil {
		return capturedEntry{}, err
	}
	file, err := root.OpenFile(hdr.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return capturedEntry{}, hostEquivalentCollision(hdr.Name, err)
		}
		return capturedEntry{}, fmt.Errorf("snapshot: create regular file %q: %w", hdr.Name, err)
	}
	info, statErr := file.Stat()
	if statErr == nil && !info.Mode().IsRegular() {
		statErr = fmt.Errorf("created descriptor is not a regular file")
	}
	mode := os.FileMode(0644)
	if hdr.Mode&0111 != 0 {
		mode = 0755
	}
	chmodErr := error(nil)
	if statErr == nil {
		chmodErr = file.Chmod(mode)
	}
	if err := errors.Join(statErr, chmodErr); err != nil {
		return capturedEntry{}, errors.Join(fmt.Errorf("snapshot: validate regular file %q: %w", hdr.Name, err), file.Close())
	}
	currentOffset, err := spool.Seek(0, io.SeekCurrent)
	if err != nil {
		return capturedEntry{}, errors.Join(fmt.Errorf("snapshot: inspect content spool offset: %w", err), file.Close())
	}
	if currentOffset != spoolOffset {
		return capturedEntry{}, errors.Join(fmt.Errorf("snapshot: content spool offset changed from %d to %d", spoolOffset, currentOffset), file.Close())
	}

	reader := io.Reader(contextReader{ctx: ctx, reader: tr})
	if remaining != int64(^uint64(0)>>1) {
		reader = io.LimitReader(reader, remaining+1)
	}
	written, copyErr := io.Copy(io.MultiWriter(file, spool), reader)
	finalInfo, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil {
		return capturedEntry{}, fmt.Errorf("snapshot: extract regular file %q: %w", hdr.Name, errors.Join(copyErr, statErr, closeErr))
	}
	if err := errors.Join(statErr, closeErr); err != nil {
		return capturedEntry{}, fmt.Errorf("snapshot: close regular file %q: %w", hdr.Name, err)
	}
	if written > remaining {
		return capturedEntry{}, fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	if written != hdr.Size {
		return capturedEntry{}, fmt.Errorf("snapshot: regular file %q is truncated: copied %d of %d bytes", hdr.Name, written, hdr.Size)
	}
	return capturedEntry{
		name: hdr.Name, kind: extractedRegular, mode: int64(mode), size: written,
		spoolOffset: spoolOffset, info: finalInfo,
	}, nil
}

func cleanSymlinkTarget(name, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("snapshot: symlink %q has an empty target", name)
	}
	if strings.Contains(target, `\`) {
		return "", fmt.Errorf("snapshot: symlink %q target contains a backslash", name)
	}
	if path.IsAbs(target) {
		return "", fmt.Errorf("snapshot: symlink %q target is absolute", name)
	}
	if containsDriveLikeSegment(target) {
		return "", fmt.Errorf("snapshot: symlink %q target is drive-like", name)
	}
	cleaned := path.Clean(target)
	resolved := path.Clean(path.Join(path.Dir(name), cleaned))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("snapshot: symlink %q target escapes the archive root", name)
	}
	return cleaned, nil
}

func writeCanonicalTar(
	ctx context.Context,
	archive *os.File,
	spool *os.File,
	root *os.Root,
	index *captureIndex,
	beforeCanonicalOpen func(*os.Root, string) error,
) (Digest, int64, error) {
	hash := sha256.New()
	counted := &countingWriter{writer: io.MultiWriter(archive, hash)}
	tw := tar.NewWriter(counted)

	writeErr := writeCanonicalEntries(ctx, tw, spool, root, sortedCaptureEntries(index), beforeCanonicalOpen)
	tarCloseErr := tw.Close()
	writeErr = errors.Join(writeErr, tarCloseErr)
	if writeErr != nil {
		return "", 0, fmt.Errorf("snapshot: write canonical archive: %w", writeErr)
	}
	digest := Digest(fmt.Sprintf("sha256:%x", hash.Sum(nil)))
	if err := digest.Validate(); err != nil {
		return "", 0, err
	}
	return digest, counted.count, nil
}

func sortedCaptureEntries(index *captureIndex) []capturedEntry {
	entries := make([]capturedEntry, 0, len(index.entries))
	for _, entry := range index.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries
}

func writeCanonicalEntries(
	ctx context.Context,
	tw *tar.Writer,
	spool *os.File,
	root *os.Root,
	entries []capturedEntry,
	beforeCanonicalOpen func(*os.Root, string) error,
) error {
	epoch := time.Unix(0, 0).UTC()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:       entry.name,
			Uid:        0,
			Gid:        0,
			Uname:      "",
			Gname:      "",
			ModTime:    epoch,
			AccessTime: epoch,
			ChangeTime: epoch,
			// FormatGNU is part of physical snapshot identity. Changing the
			// serializer or its header format requires an identity migration.
			Format: tar.FormatGNU,
		}
		switch entry.kind {
		case extractedRegular:
			if beforeCanonicalOpen != nil {
				if err := beforeCanonicalOpen(root, entry.name); err != nil {
					return fmt.Errorf("before canonical spool read %q: %w", entry.name, err)
				}
			}
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = entry.mode
			hdr.Size = entry.size
		case extractedDirectory:
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = entry.mode
		case extractedSymlink:
			hdr.Typeflag = tar.TypeSymlink
			hdr.Mode = entry.mode
			hdr.Linkname = entry.target
		default:
			return fmt.Errorf("unsupported captured file type for %q", entry.name)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if entry.kind != extractedRegular {
			continue
		}
		section := io.NewSectionReader(spool, entry.spoolOffset, entry.size)
		written, err := io.Copy(tw, contextReader{ctx: ctx, reader: section})
		if err != nil {
			return err
		}
		if written != entry.size {
			return fmt.Errorf("snapshot: content spool is truncated for %q", entry.name)
		}
	}
	return nil
}

func verifyCaptureIndex(ctx context.Context, root *os.Root, index *captureIndex, maxEntries int64) error {
	seen := make(map[string]struct{}, len(index.entries))
	err := fs.WalkDir(root.FS(), ".", func(name string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if int64(len(seen)) >= maxEntries {
			return fmt.Errorf("canonical tree exceeds entry limit of %d", maxEntries)
		}
		expected, found := index.entries[name]
		if !found {
			return fmt.Errorf("unexpected path %q", name)
		}
		info, err := dirEntry.Info()
		if err != nil {
			return err
		}
		if err := verifyCapturedEntry(root, expected, info); err != nil {
			return err
		}
		seen[name] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(index.entries) {
		missing := make([]string, 0)
		for name := range index.entries {
			if _, found := seen[name]; !found {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("captured paths disappeared: %q", missing)
	}
	return nil
}

func verifyCapturedEntry(root *os.Root, expected capturedEntry, info fs.FileInfo) error {
	changed := func(detail string) error {
		return fmt.Errorf("path %q changed during capture: %s", expected.name, detail)
	}
	if !os.SameFile(expected.info, info) {
		return changed("filesystem identity differs")
	}
	switch expected.kind {
	case extractedRegular:
		if !info.Mode().IsRegular() {
			return changed("kind is no longer regular")
		}
		if info.Size() != expected.size {
			return changed("regular size differs")
		}
		if int64(info.Mode().Perm()) != expected.mode {
			return changed("regular mode differs")
		}
	case extractedDirectory:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return changed("kind is no longer directory")
		}
		if int64(info.Mode().Perm()) != expected.mode {
			return changed("directory mode differs")
		}
	case extractedSymlink:
		if info.Mode()&os.ModeSymlink == 0 {
			return changed("kind is no longer symlink")
		}
		if info.Mode().Perm() != expected.info.Mode().Perm() {
			return changed("symlink mode differs")
		}
		target, err := root.Readlink(expected.name)
		if err != nil {
			return changed(err.Error())
		}
		cleaned, err := cleanSymlinkTarget(expected.name, target)
		if err != nil {
			return changed(err.Error())
		}
		if cleaned != expected.target {
			return changed("symlink target differs")
		}
	default:
		return changed("unknown captured kind")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}
