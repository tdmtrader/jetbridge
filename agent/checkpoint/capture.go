package checkpoint

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
)

// ArchiveRequest identifies the two server-approved root sets. All paths are
// relative to a daemon-anchored container directory; callers cannot supply a
// host path through this contract.
type ArchiveRequest struct {
	ContainerHandle string   `json:"container_handle"`
	WorkspaceRoots  []string `json:"workspace_roots"`
	SessionRoots    []string `json:"session_roots"`
	MaxBytes        int64    `json:"max_bytes"`
}

func (r ArchiveRequest) Validate() error {
	if !validCaptureHandle(r.ContainerHandle) || r.MaxBytes <= 0 {
		return errors.New("checkpoint: invalid archive request")
	}
	if err := validateCaptureRoots("workspace", r.WorkspaceRoots); err != nil {
		return err
	}
	if err := validateCaptureRoots("session", r.SessionRoots); err != nil {
		return err
	}
	for _, workspace := range r.WorkspaceRoots {
		for _, session := range r.SessionRoots {
			if captureRootsOverlap(workspace, session) {
				return errors.New("checkpoint: workspace and session roots overlap")
			}
		}
	}
	return nil
}

// ArchiveSource is daemon-internal authority. Its absolute root and limits
// are derived by Jetbridge/daemon composition, never by an agent process.
type ArchiveSource struct {
	ContainerRoot string
	ExcludedRoots []string
	MaxBytes      int64
	MaxEntries    int64
	TempDir       string
}

func (s ArchiveSource) validate() error {
	if !filepath.IsAbs(s.ContainerRoot) || filepath.Clean(s.ContainerRoot) != s.ContainerRoot || s.MaxBytes <= 0 || s.MaxEntries <= 0 {
		return errors.New("checkpoint: invalid server archive authority")
	}
	for _, root := range s.ExcludedRoots {
		if !validCaptureRoot(root) {
			return fmt.Errorf("checkpoint: invalid excluded root %q", root)
		}
	}
	return nil
}

type PreparedArchive struct {
	Handle string        `json:"handle"`
	Digest hangar.Digest `json:"digest"`
	Files  int           `json:"files"`
	Bytes  int64         `json:"bytes"`
}

func (p PreparedArchive) Validate() error {
	if !validCaptureHandle(p.Handle) || p.Files <= 0 || p.Bytes <= 0 {
		return errors.New("checkpoint: invalid prepared archive")
	}
	return p.Digest.Validate()
}

type ArchiveResult struct {
	Object hangar.Attributes `json:"object"`
	Files  int               `json:"files"`
	Bytes  int64             `json:"bytes"`
}

type CapturedArchive struct {
	Prepared   PreparedArchive
	PreparedAt time.Time
	tree       *snapshot.CapturedTree
}

func (a *CapturedArchive) Open() (*os.File, error) {
	if a == nil || a.tree == nil {
		return nil, errors.New("checkpoint: captured archive is unavailable")
	}
	return os.Open(a.tree.ArchivePath)
}

func (a *CapturedArchive) Close() error {
	if a == nil || a.tree == nil {
		return nil
	}
	return a.tree.Close()
}

// CaptureArchive creates a deterministic, canonical combined tar. It opens
// the container root once and uses descriptor-relative operations thereafter.
func CaptureArchive(ctx context.Context, request ArchiveRequest, source ArchiveSource) (*CapturedArchive, error) {
	if ctx == nil {
		return nil, errors.New("checkpoint: capture context is required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := source.validate(); err != nil {
		return nil, err
	}
	for _, excluded := range source.ExcludedRoots {
		for _, declared := range append(append([]string{}, request.WorkspaceRoots...), request.SessionRoots...) {
			if captureRootsOverlap(excluded, declared) {
				return nil, fmt.Errorf("checkpoint: declared root %q overlaps excluded root %q", declared, excluded)
			}
		}
	}

	root, err := openCaptureRoot(source.ContainerRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	maxBytes := minCapture(request.MaxBytes, source.MaxBytes)
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := writeCaptureTar(ctx, root, request, source.MaxEntries, writer)
		done <- errors.Join(err, writer.CloseWithError(err))
	}()
	tree, captureErr := (snapshot.Canonicalizer{MaxEntries: source.MaxEntries, MaxContentBytes: maxBytes, TempDir: source.TempDir}).Capture(ctx, reader)
	readErr := reader.Close()
	writeErr := <-done
	if captureErr != nil && errors.Is(writeErr, io.ErrClosedPipe) {
		writeErr = nil
	}
	if err := errors.Join(captureErr, readErr, writeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, err
	}
	files, err := countCaptureEntries(tree)
	if err != nil {
		_ = tree.Close()
		return nil, err
	}
	prepared := PreparedArchive{Handle: newCaptureHandle(), Digest: hangar.Digest(tree.Digest), Files: files, Bytes: tree.ByteSize}
	if err := prepared.Validate(); err != nil {
		_ = tree.Close()
		return nil, err
	}
	return &CapturedArchive{Prepared: prepared, PreparedAt: time.Now().UTC(), tree: tree}, nil
}

func writeCaptureTar(ctx context.Context, root *os.Root, request ArchiveRequest, limit int64, destination io.Writer) (err error) {
	tw := tar.NewWriter(destination)
	defer func() { err = errors.Join(err, tw.Close()) }()
	remaining := limit
	for _, entry := range []struct {
		tree  string
		roots []string
	}{{"workspace", request.WorkspaceRoots}, {"session", request.SessionRoots}} {
		roots := append([]string(nil), entry.roots...)
		sort.Strings(roots)
		for _, sourcePath := range roots {
			target := entry.tree
			if len(roots) > 1 {
				target = path.Join(target, sourcePath)
			}
			if err := writeCaptureDirectory(ctx, root, sourcePath, target, &remaining, limit, tw); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCaptureDirectory(ctx context.Context, root *os.Root, sourcePath, archivePath string, remaining *int64, limit int64, tw *tar.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := root.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("checkpoint: inspect %q: %w", sourcePath, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || privilegedCaptureMode(before.Mode()) {
		return fmt.Errorf("checkpoint: unsafe directory %q", sourcePath)
	}
	dir, err := root.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("checkpoint: open %q: %w", sourcePath, err)
	}
	opened, statErr := dir.Stat()
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if statErr != nil || readErr != nil || closeErr != nil || !sameCaptureFile(before, opened) {
		return errors.Join(statErr, readErr, closeErr, errors.New("checkpoint: directory changed while opening"))
	}
	if err := consumeCaptureEntry(remaining, limit); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: archivePath, Typeflag: tar.TypeDir, Mode: int64(before.Mode().Perm())}); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	beforeEntries := captureEntryNames(entries)
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return errors.New("checkpoint: unsafe directory entry")
		}
		child := path.Join(sourcePath, name)
		info, err := root.Lstat(child)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := writeCaptureDirectory(ctx, root, child, path.Join(archivePath, name), remaining, limit, tw); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := writeCaptureFile(ctx, root, child, path.Join(archivePath, name), info, remaining, limit, tw); err != nil {
				return err
			}
		default:
			return fmt.Errorf("checkpoint: unsafe source entry %q", child)
		}
	}
	after, err := root.Lstat(sourcePath)
	if err != nil || !sameCaptureFile(before, after) || !sameCaptureEntries(root, sourcePath, beforeEntries) {
		return errors.New("checkpoint: source directory changed while reading")
	}
	return nil
}

