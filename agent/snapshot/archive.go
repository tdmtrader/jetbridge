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
type Canonicalizer struct {
	MaxEntries      int64
	MaxContentBytes int64

	// TempDir selects the parent of the private capture directory. It is useful
	// for constraining storage and observing cleanup; an empty value uses the
	// operating system's default temporary directory.
	TempDir string
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
	closeOnce   sync.Once
	closeErr    error
}

func (t *CapturedTree) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		if t.privateRoot != "" {
			t.closeErr = os.RemoveAll(t.privateRoot)
		}
	})
	return t.closeErr
}

// Capture extracts rawTar into a private directory, emits a canonical tar next
// to it, and hashes the exact emitted bytes. No caller-visible ownership is
// transferred unless all extraction and canonicalization steps succeed.
func (c Canonicalizer) Capture(ctx context.Context, rawTar io.Reader) (*CapturedTree, error) {
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
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(privateRoot)
		}
	}()

	root := filepath.Join(privateRoot, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, fmt.Errorf("snapshot: create extraction root: %w", err)
	}
	if err := extractTar(ctx, rawTar, root, maxEntries, maxContent); err != nil {
		return nil, err
	}

	archivePath := filepath.Join(privateRoot, "canonical.tar")
	digest, byteSize, fileCount, err := writeCanonicalTar(ctx, root, archivePath, maxEntries)
	if err != nil {
		return nil, err
	}

	tree := &CapturedTree{
		Root:        root,
		ArchivePath: archivePath,
		Digest:      digest,
		ByteSize:    byteSize,
		FileCount:   fileCount,
		privateRoot: privateRoot,
	}
	succeeded = true
	return tree, nil
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

func extractTar(ctx context.Context, rawTar io.Reader, root string, maxEntries, maxContent int64) error {
	tr := tar.NewReader(contextReader{ctx: ctx, reader: rawTar})
	seen := make(map[string]extractedKind)
	var acceptedEntries int64
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
		if _, exists := seen[name]; exists {
			return fmt.Errorf("snapshot: duplicate canonical path %q", name)
		}
		if acceptedEntries >= maxEntries {
			return fmt.Errorf("snapshot: archive exceeds entry limit of %d", maxEntries)
		}
		acceptedEntries++

		if err := validateKnownParents(name, seen); err != nil {
			return err
		}
		if err := ensureHostParents(root, name); err != nil {
			return err
		}
		hostPath := filepath.Join(root, filepath.FromSlash(name))

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if err := extractRegular(ctx, tr, hostPath, hdr, maxContent, &contentBytes); err != nil {
				return err
			}
			seen[name] = extractedRegular
		case tar.TypeDir:
			if err := extractDirectory(hostPath); err != nil {
				return fmt.Errorf("snapshot: extract directory %q: %w", name, err)
			}
			seen[name] = extractedDirectory
		case tar.TypeSymlink:
			target, err := cleanSymlinkTarget(name, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, hostPath); err != nil {
				return fmt.Errorf("snapshot: symlink %q conflicts with extracted path: %w", name, err)
			}
			seen[name] = extractedSymlink
		}
	}
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
			if value != hdr.Name {
				return fmt.Errorf("snapshot: inconsistent PAX path metadata for %q", hdr.Name)
			}
		case "linkpath":
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

func validateKnownParents(name string, seen map[string]extractedKind) error {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		kind, exists := seen[parent]
		if !exists {
			continue
		}
		switch kind {
		case extractedSymlink:
			return fmt.Errorf("snapshot: path %q has symlink parent %q", name, parent)
		case extractedRegular:
			return fmt.Errorf("snapshot: path %q has non-directory parent %q", name, parent)
		}
	}
	return nil
}

