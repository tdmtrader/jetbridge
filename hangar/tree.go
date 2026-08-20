package hangar

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxTreeEntries      int64 = 100_000
	DefaultMaxTreeContentBytes int64 = 10 << 30
	maxTreePathBytes           int64 = 4096
	maxTreeSymlinkTargetBytes  int64 = 4096
)

const tarBlockBytes int64 = 512

// TreeLimits are the logical admission limits for a tree archive.
// They are intentionally distinct from the derived physical tar byte bound:
// lowering MaxContentBytes to one byte must reject a two-byte file even though
// both archives fit inside the tar transport envelope.
type TreeLimits struct {
	MaxContentBytes int64
	MaxEntries      int64
}

func (limits TreeLimits) validate() error {
	if limits.MaxContentBytes <= 0 {
		return fmt.Errorf("hangar: maximum content bytes must be positive")
	}
	if limits.MaxEntries <= 0 {
		return fmt.Errorf("hangar: maximum entries must be positive")
	}
	return nil
}

// ValidateArchiveLimits streams an archive once and enforces the same logical
// entry and regular-content accounting used by Canonicalizer. Implicit parent
// directories count as entries, so a non-canonical nested path cannot evade an
// entry limit by omitting directory headers.
func ValidateArchiveLimits(ctx context.Context, source io.Reader, limits TreeLimits) error {
	if source == nil {
		return fmt.Errorf("hangar: archive reader is required")
	}
	if err := limits.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	reader := tar.NewReader(contextReader{ctx: ctx, reader: source})
	materialized := make(map[string]capturedEntry)
	seenHeaders := make(map[string]struct{})
	var contentBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			var trailing [1]byte
			n, trailingErr := io.ReadFull(contextReader{ctx: ctx, reader: source}, trailing[:])
			if n != 0 {
				return fmt.Errorf("hangar: archive contains trailing data after the tar terminator")
			}
			if errors.Is(trailingErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("hangar: inspect archive terminator: %w", trailingErr)
		}
		if err != nil {
			return fmt.Errorf("hangar: read admitted archive: %w", err)
		}
		if err := validateHeader(header); err != nil {
			return err
		}
		if _, exists := seenHeaders[header.Name]; exists {
			return fmt.Errorf("hangar: duplicate canonical path %q", header.Name)
		}
		seenHeaders[header.Name] = struct{}{}
		kind := headerKind(header.Typeflag)
		planned, err := planMaterialization(header.Name, kind, materialized)
		if err != nil {
			return err
		}
		if int64(len(materialized)) > limits.MaxEntries-int64(len(planned)) {
			return fmt.Errorf("%w: archive exceeds entry limit of %d", ErrLimitExceeded, limits.MaxEntries)
		}
		for _, entry := range planned {
			materialized[entry.name] = capturedEntry{name: entry.name, kind: extractedDirectory}
		}
		materialized[header.Name] = capturedEntry{name: header.Name, kind: kind}

		if kind == extractedSymlink {
			if _, err := cleanSymlinkTarget(header.Name, header.Linkname); err != nil {
				return err
			}
		}
		if kind != extractedRegular {
			continue
		}
		if contentBytes > limits.MaxContentBytes-header.Size {
			return fmt.Errorf("%w: archive exceeds regular content limit of %d bytes", ErrLimitExceeded, limits.MaxContentBytes)
		}
		contentBytes += header.Size
		copied, err := io.Copy(io.Discard, contextReader{ctx: ctx, reader: reader})
		if err != nil {
			return fmt.Errorf("hangar: read regular content for %q: %w", header.Name, err)
		}
		if copied != header.Size {
			return fmt.Errorf("hangar: regular file %q is truncated: copied %d of %d bytes", header.Name, copied, header.Size)
		}
	}
}

// CanonicalArchiveByteLimit derives a checked transport bound from the
// logical canonicalizer limits. Canonical GNU tar includes headers, padding,
// optional GNU long-name/long-link records, and a two-block trailer in
// addition to regular-file content. The result deliberately overestimates by
// allowing both long-string records and content padding for every entry.
func CanonicalArchiveByteLimit(maxContentBytes, maxEntries int64) (int64, error) {
	if maxContentBytes <= 0 {
		return 0, fmt.Errorf("hangar: maximum content bytes must be positive")
	}
	if maxEntries <= 0 {
		return 0, fmt.Errorf("hangar: maximum entries must be positive")
	}
	roundBlock := func(value int64) (int64, error) {
		if value < 0 || value > math.MaxInt64-(tarBlockBytes-1) {
			return 0, fmt.Errorf("hangar: canonical archive bound overflows")
		}
		return ((value + tarBlockBytes - 1) / tarBlockBytes) * tarBlockBytes, nil
	}
	longNamePayload, err := roundBlock(maxTreePathBytes + 1)
	if err != nil {
		return 0, err
	}
	longLinkPayload, err := roundBlock(maxTreeSymlinkTargetBytes + 1)
	if err != nil {
		return 0, err
	}
	perEntry := tarBlockBytes + tarBlockBytes + longNamePayload + tarBlockBytes + longLinkPayload + (tarBlockBytes - 1)
	if maxEntries > (math.MaxInt64-2*tarBlockBytes)/perEntry {
		return 0, fmt.Errorf("hangar: canonical archive bound overflows")
	}
	overhead := maxEntries*perEntry + 2*tarBlockBytes
	if maxContentBytes > math.MaxInt64-overhead {
		return 0, fmt.Errorf("hangar: canonical archive bound overflows")
	}
	return maxContentBytes + overhead, nil
}

