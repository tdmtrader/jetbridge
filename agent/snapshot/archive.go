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

	removeAll           func(string) error
	beforeMaterialize   func(*os.Root, string) error
	beforeCanonicalOpen func(*os.Root, string) error
}

// CapturedTree owns both its extracted root and canonical archive. Call Close
// when neither is needed; Close is safe to call repeatedly.
type CapturedTree struct {
	Root        string
	ArchivePath string
	Digest      Digest
	ByteSize    int64
	FileCount   int64

	privateRoot string
	closeMu     sync.Mutex
	closed      bool
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

	privateRoot, err := os.MkdirTemp(c.TempDir, "concourse-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("snapshot: create private capture directory: %w", err)
	}
	removeAll := c.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	var extractionRoot *os.Root
	defer func() {
		if extractionRoot != nil {
			err = errors.Join(err, extractionRoot.Close())
		}
		if err != nil || tree == nil {
			tree = nil
			err = errors.Join(err, removeAll(privateRoot))
		}
	}()
	stopCancelClose := closeReadCloserOnCancel(ctx, rawTar)
	defer stopCancelClose()

	root := filepath.Join(privateRoot, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, fmt.Errorf("snapshot: create extraction root: %w", err)
	}
	extractionRoot, err = os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open extraction root: %w", err)
	}
	if err := extractTar(ctx, rawTar, extractionRoot, maxEntries, maxContent, c.beforeMaterialize); err != nil {
		return nil, err
	}

	archivePath := filepath.Join(privateRoot, "canonical.tar")
	digest, byteSize, fileCount, err := writeCanonicalTar(ctx, extractionRoot, archivePath, maxEntries, c.beforeCanonicalOpen)
	if err != nil {
		return nil, err
	}

	tree = &CapturedTree{
		Root:        root,
		ArchivePath: archivePath,
		Digest:      digest,
		ByteSize:    byteSize,
		FileCount:   fileCount,
		privateRoot: privateRoot,
		removeAll:   removeAll,
	}
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

type extractedKind uint8

const (
	extractedRegular extractedKind = iota + 1
	extractedDirectory
	extractedSymlink
)

func extractTar(
	ctx context.Context,
	rawTar io.Reader,
	root *os.Root,
	maxEntries int64,
	maxContent int64,
	beforeMaterialize func(*os.Root, string) error,
) error {
	tr := tar.NewReader(contextReader{ctx: ctx, reader: rawTar})
	seenHeaders := make(map[string]struct{})
	materialized := make(map[string]extractedKind)
	var contentBytes int64

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("snapshot: read tar header: %w", err)
		}
		if err := validateHeader(hdr); err != nil {
			return err
		}
		name := hdr.Name
		if _, exists := seenHeaders[name]; exists {
			return fmt.Errorf("snapshot: duplicate canonical path %q", name)
		}
		seenHeaders[name] = struct{}{}
		kind := headerKind(hdr.Typeflag)
		planned, err := planMaterialization(name, kind, materialized)
		if err != nil {
			return err
		}
		if int64(len(materialized))+int64(len(planned)) > maxEntries {
			return fmt.Errorf("snapshot: archive exceeds entry limit of %d", maxEntries)
		}
		for _, entry := range planned {
			if entry.name == name {
				continue
			}
			if err := materializeDirectory(root, entry.name, beforeMaterialize); err != nil {
				return fmt.Errorf("snapshot: extract implicit directory %q: %w", entry.name, err)
			}
			materialized[entry.name] = extractedDirectory
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if err := extractRegular(ctx, tr, root, hdr, maxContent, &contentBytes, beforeMaterialize); err != nil {
				return err
			}
			materialized[name] = extractedRegular
		case tar.TypeDir:
			isNew := isPlanned(planned, name)
			var err error
			if isNew {
				err = materializeDirectory(root, name, beforeMaterialize)
			} else {
				err = normalizeExistingDirectory(root, name)
			}
			if err != nil {
				return fmt.Errorf("snapshot: extract directory %q: %w", name, err)
			}
			materialized[name] = extractedDirectory
		case tar.TypeSymlink:
			target, err := cleanSymlinkTarget(name, hdr.Linkname)
			if err != nil {
				return err
			}
			if beforeMaterialize != nil {
				if err := beforeMaterialize(root, name); err != nil {
					return fmt.Errorf("snapshot: before materializing %q: %w", name, err)
				}
			}
			if err := validateHostParents(root, name); err != nil {
				return err
			}
			if err := ensureUnmaterialized(root, name); err != nil {
				return err
			}
			if err := root.Symlink(target, name); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return hostEquivalentCollision(name, err)
				}
				return fmt.Errorf("snapshot: create symlink %q: %w", name, err)
			}
			materialized[name] = extractedSymlink
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