func ensureHostParents(root, name string) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	current := root
	for _, segment := range strings.Split(parent, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0755); err != nil {
				return fmt.Errorf("snapshot: create parent for %q: %w", name, err)
			}
			if err := os.Chmod(current, 0755); err != nil {
				return fmt.Errorf("snapshot: normalize parent for %q: %w", name, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("snapshot: inspect parent for %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot: path %q has symlink parent %q", name, segment)
		}
		if !info.IsDir() {
			return fmt.Errorf("snapshot: path %q has non-directory parent", name)
		}
	}
	return nil
}

func extractRegular(ctx context.Context, tr *tar.Reader, hostPath string, hdr *tar.Header, maxContent int64, contentBytes *int64) error {
	remaining := maxContent - *contentBytes
	if remaining < 0 {
		return fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	file, err := os.OpenFile(hostPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("snapshot: regular file %q conflicts with extracted path: %w", hdr.Name, err)
	}

	reader := io.Reader(contextReader{ctx: ctx, reader: tr})
	if remaining < int64(^uint64(0)>>1) {
		reader = io.LimitReader(reader, remaining+1)
	}
	written, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	*contentBytes += written
	if copyErr != nil {
		return fmt.Errorf("snapshot: extract regular file %q: %w", hdr.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("snapshot: close regular file %q: %w", hdr.Name, closeErr)
	}
	if written > remaining {
		return fmt.Errorf("snapshot: archive exceeds regular content limit of %d bytes", maxContent)
	}
	if written != hdr.Size {
		return fmt.Errorf("snapshot: regular file %q is truncated: copied %d of %d bytes", hdr.Name, written, hdr.Size)
	}
	mode := os.FileMode(0644)
	if hdr.Mode&0111 != 0 {
		mode = 0755
	}
	if err := os.Chmod(hostPath, mode); err != nil {
		return fmt.Errorf("snapshot: normalize regular file %q: %w", hdr.Name, err)
	}
	return nil
}

func extractDirectory(hostPath string) error {
	info, err := os.Lstat(hostPath)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(hostPath, 0755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("conflicts with non-directory path")
	}
	return os.Chmod(hostPath, 0755)
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

func writeCanonicalTar(ctx context.Context, root, archivePath string, maxEntries int64) (Digest, int64, int64, error) {
	entries := make([]canonicalEntry, 0)
	err := filepath.WalkDir(root, func(hostPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if hostPath == root {
			return nil
		}
		if int64(len(entries)) >= maxEntries {
			return fmt.Errorf("snapshot: canonical tree exceeds entry limit of %d", maxEntries)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, hostPath)
		if err != nil {
			return err
		}
		entries = append(entries, canonicalEntry{name: filepath.ToSlash(relative), info: info})
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

	writeErr := writeCanonicalEntries(ctx, tw, root, entries)
	if closeErr := tw.Close(); writeErr == nil && closeErr != nil {
		writeErr = closeErr
	}
	if closeErr := archive.Close(); writeErr == nil && closeErr != nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", 0, 0, fmt.Errorf("snapshot: write canonical archive: %w", writeErr)
	}
	digest := Digest(fmt.Sprintf("sha256:%x", hash.Sum(nil)))
	if err := digest.Validate(); err != nil {
		return "", 0, 0, err
	}
	return digest, counted.count, int64(len(entries)), nil
}

func writeCanonicalEntries(ctx context.Context, tw *tar.Writer, root string, entries []canonicalEntry) error {
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
			Format:     tar.FormatGNU,
		}
		hostPath := filepath.Join(root, filepath.FromSlash(entry.name))
		switch {
		case entry.info.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = 0644
			if entry.info.Mode().Perm()&0111 != 0 {
				hdr.Mode = 0755
			}
			hdr.Size = entry.info.Size()
		case entry.info.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0755
		case entry.info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(hostPath)
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
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		file, err := os.Open(hostPath)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(tw, contextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != hdr.Size {
			return fmt.Errorf("file %q changed during capture", entry.name)
		}
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
	return r.reader.Read(p)
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