// Canonicalizer safely materializes a tar stream and emits its deterministic
// filesystem-tree representation. Zero limits select the secure defaults.
//
// Tree paths form a bytewise POSIX namespace. Tree storage execution
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
	beforeSpoolSeal       func(*os.Root, *os.File) error
	afterSpoolSeal        func(*os.Root, *os.File) error
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
		return nil, fmt.Errorf("hangar: tar reader is required")
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

	privateRoot, err := os.MkdirTemp(c.TempDir, "hangar-tree-")
	if err != nil {
		return nil, fmt.Errorf("hangar: create private capture directory: %w", err)
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
		return nil, fmt.Errorf("hangar: anchor private capture directory: %w", err)
	}
	captureInfo, err := captureRoot.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("hangar: stat private capture directory: %w", err)
	}
	if err := captureRoot.Mkdir("root", 0700); err != nil {
		return nil, fmt.Errorf("hangar: create extraction root: %w", err)
	}
	extractionRoot, err = captureRoot.OpenRoot("root")
	if err != nil {
		return nil, fmt.Errorf("hangar: open extraction root: %w", err)
	}
	extractionInfo, err := extractionRoot.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("hangar: stat extraction root: %w", err)
	}
	spool, err = captureRoot.OpenFile("content.spool", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("hangar: create content spool: %w", err)
	}
	if err := spool.Chmod(0600); err != nil {
		return nil, fmt.Errorf("hangar: normalize content spool: %w", err)
	}
	index, err := extractTar(ctx, rawTar, extractionRoot, spool, maxEntries, maxContent, c.beforeMaterialize)
	if err != nil {
		return nil, err
	}
	if c.beforeSpoolSeal != nil {
		if err := c.beforeSpoolSeal(captureRoot, spool); err != nil {
			return nil, fmt.Errorf("hangar: before sealing content spool: %w", err)
		}
	}
	writableSpool := spool
	spool = nil
	spool, err = sealContentSpool(captureRoot, writableSpool, index)
	if err != nil {
		return nil, err
	}
	if c.afterSpoolSeal != nil {
		if err := c.afterSpoolSeal(captureRoot, spool); err != nil {
			return nil, fmt.Errorf("hangar: after sealing content spool: %w", err)
		}
	}
	if err := validateSpoolContents(ctx, spool, index); err != nil {
		return nil, fmt.Errorf("hangar: verify sealed content spool: %w", err)
	}
	if c.beforePreEmitVerify != nil {
		if err := c.beforePreEmitVerify(extractionRoot); err != nil {
			return nil, fmt.Errorf("hangar: before pre-emission verification: %w", err)
		}
	}
	if err := verifyCaptureIndex(ctx, extractionRoot, index, maxEntries); err != nil {
		return nil, fmt.Errorf("hangar: verify captured tree before emission: %w", err)
	}

	archive, err = captureRoot.OpenFile("canonical.tar", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("hangar: create canonical archive: %w", err)
	}
	if err := archive.Chmod(0600); err != nil {
		return nil, fmt.Errorf("hangar: normalize canonical archive: %w", err)
	}
	digest, byteSize, err := writeCanonicalTar(ctx, archive, spool, extractionRoot, index, c.beforeCanonicalOpen)
	if err != nil {
		return nil, err
	}
	archiveInfo, err := archive.Stat()
	if err != nil {
		return nil, fmt.Errorf("hangar: stat canonical archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("hangar: close canonical archive: %w", err)
	}
	archive = nil
	if c.beforePostEmitVerify != nil {
		if err := c.beforePostEmitVerify(extractionRoot); err != nil {
			return nil, fmt.Errorf("hangar: before post-emission verification: %w", err)
		}
	}
	if err := verifyCaptureIndex(ctx, extractionRoot, index, maxEntries); err != nil {
		return nil, fmt.Errorf("hangar: verify captured tree after emission: %w", err)
	}
	if err := spool.Close(); err != nil {
		return nil, fmt.Errorf("hangar: close content spool: %w", err)
	}
	spool = nil
	if c.beforeCaptureBoundary != nil {
		if err := c.beforeCaptureBoundary(captureRoot, privateRoot); err != nil {
			return nil, fmt.Errorf("hangar: before capture boundary verification: %w", err)
		}
	}
	if err := verifyCaptureBoundary(captureRoot, captureInfo, extractionRoot, extractionInfo, archiveInfo, privateRoot); err != nil {
		return nil, err
	}
	if err := verifyCaptureIndex(ctx, extractionRoot, index, maxEntries); err != nil {
		return nil, fmt.Errorf("hangar: verify captured tree at success boundary: %w", err)
	}
	if err := verifyCanonicalArchive(ctx, captureRoot, archiveInfo, privateRoot, byteSize, digest); err != nil {
		return nil, fmt.Errorf("hangar: verify returned canonical archive: %w", err)
	}
	if err := extractionRoot.Close(); err != nil {
		return nil, fmt.Errorf("hangar: close extraction root: %w", err)
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
		maxEntries = DefaultMaxTreeEntries
	}
	if maxEntries < 0 {
		return 0, 0, fmt.Errorf("hangar: maximum entries must be positive")
	}
	maxContent := c.MaxContentBytes
	if maxContent == 0 {
		maxContent = DefaultMaxTreeContentBytes
	}
	if maxContent < 0 {
		return 0, 0, fmt.Errorf("hangar: maximum content bytes must be positive")
	}
	return maxEntries, maxContent, nil
}