func writeCaptureFile(ctx context.Context, root *os.Root, sourcePath, archivePath string, before os.FileInfo, remaining *int64, limit int64, tw *tar.Writer) error {
	if privilegedCaptureMode(before.Mode()) {
		return fmt.Errorf("checkpoint: privileged file %q", sourcePath)
	}
	f, err := root.Open(sourcePath)
	if err != nil {
		return err
	}
	opened, statErr := f.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !sameCaptureFile(before, opened) {
		_ = f.Close()
		return errors.New("checkpoint: source file changed while opening")
	}
	if err := consumeCaptureEntry(remaining, limit); err != nil {
		_ = f.Close()
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: archivePath, Typeflag: tar.TypeReg, Mode: int64(opened.Mode().Perm()), Size: opened.Size()}); err != nil {
		_ = f.Close()
		return err
	}
	written, copyErr := io.CopyN(tw, contextCaptureReader{ctx, f}, opened.Size())
	afterOpen, afterErr := f.Stat()
	closeErr := f.Close()
	after, lstatErr := root.Lstat(sourcePath)
	if copyErr != nil || written != opened.Size() || afterErr != nil || closeErr != nil || lstatErr != nil || !sameCaptureFile(opened, afterOpen) || !sameCaptureFile(opened, after) {
		return errors.New("checkpoint: source file changed while reading")
	}
	return nil
}

func openCaptureRoot(rootPath string) (*os.Root, error) {
	before, err := os.Lstat(rootPath)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("checkpoint: container root is unsafe")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !sameCaptureFile(before, after) {
		_ = root.Close()
		return nil, errors.New("checkpoint: container root changed while opening")
	}
	return root, nil
}

func validCaptureHandle(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
func validCaptureRoot(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\\\x00") && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && value != ".." && !strings.Contains(value, "../")
}
func validateCaptureRoots(kind string, roots []string) error {
	if len(roots) == 0 {
		return fmt.Errorf("checkpoint: %s roots are required", kind)
	}
	for i, root := range roots {
		if !validCaptureRoot(root) {
			return fmt.Errorf("checkpoint: invalid %s root", kind)
		}
		for _, other := range roots[:i] {
			if captureRootsOverlap(root, other) {
				return fmt.Errorf("checkpoint: overlapping %s roots", kind)
			}
		}
	}
	return nil
}
func captureRootsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
func minCapture(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
func privilegedCaptureMode(mode os.FileMode) bool { return mode&(os.ModeSetuid|os.ModeSetgid) != 0 }
func sameCaptureFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
func consumeCaptureEntry(remaining *int64, limit int64) error {
	if *remaining <= 0 {
		return fmt.Errorf("checkpoint: archive exceeds entry limit of %d", limit)
	}
	*remaining--
	return nil
}
func captureEntryNames(entries []fs.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
func sameCaptureEntries(root *os.Root, sourcePath string, expected []string) bool {
	d, err := root.Open(sourcePath)
	if err != nil {
		return false
	}
	entries, readErr := d.ReadDir(-1)
	closeErr := d.Close()
	if readErr != nil || closeErr != nil || len(entries) != len(expected) {
		return false
	}
	names := captureEntryNames(entries)
	sort.Strings(names)
	for i := range names {
		if names[i] != expected[i] {
			return false
		}
	}
	return true
}
func countCaptureEntries(tree *snapshot.CapturedTree) (int, error) {
	file, err := os.Open(tree.ArchivePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	count := 0
	for {
		_, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
		count++
	}
}
func newCaptureHandle() string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return "prepared-" + hex.EncodeToString(raw[:])
}

type contextCaptureReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextCaptureReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