func planMaterialization(name string, kind extractedKind, materialized map[string]extractedKind) ([]materialization, error) {
	segments := strings.Split(name, "/")
	planned := make([]materialization, 0, len(segments))
	for i := 1; i < len(segments); i++ {
		parent := strings.Join(segments[:i], "/")
		if existing, found := materialized[parent]; found {
			if existing == extractedSymlink {
				return nil, fmt.Errorf("snapshot: path %q has symlink parent %q", name, parent)
			}
			if existing != extractedDirectory {
				return nil, fmt.Errorf("snapshot: path %q has non-directory parent %q", name, parent)
			}
			continue
		}
		planned = append(planned, materialization{name: parent})
	}
	if existing, found := materialized[name]; found {
		if existing == extractedDirectory && kind == extractedDirectory {
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

func materializeDirectory(root *os.Root, name string, beforeMaterialize func(*os.Root, string) error) error {
	if beforeMaterialize != nil {
		if err := beforeMaterialize(root, name); err != nil {
			return fmt.Errorf("snapshot: before materializing %q: %w", name, err)
		}
	}
	if err := validateHostParents(root, name); err != nil {
		return err
	}
	if err := ensureUnmaterialized(root, name); err != nil {
		return err
	}
	if err := root.Mkdir(name, 0755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return hostEquivalentCollision(name, err)
		}
		return err
	}
	return normalizeExistingDirectory(root, name)
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

func normalizeExistingDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is not the materialized directory")
	}
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	openedInfo, statErr := directory.Stat()
	if statErr == nil && (!openedInfo.IsDir() || !os.SameFile(info, openedInfo)) {
		statErr = fmt.Errorf("directory changed while opening")
	}
	chmodErr := error(nil)
	if statErr == nil {
		chmodErr = directory.Chmod(0755)
	}
	return errors.Join(statErr, chmodErr, directory.Close())
}

func extractRegular(
	ctx context.Context,
	tr *tar.Reader,
	root *os.Root,
	hdr *tar.Header,
	maxContent int64,
	contentBytes *int64,
	beforeMaterialize func(*os.Root, string) error,
) error {
	remaining := maxContent - *contentBytes
	if remaining < 0 {
		return fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	if beforeMaterialize != nil {
		if err := beforeMaterialize(root, hdr.Name); err != nil {
			return fmt.Errorf("snapshot: before materializing %q: %w", hdr.Name, err)
		}
	}
	if err := validateHostParents(root, hdr.Name); err != nil {
		return err
	}
	if err := ensureUnmaterialized(root, hdr.Name); err != nil {
		return err
	}
	file, err := root.OpenFile(hdr.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return hostEquivalentCollision(hdr.Name, err)
		}
		return fmt.Errorf("snapshot: create regular file %q: %w", hdr.Name, err)
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
		return errors.Join(fmt.Errorf("snapshot: validate regular file %q: %w", hdr.Name, err), file.Close())
	}

	reader := io.Reader(contextReader{ctx: ctx, reader: tr})
	if remaining != int64(^uint64(0)>>1) {
		reader = io.LimitReader(reader, remaining+1)
	}
	written, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("snapshot: extract regular file %q: %w", hdr.Name, errors.Join(copyErr, closeErr))
	}
	if closeErr != nil {
		return fmt.Errorf("snapshot: close regular file %q: %w", hdr.Name, closeErr)
	}
	if written > remaining {
		return fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	*contentBytes += written
	if written != hdr.Size {
		return fmt.Errorf("snapshot: regular file %q is truncated: copied %d of %d bytes", hdr.Name, written, hdr.Size)
	}
	return nil
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

type canonicalEntry struct {
	name string
	info fs.FileInfo
}

func writeCanonicalTar(
	ctx context.Context,
	root *os.Root,
	archivePath string,
	maxEntries int64,
	beforeCanonicalOpen func(*os.Root, string) error,
) (Digest, int64, int64, error) {
	entries := make([]canonicalEntry, 0)
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if int64(len(entries)) >= maxEntries {
			return fmt.Errorf("snapshot: canonical tree exceeds entry limit of %d", maxEntries)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, canonicalEntry{name: name, info: info})
		return nil
	})
	if err != nil {
		return "", 0, 0, fmt.Errorf("snapshot: walk extracted tree: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", 0, 0, fmt.Errorf("snapshot: create canonical archive: %w", err)
	}
	hash := sha256.New()
	counted := &countingWriter{writer: io.MultiWriter(archive, hash)}
	tw := tar.NewWriter(counted)

	writeErr := writeCanonicalEntries(ctx, tw, root, entries, beforeCanonicalOpen)
	tarCloseErr := tw.Close()
	archiveCloseErr := archive.Close()
	writeErr = errors.Join(writeErr, tarCloseErr, archiveCloseErr)
	if writeErr != nil {
		return "", 0, 0, fmt.Errorf("snapshot: write canonical archive: %w", writeErr)
	}
	digest := Digest(fmt.Sprintf("sha256:%x", hash.Sum(nil)))
	if err := digest.Validate(); err != nil {
		return "", 0, 0, err
	}
	return digest, counted.count, int64(len(entries)), nil
}

func writeCanonicalEntries(
	ctx context.Context,
	tw *tar.Writer,
	root *os.Root,
	entries []canonicalEntry,
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
		var regular *os.File
		switch {
		case entry.info.Mode().IsRegular():
			if beforeCanonicalOpen != nil {
				if err := beforeCanonicalOpen(root, entry.name); err != nil {
					return fmt.Errorf("before canonical open %q: %w", entry.name, err)
				}
			}
			file, err := root.Open(entry.name)
			if err != nil {
				return err
			}
			openedInfo, statErr := file.Stat()
			if statErr == nil && regularFileChanged(entry.info, openedInfo) {
				statErr = fmt.Errorf("file %q changed during capture", entry.name)
			}
			if statErr != nil {
				return errors.Join(statErr, file.Close())
			}
			regular = file
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = 0644
			if openedInfo.Mode().Perm()&0111 != 0 {
				hdr.Mode = 0755
			}
			hdr.Size = openedInfo.Size()
		case entry.info.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0755
		case entry.info.Mode()&os.ModeSymlink != 0:
			target, err := root.Readlink(entry.name)
			if err != nil {
				return err
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Mode = 0777
			hdr.Linkname = target
		default:
			return fmt.Errorf("unsupported extracted file type for %q", entry.name)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			if regular != nil {
				return errors.Join(err, regular.Close())
			}
			return err
		}
		if regular == nil {
			continue
		}
		written, copyErr := io.Copy(tw, contextReader{ctx: ctx, reader: regular})
		finalInfo, statErr := regular.Stat()
		closeErr := regular.Close()
		if copyErr != nil {
			return errors.Join(copyErr, statErr, closeErr)
		}
		if statErr == nil && regularFileChanged(entry.info, finalInfo) {
			statErr = fmt.Errorf("file %q changed during capture", entry.name)
		}
		if written != hdr.Size && statErr == nil {
			statErr = fmt.Errorf("file %q changed during capture", entry.name)
		}
		if err := errors.Join(statErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func regularFileChanged(walked, opened fs.FileInfo) bool {
	return !opened.Mode().IsRegular() ||
		opened.Size() != walked.Size() ||
		opened.Mode().Perm() != walked.Mode().Perm() ||
		!os.SameFile(walked, opened)
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