// ValidateTempDir verifies that a configured tree scratch parent exists
// and cannot be replaced or populated by an unrelated local user. An empty
// value preserves the Canonicalizer's legacy os.TempDir behavior.
func ValidateTempDir(tempDir string) error {
	if tempDir == "" {
		return nil
	}
	info, err := os.Stat(tempDir)
	if err != nil {
		return fmt.Errorf("hangar: inspect trusted temporary parent %q: %w", tempDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("hangar: trusted temporary parent %q is not a directory", tempDir)
	}
	if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("hangar: trusted temporary parent %q is group- or other-writable without the sticky bit", tempDir)
	}
	return nil
}

func validateConfiguredTempDir(tempDir string) error {
	return ValidateTempDir(tempDir)
}

func wipeCaptureRoot(root *os.Root) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("hangar: list anchored capture directory for cleanup: %w", err)
	}
	var cleanupErr error
	for _, entry := range entries {
		if err := root.RemoveAll(entry.Name()); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("hangar: remove anchored capture path %q: %w", entry.Name(), err))
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
		return fmt.Errorf("hangar: verify capture path identity through anchored handle: %w", err)
	}
	pathCaptureInfo, err := os.Lstat(privateRoot)
	if err != nil {
		return fmt.Errorf("hangar: verify capture path identity at %q: %w", privateRoot, err)
	}
	if !os.SameFile(captureInfo, anchoredCaptureInfo) || !os.SameFile(captureInfo, pathCaptureInfo) {
		return fmt.Errorf("hangar: capture path identity changed before success")
	}

	anchoredExtractionInfo, err := extractionRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("hangar: verify extraction path identity through anchored handle: %w", err)
	}
	captureExtractionInfo, err := captureRoot.Lstat("root")
	if err != nil {
		return fmt.Errorf("hangar: verify extraction path identity through capture root: %w", err)
	}
	pathExtractionInfo, err := os.Lstat(filepath.Join(privateRoot, "root"))
	if err != nil {
		return fmt.Errorf("hangar: verify extraction path identity at object path: %w", err)
	}
	if !os.SameFile(extractionInfo, anchoredExtractionInfo) ||
		!os.SameFile(extractionInfo, captureExtractionInfo) ||
		!os.SameFile(extractionInfo, pathExtractionInfo) {
		return fmt.Errorf("hangar: extraction path identity changed before success")
	}

	anchoredArchiveInfo, err := captureRoot.Lstat("canonical.tar")
	if err != nil {
		return fmt.Errorf("hangar: verify archive path identity through capture root: %w", err)
	}
	pathArchiveInfo, err := os.Lstat(filepath.Join(privateRoot, "canonical.tar"))
	if err != nil {
		return fmt.Errorf("hangar: verify archive path identity at object path: %w", err)
	}
	if !os.SameFile(archiveInfo, anchoredArchiveInfo) ||
		!os.SameFile(archiveInfo, pathArchiveInfo) {
		return fmt.Errorf("hangar: archive path identity changed before success")
	}
	return nil
}

func verifyCanonicalArchive(
	ctx context.Context,
	captureRoot *os.Root,
	originalInfo fs.FileInfo,
	privateRoot string,
	byteSize int64,
	digest Digest,
) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	validateInfo := func(location string, info fs.FileInfo) error {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("hangar: canonical archive type at %s is %v, want regular", location, info.Mode())
		}
		if info.Mode().Perm() != 0600 {
			return fmt.Errorf("hangar: canonical archive mode at %s is %v, want 0600", location, info.Mode().Perm())
		}
		if info.Size() != byteSize {
			return fmt.Errorf("hangar: canonical archive byte size at %s is %d, want %d", location, info.Size(), byteSize)
		}
		if !os.SameFile(originalInfo, info) {
			return fmt.Errorf("hangar: canonical archive path identity at %s changed before success", location)
		}
		return nil
	}
	if err := validateInfo("created descriptor", originalInfo); err != nil {
		return err
	}
	anchoredBefore, err := captureRoot.Lstat("canonical.tar")
	if err != nil {
		return fmt.Errorf("hangar: inspect canonical archive path identity before reading: %w", err)
	}
	if err := validateInfo("anchored path before reading", anchoredBefore); err != nil {
		return err
	}
	archivePath := filepath.Join(privateRoot, "canonical.tar")
	pathBefore, err := os.Lstat(archivePath)
	if err != nil {
		return fmt.Errorf("hangar: inspect canonical archive object path identity before reading: %w", err)
	}
	if err := validateInfo("object path before reading", pathBefore); err != nil {
		return err
	}

	archive, err := captureRoot.Open("canonical.tar")
	if err != nil {
		return fmt.Errorf("hangar: reopen canonical archive read-only: %w", err)
	}
	defer func() {
		err = errors.Join(err, archive.Close())
	}()
	descriptorBefore, err := archive.Stat()
	if err != nil {
		return fmt.Errorf("hangar: stat canonical archive descriptor before reading: %w", err)
	}
	if err := validateInfo("reopened descriptor before reading", descriptorBefore); err != nil {
		return err
	}
	anchoredOpened, err := captureRoot.Lstat("canonical.tar")
	if err != nil {
		return fmt.Errorf("hangar: recheck canonical archive path identity before reading: %w", err)
	}
	if err := validateInfo("anchored path after reopening", anchoredOpened); err != nil {
		return err
	}
	pathOpened, err := os.Lstat(archivePath)
	if err != nil {
		return fmt.Errorf("hangar: recheck canonical archive object path identity before reading: %w", err)
	}
	if err := validateInfo("object path after reopening", pathOpened); err != nil {
		return err
	}

	hasher := sha256.New()
	readSize, readErr := io.Copy(hasher, contextReader{ctx: ctx, reader: archive})
	if readErr != nil {
		return fmt.Errorf("hangar: hash canonical archive: %w", readErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	descriptorAfter, err := archive.Stat()
	if err != nil {
		return fmt.Errorf("hangar: stat canonical archive descriptor after reading: %w", err)
	}
	if err := validateInfo("reopened descriptor after reading", descriptorAfter); err != nil {
		return err
	}
	anchoredAfter, err := captureRoot.Lstat("canonical.tar")
	if err != nil {
		return fmt.Errorf("hangar: recheck canonical archive path identity after reading: %w", err)
	}
	if err := validateInfo("anchored path after reading", anchoredAfter); err != nil {
		return err
	}
	pathAfter, err := os.Lstat(archivePath)
	if err != nil {
		return fmt.Errorf("hangar: recheck canonical archive object path identity after reading: %w", err)
	}
	if err := validateInfo("object path after reading", pathAfter); err != nil {
		return err
	}
	if readSize != byteSize {
		return fmt.Errorf("hangar: canonical archive byte size read is %d, want %d", readSize, byteSize)
	}
	actualDigest := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
	if actualDigest != digest {
		return fmt.Errorf("hangar: canonical archive digest is %q, want %q", actualDigest, digest)
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
	contentHash [sha256.Size]byte
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
			return nil, fmt.Errorf("hangar: read tar header: %w", err)
		}
		if err := validateHeader(hdr); err != nil {
			return nil, err
		}
		name := hdr.Name
		if _, exists := seenHeaders[name]; exists {
			return nil, fmt.Errorf("hangar: duplicate canonical path %q", name)
		}
		seenHeaders[name] = struct{}{}
		kind := headerKind(hdr.Typeflag)
		planned, err := planMaterialization(name, kind, index.entries)
		if err != nil {
			return nil, err
		}
		if int64(len(index.entries))+int64(len(planned)) > maxEntries {
			return nil, fmt.Errorf("%w: archive exceeds entry limit of %d", ErrLimitExceeded, maxEntries)
		}
		for _, plannedEntry := range planned {
			if plannedEntry.name == name {
				continue
			}
			entry, err := materializeDirectory(root, plannedEntry.name, beforeMaterialize)
			if err != nil {
				return nil, fmt.Errorf("hangar: extract implicit directory %q: %w", plannedEntry.name, err)
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
					return nil, fmt.Errorf("hangar: extract directory %q: %w", name, err)
				}
				index.entries[name] = entry
			} else {
				info, err := normalizeExistingDirectory(root, name)
				if err != nil {
					return nil, fmt.Errorf("hangar: extract directory %q: %w", name, err)
				}
				captured := index.entries[name]
				if !os.SameFile(captured.info, info) {
					return nil, fmt.Errorf("hangar: implicit directory %q changed before explicit header", name)
				}
			}
		case tar.TypeSymlink:
			target, err := cleanSymlinkTarget(name, hdr.Linkname)
			if err != nil {
				return nil, err
			}
			if beforeMaterialize != nil {
				if err := beforeMaterialize(root, name); err != nil {
					return nil, fmt.Errorf("hangar: before materializing %q: %w", name, err)
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
				return nil, fmt.Errorf("hangar: create symlink %q: %w", name, err)
			}
			info, err := root.Lstat(name)
			if err != nil {
				return nil, fmt.Errorf("hangar: stat symlink %q: %w", name, err)
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
				return nil, fmt.Errorf("hangar: path %q has symlink parent %q", name, parent)
			}
			if existing.kind != extractedDirectory {
				return nil, fmt.Errorf("hangar: path %q has non-directory parent %q", name, parent)
			}
			continue
		}
		planned = append(planned, materialization{name: parent})
	}
	if existing, found := materialized[name]; found {
		if existing.kind == extractedDirectory && kind == extractedDirectory {
			return planned, nil
		}
		return nil, fmt.Errorf("hangar: path %q conflicts with an already materialized path", name)
	}
	return append(planned, materialization{name: name}), nil
}

func validateHeader(hdr *tar.Header) error {
	if err := validateArchivePath(hdr.Name); err != nil {
		return err
	}
	if len(hdr.Xattrs) != 0 {
		return fmt.Errorf("hangar: PAX and extended metadata are not supported for %q", hdr.Name)
	}
	for key, value := range hdr.PAXRecords {
		switch key {
		case "path":
			// archive/tar exposes the effective PAX value in both fields. This
			// equality check detects inconsistent readers; it does not prove what
			// the hidden base header contained. The effective Name was validated
			// above and is the only name we materialize.
			if value != hdr.Name {
				return fmt.Errorf("hangar: inconsistent PAX path metadata for %q", hdr.Name)
			}
		case "linkpath":
			if hdr.Typeflag != tar.TypeSymlink {
				return fmt.Errorf("hangar: PAX linkpath is only permitted for symlink entries")
			}
			if value != hdr.Linkname {
				return fmt.Errorf("hangar: inconsistent PAX link metadata for %q", hdr.Name)
			}
		default:
			return fmt.Errorf("hangar: unsupported PAX metadata %q for %q", key, hdr.Name)
		}
	}
	if hdr.Mode&06000 != 0 {
		return fmt.Errorf("hangar: setuid or setgid bits are not permitted for %q", hdr.Name)
	}
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		if hdr.Size < 0 {
			return fmt.Errorf("hangar: regular file %q has negative size", hdr.Name)
		}
	case tar.TypeDir, tar.TypeSymlink:
		if hdr.Size != 0 {
			return fmt.Errorf("hangar: non-regular entry %q declares content", hdr.Name)
		}
	default:
		return fmt.Errorf("hangar: unsupported tar entry type %q for %q", hdr.Typeflag, hdr.Name)
	}
	return nil
}

func validateArchivePath(name string) error {
	if name == "" {
		return fmt.Errorf("hangar: archive path is empty")
	}
	if int64(len(name)) > maxTreePathBytes {
		return fmt.Errorf("hangar: archive path exceeds %d bytes", maxTreePathBytes)
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("hangar: archive path %q contains a backslash", name)
	}
	if path.IsAbs(name) {
		return fmt.Errorf("hangar: archive path %q is absolute", name)
	}
	if containsDriveLikeSegment(name) {
		return fmt.Errorf("hangar: archive path %q is drive-like", name)
	}
	if strings.HasSuffix(name, "/") {
		return fmt.Errorf("hangar: archive path %q has a trailing separator", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("hangar: archive path %q contains an empty, dot, or traversal segment", name)
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
	return fmt.Errorf("hangar: inspect unmaterialized path %q: %w", name, err)
}

func hostEquivalentCollision(name string, err error) error {
	return fmt.Errorf("hangar: host-equivalent collision at POSIX path %q: %w", name, err)
}

func materializeDirectory(root *os.Root, name string, beforeMaterialize func(*os.Root, string) error) (capturedEntry, error) {
	if beforeMaterialize != nil {
		if err := beforeMaterialize(root, name); err != nil {
			return capturedEntry{}, fmt.Errorf("hangar: before materializing %q: %w", name, err)
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
			return fmt.Errorf("hangar: inspect parent %q for %q: %w", parent, name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("hangar: path %q has symlink parent %q", name, parent)
		}
		if !info.IsDir() {
			return fmt.Errorf("hangar: path %q has non-directory parent %q", name, parent)
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
		return capturedEntry{}, fmt.Errorf("%w: archive exceeds regular content limit of %d bytes", ErrLimitExceeded, maxContent)
	}
	if hdr.Size > remaining {
		return capturedEntry{}, fmt.Errorf("%w: archive exceeds regular content limit of %d bytes", ErrLimitExceeded, maxContent)
	}
	if beforeMaterialize != nil {
		if err := beforeMaterialize(root, hdr.Name); err != nil {
			return capturedEntry{}, fmt.Errorf("hangar: before materializing %q: %w", hdr.Name, err)
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
		return capturedEntry{}, fmt.Errorf("hangar: create regular file %q: %w", hdr.Name, err)
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
		return capturedEntry{}, errors.Join(fmt.Errorf("hangar: validate regular file %q: %w", hdr.Name, err), file.Close())
	}
	currentOffset, err := spool.Seek(0, io.SeekCurrent)
	if err != nil {
		return capturedEntry{}, errors.Join(fmt.Errorf("hangar: inspect content spool offset: %w", err), file.Close())
	}
	if currentOffset != spoolOffset {
		return capturedEntry{}, errors.Join(fmt.Errorf("hangar: content spool offset changed from %d to %d", spoolOffset, currentOffset), file.Close())
	}

	reader := io.Reader(contextReader{ctx: ctx, reader: tr})
	if remaining != int64(^uint64(0)>>1) {
		reader = io.LimitReader(reader, remaining+1)
	}
	contentHasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, spool, contentHasher), reader)
	finalInfo, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil {
		return capturedEntry{}, fmt.Errorf("hangar: extract regular file %q: %w", hdr.Name, errors.Join(copyErr, statErr, closeErr))
	}
	if err := errors.Join(statErr, closeErr); err != nil {
		return capturedEntry{}, fmt.Errorf("hangar: close regular file %q: %w", hdr.Name, err)
	}
	if written > remaining {
		return capturedEntry{}, fmt.Errorf("%w: archive exceeds regular content limit of %d bytes", ErrLimitExceeded, maxContent)
	}
	if written != hdr.Size {
		return capturedEntry{}, fmt.Errorf("hangar: regular file %q is truncated: copied %d of %d bytes", hdr.Name, written, hdr.Size)
	}
	entry := capturedEntry{
		name: hdr.Name, kind: extractedRegular, mode: int64(mode), size: written,
		spoolOffset: spoolOffset, info: finalInfo,
	}
	copy(entry.contentHash[:], contentHasher.Sum(nil))
	return entry, nil
}

func sealContentSpool(root *os.Root, writable *os.File, index *captureIndex) (*os.File, error) {
	closeWritable := func(cause error) (*os.File, error) {
		return nil, errors.Join(cause, writable.Close())
	}
	writableInfo, err := writable.Stat()
	if err != nil {
		return closeWritable(fmt.Errorf("hangar: stat writable content spool: %w", err))
	}
	if !writableInfo.Mode().IsRegular() || writableInfo.Mode().Perm() != 0600 {
		return closeWritable(fmt.Errorf("hangar: writable content spool has unexpected type or mode %v", writableInfo.Mode()))
	}
	pathInfo, err := root.Lstat("content.spool")
	if err != nil {
		return closeWritable(fmt.Errorf("hangar: inspect content spool path identity: %w", err))
	}
	if !os.SameFile(writableInfo, pathInfo) {
		return closeWritable(fmt.Errorf("hangar: content spool path identity changed before sealing"))
	}
	if err := validateSpoolLayout(index, writableInfo.Size()); err != nil {
		return closeWritable(err)
	}
	if err := writable.Close(); err != nil {
		return nil, fmt.Errorf("hangar: close writable content spool: %w", err)
	}

	sealed, err := root.Open("content.spool")
	if err != nil {
		return nil, fmt.Errorf("hangar: reopen content spool read-only: %w", err)
	}
	closeSealed := func(cause error) (*os.File, error) {
		return nil, errors.Join(cause, sealed.Close())
	}
	sealedInfo, err := sealed.Stat()
	if err != nil {
		return closeSealed(fmt.Errorf("hangar: stat sealed content spool: %w", err))
	}
	sealedPathInfo, err := root.Lstat("content.spool")
	if err != nil {
		return closeSealed(fmt.Errorf("hangar: recheck content spool path identity: %w", err))
	}
	if !os.SameFile(writableInfo, sealedInfo) || !os.SameFile(writableInfo, sealedPathInfo) {
		return closeSealed(fmt.Errorf("hangar: content spool path identity changed while sealing"))
	}
	if !sealedInfo.Mode().IsRegular() || sealedInfo.Mode().Perm() != 0600 {
		return closeSealed(fmt.Errorf("hangar: sealed content spool has unexpected type or mode %v", sealedInfo.Mode()))
	}
	if err := validateSpoolLayout(index, sealedInfo.Size()); err != nil {
		return closeSealed(err)
	}
	if err := root.Remove("content.spool"); err != nil {
		return closeSealed(fmt.Errorf("hangar: unlink sealed content spool: %w", err))
	}
	if _, err := root.Lstat("content.spool"); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = fmt.Errorf("path still exists")
		}
		return closeSealed(fmt.Errorf("hangar: verify sealed content spool is unlinked: %w", err))
	}
	return sealed, nil
}

func validateSpoolLayout(index *captureIndex, actualSize int64) error {
	if index == nil {
		return fmt.Errorf("hangar: content spool index is required")
	}
	if actualSize < 0 || index.spoolSize < 0 || index.spoolSize != actualSize {
		return fmt.Errorf("hangar: content spool size is %d, want %d", actualSize, index.spoolSize)
	}
	regulars := make([]capturedEntry, 0, len(index.entries))
	for _, entry := range index.entries {
		if entry.kind == extractedRegular {
			regulars = append(regulars, entry)
			continue
		}
		if entry.spoolOffset != 0 || entry.size != 0 {
			return fmt.Errorf("hangar: non-regular path %q claims content spool bytes", entry.name)
		}
	}
	sortSpoolEntries(regulars)
	expectedOffset := int64(0)
	for _, entry := range regulars {
		if entry.spoolOffset < 0 || entry.size < 0 {
			return fmt.Errorf("hangar: content spool section for %q is outside bounds", entry.name)
		}
		if entry.spoolOffset != expectedOffset {
			return fmt.Errorf("hangar: content spool section for %q has a gap or overlap at offset %d, want %d", entry.name, entry.spoolOffset, expectedOffset)
		}
		if entry.spoolOffset > actualSize || entry.size > actualSize-entry.spoolOffset {
			return fmt.Errorf("hangar: content spool section for %q exceeds bounds", entry.name)
		}
		expectedOffset += entry.size
	}
	if expectedOffset != actualSize {
		return fmt.Errorf("hangar: content spool sections cover %d bytes, want %d", expectedOffset, actualSize)
	}
	return nil
}

func validateSpoolContents(ctx context.Context, spool *os.File, index *captureIndex) error {
	initialInfo, err := spool.Stat()
	if err != nil {
		return fmt.Errorf("hangar: stat content spool: %w", err)
	}
	if !initialInfo.Mode().IsRegular() || initialInfo.Mode().Perm() != 0600 {
		return fmt.Errorf("hangar: content spool has unexpected type or mode %v", initialInfo.Mode())
	}
	if err := validateSpoolLayout(index, initialInfo.Size()); err != nil {
		return err
	}
	for _, entry := range sortedSpoolEntries(index) {
		if err := ctx.Err(); err != nil {
			return err
		}
		hasher := sha256.New()
		section := io.NewSectionReader(spool, entry.spoolOffset, entry.size)
		written, err := io.Copy(hasher, contextReader{ctx: ctx, reader: section})
		if err != nil {
			return fmt.Errorf("hangar: read content spool section for %q: %w", entry.name, err)
		}
		if written != entry.size {
			return fmt.Errorf("hangar: content spool section for %q is truncated", entry.name)
		}
		var actualHash [sha256.Size]byte
		copy(actualHash[:], hasher.Sum(nil))
		if actualHash != entry.contentHash {
			return fmt.Errorf("hangar: spool content differs for %q", entry.name)
		}
	}
	finalInfo, err := spool.Stat()
	if err != nil {
		return fmt.Errorf("hangar: restat content spool: %w", err)
	}
	if !os.SameFile(initialInfo, finalInfo) {
		return fmt.Errorf("hangar: content spool descriptor identity changed during verification")
	}
	if err := validateSpoolLayout(index, finalInfo.Size()); err != nil {
		return err
	}
	return nil
}

func sortedSpoolEntries(index *captureIndex) []capturedEntry {
	regulars := make([]capturedEntry, 0, len(index.entries))
	for _, entry := range index.entries {
		if entry.kind == extractedRegular {
			regulars = append(regulars, entry)
		}
	}
	sortSpoolEntries(regulars)
	return regulars
}

func sortSpoolEntries(regulars []capturedEntry) {
	sort.Slice(regulars, func(i, j int) bool {
		if regulars[i].spoolOffset != regulars[j].spoolOffset {
			return regulars[i].spoolOffset < regulars[j].spoolOffset
		}
		if regulars[i].size != regulars[j].size {
			return regulars[i].size < regulars[j].size
		}
		return regulars[i].name < regulars[j].name
	})
}

func cleanSymlinkTarget(name, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("hangar: symlink %q has an empty target", name)
	}
	if int64(len(target)) > maxTreeSymlinkTargetBytes {
		return "", fmt.Errorf("hangar: symlink %q target exceeds %d bytes", name, maxTreeSymlinkTargetBytes)
	}
	if strings.Contains(target, `\`) {
		return "", fmt.Errorf("hangar: symlink %q target contains a backslash", name)
	}
	if path.IsAbs(target) {
		return "", fmt.Errorf("hangar: symlink %q target is absolute", name)
	}
	if containsDriveLikeSegment(target) {
		return "", fmt.Errorf("hangar: symlink %q target is drive-like", name)
	}
	cleaned := path.Clean(target)
	resolved := path.Clean(path.Join(path.Dir(name), cleaned))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("hangar: symlink %q target escapes the archive root", name)
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
		return "", 0, fmt.Errorf("hangar: write canonical archive: %w", writeErr)
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
			// FormatGNU is part of physical tree identity. Changing the
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
		contentHasher := sha256.New()
		written, err := io.Copy(tw, contextReader{ctx: ctx, reader: io.TeeReader(section, contentHasher)})
		if err != nil {
			return err
		}
		if written != entry.size {
			return fmt.Errorf("hangar: content spool is truncated for %q", entry.name)
		}
		var actualHash [sha256.Size]byte
		copy(actualHash[:], contentHasher.Sum(nil))
		if actualHash != entry.contentHash {
			return fmt.Errorf("hangar: spool content differs for %q during canonical emission", entry.name)
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
			return fmt.Errorf("%w: canonical tree exceeds entry limit of %d", ErrLimitExceeded, maxEntries)
		}
		expected, found := index.entries[name]
		if !found {
			return fmt.Errorf("unexpected path %q", name)
		}
		info, err := dirEntry.Info()
		if err != nil {
			return err
		}
		if err := verifyCapturedEntry(ctx, root, expected, info); err != nil {
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

func verifyCapturedEntry(ctx context.Context, root *os.Root, expected capturedEntry, info fs.FileInfo) error {
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
		if err := verifyRegularContent(ctx, root, expected); err != nil {
			return err
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

func verifyRegularContent(ctx context.Context, root *os.Root, expected capturedEntry) error {
	changed := func(detail string) error {
		return fmt.Errorf("path %q changed during capture: %s", expected.name, detail)
	}
	file, err := root.Open(expected.name)
	if err != nil {
		return changed(err.Error())
	}
	openedInfo, statErr := file.Stat()
	if statErr == nil && !os.SameFile(expected.info, openedInfo) {
		statErr = changed("regular descriptor identity differs")
	}
	if statErr == nil && !openedInfo.Mode().IsRegular() {
		statErr = changed("opened descriptor is no longer regular")
	}
	if statErr == nil && openedInfo.Size() != expected.size {
		statErr = changed("opened regular size differs")
	}
	if statErr == nil && int64(openedInfo.Mode().Perm()) != expected.mode {
		statErr = changed("opened regular mode differs")
	}
	if statErr != nil {
		return errors.Join(statErr, file.Close())
	}

	hasher := sha256.New()
	written, readErr := io.Copy(hasher, contextReader{ctx: ctx, reader: file})
	finalInfo, finalStatErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("hangar: verify regular content %q: %w", expected.name, errors.Join(readErr, finalStatErr, closeErr))
	}
	if err := errors.Join(finalStatErr, closeErr); err != nil {
		return fmt.Errorf("hangar: finish verifying regular content %q: %w", expected.name, err)
	}
	if !os.SameFile(expected.info, finalInfo) || !os.SameFile(openedInfo, finalInfo) {
		return changed("regular descriptor identity changed while reading")
	}
	if !finalInfo.Mode().IsRegular() || finalInfo.Size() != expected.size || int64(finalInfo.Mode().Perm()) != expected.mode {
		return changed("regular metadata changed while reading")
	}
	if written != expected.size {
		return changed("regular content size differs")
	}
	var actualHash [sha256.Size]byte
	copy(actualHash[:], hasher.Sum(nil))
	if actualHash != expected.contentHash {
		return changed("regular content differs")
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
