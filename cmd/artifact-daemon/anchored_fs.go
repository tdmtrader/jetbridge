package main

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const daemonStagingDirectory = ".artifact-daemon-staging"

func openDirectoryNoFollow(name string) (*os.File, error) {
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openDaemonStagingAt(storage *os.File) (*os.File, error) {
	if err := unix.Mkdirat(int(storage.Fd()), daemonStagingDirectory, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	staging, err := openDirAtNoFollow(storage, daemonStagingDirectory, false)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(staging.Fd()), &stat); err != nil {
		staging.Close()
		return nil, err
	}
	if int(stat.Uid) != os.Geteuid() {
		staging.Close()
		return nil, fmt.Errorf("daemon staging directory is not owned by the daemon user")
	}
	if err := unix.Fchmod(int(staging.Fd()), 0700); err != nil {
		staging.Close()
		return nil, err
	}
	unchanged, err := sameOpenDirectoryAt(storage, daemonStagingDirectory, staging)
	if err != nil || !unchanged {
		staging.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("daemon staging directory changed while opening")
	}
	return staging, nil
}

// openRootAt adapts an already-open directory descriptor to os.Root without
// resolving the directory's pathname again. Both Linux and Darwin expose open
// descriptors through one of these process-local paths; OpenRoot follows that
// link once and then retains its own descriptor-rooted authority.
func openRootAt(directory *os.File) (*os.Root, error) {
	var errs []error
	for _, prefix := range []string{"/proc/self/fd", "/dev/fd"} {
		root, err := os.OpenRoot(fmt.Sprintf("%s/%d", prefix, directory.Fd()))
		if err == nil {
			return root, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("open os.Root from directory descriptor: %w", errors.Join(errs...))
}

func openPathAtNoFollow(base *os.File, rel string) (*os.File, os.FileInfo, error) {
	slashRel := strings.ReplaceAll(rel, string(os.PathSeparator), "/")
	if err := validateCanonicalRelativeKey(slashRel); err != nil {
		return nil, nil, err
	}
	parent, err := openDirAtNoFollow(base, path.Dir(slashRel), false)
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), path.Base(slashRel), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), rel)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, fmt.Errorf("artifact path is not a regular file or directory")
	}
	return file, info, nil
}

func tarOpenedDirectory(w io.Writer, directory *os.File) error {
	tw := tar.NewWriter(w)
	if err := writeOpenedTarTree(tw, directory, ""); err != nil {
		return err
	}
	return tw.Close()
}

func writeOpenedTarTree(tw *tar.Writer, directory *os.File, prefix string) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return fmt.Errorf("unsafe artifact entry name %q", name)
		}
		entryPath := name
		if prefix != "" {
			entryPath = path.Join(prefix, name)
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			child := os.NewFile(uintptr(fd), entryPath)
			info, statErr := child.Stat()
			if statErr != nil {
				child.Close()
				return statErr
			}
			if err := tw.WriteHeader(&tar.Header{Name: entryPath + "/", Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()}); err != nil {
				child.Close()
				return err
			}
			recurseErr := writeOpenedTarTree(tw, child, entryPath)
			closeErr := child.Close()
			if recurseErr != nil || closeErr != nil {
				return errors.Join(recurseErr, closeErr)
			}
		case unix.S_IFREG:
			fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(fd), entryPath)
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				file.Close()
				return fmt.Errorf("artifact entry %q changed type while streaming", entryPath)
			}
			if err := tw.WriteHeader(&tar.Header{Name: entryPath, Typeflag: tar.TypeReg, Size: info.Size(), Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()}); err != nil {
				file.Close()
				return err
			}
			_, copyErr := io.CopyN(tw, file, info.Size())
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		case unix.S_IFLNK:
			target, err := readlinkAt(int(directory.Fd()), name)
			if err != nil {
				return err
			}
			if err := validateReproducibleSymlink(entryPath, target); err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{Name: entryPath, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0777}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("artifact entry %q has unsupported file type", entryPath)
		}
	}
	return nil
}

// openDirAtNoFollow opens each component relative to an already-open
// directory. No component may be a symlink. When create is true, missing
// components are created before being opened with the same no-follow rule.
func openDirAtNoFollow(base *os.File, rel string, create bool) (*os.File, error) {
	if rel == "" || rel == "." {
		dup, err := unix.Dup(int(base.Fd()))
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(dup), base.Name()), nil
	}
	if err := validateCanonicalRelativeKey(strings.ReplaceAll(rel, string(os.PathSeparator), "/")); err != nil {
		return nil, err
	}
	dup, err := unix.Dup(int(base.Fd()))
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(dup), base.Name())
	for _, segment := range strings.Split(strings.ReplaceAll(rel, string(os.PathSeparator), "/"), "/") {
		fd, openErr := unix.Openat(int(current.Fd()), segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), segment, 0755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				current.Close()
				return nil, mkdirErr
			}
			fd, openErr = unix.Openat(int(current.Fd()), segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(fd), segment)
		current.Close()
		current = next
	}
	return current, nil
}

func sameOpenDirectoryAt(base *os.File, rel string, opened *os.File) (bool, error) {
	fresh, err := openDirAtNoFollow(base, rel, false)
	if err != nil {
		return false, err
	}
	defer fresh.Close()
	var first, second unix.Stat_t
	if err := unix.Fstat(int(opened.Fd()), &first); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(fresh.Fd()), &second); err != nil {
		return false, err
	}
	return first.Dev == second.Dev && first.Ino == second.Ino, nil
}

func sameOpenEntryAt(parent *os.File, name string, opened *os.File) (bool, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return false, fmt.Errorf("entry name is not a single safe component")
	}
	var named, descriptor unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(opened.Fd()), &descriptor); err != nil {
		return false, err
	}
	return named.Dev == descriptor.Dev && named.Ino == descriptor.Ino && named.Mode&unix.S_IFMT == descriptor.Mode&unix.S_IFMT, nil
}

func randomDirectoryAt(parent *os.File, prefix string) (string, *os.File, error) {
	var random [16]byte
	for range 128 {
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", nil, err
		}
		opened, err := openDirAtNoFollow(parent, name, false)
		if err != nil {
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, err
		}
		return name, opened, nil
	}
	return "", nil, fmt.Errorf("could not allocate temporary artifact directory")
}

func randomFileAt(parent *os.File, prefix string, mode uint32) (string, *os.File, error) {
	var random [16]byte
	for range 128 {
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := prefix + hex.EncodeToString(random[:])
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		if err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", nil, err
		}
		return name, os.NewFile(uintptr(fd), name), nil
	}
	return "", nil, fmt.Errorf("could not allocate temporary artifact file")
}

func writeExclusiveFileAt(directory *os.File, name string, contents []byte, mode uint32) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("file name is not a single safe component")
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(contents)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return errors.Join(writeErr, syncErr, closeErr)
	}
	return unix.Fsync(int(directory.Fd()))
}

// publishPreparedDirectoryAt publishes a complete private directory into a
// destination. If the destination does not yet exist, publication is one
// rename. If it already exists (the Kubernetes hostPath case), its directory
// inode must be preserved because kubelet has bind-mounted that exact inode
// into the waiting pod. In that case entries are descriptor-moved into the
// anchored destination and the token receipt is moved last as the commit
// marker. The main container cannot start until init verifies that marker.
func publishPreparedDirectoryAt(ctx context.Context, sourceParent *os.File, temporaryName string, temporary *os.File, destinationParent *os.File, destinationName, receiptName string, beforeEntry func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range []string{temporaryName, destinationName} {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, '\x00') {
			return fmt.Errorf("publication name is not a single safe component")
		}
	}
	if receiptName != "" && (filepath.Base(receiptName) != receiptName || strings.ContainsRune(receiptName, '\x00')) {
		return fmt.Errorf("receipt name is not a single safe component")
	}
	temporaryUnchanged, err := sameOpenDirectoryAt(sourceParent, temporaryName, temporary)
	if err != nil || !temporaryUnchanged {
		return fmt.Errorf("prepared directory changed before publication: %w", err)
	}

	destination, err := openDirAtNoFollow(destinationParent, destinationName, false)
	if errors.Is(err, unix.ENOENT) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unix.Renameat(int(sourceParent.Fd()), temporaryName, int(destinationParent.Fd()), destinationName); err != nil {
			return err
		}
		return unix.Fsync(int(destinationParent.Fd()))
	}
	if err != nil {
		return err
	}
	defer destination.Close()
	destinationUnchanged, err := sameOpenEntryAt(destinationParent, destinationName, destination)
	if err != nil || !destinationUnchanged {
		return fmt.Errorf("destination directory changed before publication: %w", err)
	}
	if err := removeOpenedDirectoryContentsContext(ctx, destination); err != nil {
		return fmt.Errorf("clear stale destination: %w", err)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind prepared directory: %w", err)
	}
	entries, err := temporary.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("list prepared directory: %w", err)
	}
	receiptFound := receiptName == ""
	move := func(name string) error {
		if beforeEntry != nil {
			beforeEntry()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unix.Renameat(int(temporary.Fd()), name, int(destination.Fd()), name); err != nil {
			cleanupErr := removeOpenedDirectoryContentsContext(ctx, destination)
			return errors.Join(err, cleanupErr)
		}
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			_ = removeOpenedDirectoryContentsContext(ctx, destination)
			return fmt.Errorf("unsafe prepared entry name %q", name)
		}
		if name == receiptName {
			receiptFound = true
			continue
		}
		if err := move(name); err != nil {
			return fmt.Errorf("publish prepared entry %q: %w", name, err)
		}
	}
	if !receiptFound {
		_ = removeOpenedDirectoryContentsContext(ctx, destination)
		return fmt.Errorf("prepared directory is missing its resolve receipt")
	}
	if receiptName != "" {
		if err := move(receiptName); err != nil {
			return fmt.Errorf("publish resolve receipt: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Fsync(int(destination.Fd())); err != nil {
		_ = removeOpenedDirectoryContentsContext(ctx, destination)
		return err
	}
	destinationUnchanged, err = sameOpenEntryAt(destinationParent, destinationName, destination)
	if err != nil || !destinationUnchanged {
		_ = removeOpenedDirectoryContentsContext(ctx, destination)
		return fmt.Errorf("destination directory changed during publication: %w", err)
	}
	temporaryUnchanged, err = sameOpenDirectoryAt(sourceParent, temporaryName, temporary)
	if err != nil || !temporaryUnchanged {
		_ = removeOpenedDirectoryContentsContext(ctx, destination)
		return fmt.Errorf("prepared directory changed during publication: %w", err)
	}
	return unix.Fsync(int(destinationParent.Fd()))
}

func removeOpenedDirectoryContentsContext(ctx context.Context, directory *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return fmt.Errorf("unsafe destination entry name %q", name)
		}
		if err := removeTreeAtContext(ctx, directory, name); err != nil {
			return err
		}
	}
	return nil
}

func copyOpenedTree(ctx context.Context, src, dst *os.File, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := src.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return fmt.Errorf("unsafe artifact entry name %q", name)
		}
		entryPath := name
		if prefix != "" {
			entryPath = path.Join(prefix, name)
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(src.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("inspect artifact entry %q: %w", entryPath, err)
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			srcFD, err := unix.Openat(int(src.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("open artifact directory %q: %w", entryPath, err)
			}
			srcChild := os.NewFile(uintptr(srcFD), entryPath)
			mode := uint32(sanitizeMode(tarDirectoryType, os.FileMode(stat.Mode&0777)).Perm())
			if err := unix.Mkdirat(int(dst.Fd()), name, mode); err != nil {
				srcChild.Close()
				return fmt.Errorf("create artifact directory %q: %w", entryPath, err)
			}
			dstChild, err := openDirAtNoFollow(dst, name, false)
			if err != nil {
				srcChild.Close()
				return fmt.Errorf("open copied directory %q: %w", entryPath, err)
			}
			copyErr := copyOpenedTree(ctx, srcChild, dstChild, entryPath)
			closeErr := errors.Join(srcChild.Close(), dstChild.Close())
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		case unix.S_IFREG:
			srcFD, err := unix.Openat(int(src.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("open artifact file %q: %w", entryPath, err)
			}
			var openedStat unix.Stat_t
			if err := unix.Fstat(srcFD, &openedStat); err != nil || openedStat.Mode&unix.S_IFMT != unix.S_IFREG {
				unix.Close(srcFD)
				return fmt.Errorf("artifact file %q changed type during copy", entryPath)
			}
			mode := uint32(sanitizeMode(tarRegularType, os.FileMode(openedStat.Mode&0777)).Perm())
			dstFD, err := unix.Openat(int(dst.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
			if err != nil {
				unix.Close(srcFD)
				return fmt.Errorf("create artifact file %q: %w", entryPath, err)
			}
			srcFile := os.NewFile(uintptr(srcFD), entryPath)
			dstFile := os.NewFile(uintptr(dstFD), entryPath)
			copyErr := copyOpenedFileContext(ctx, dstFile, srcFile)
			chmodErr := unix.Fchmod(dstFD, mode)
			closeErr := errors.Join(srcFile.Close(), dstFile.Close())
			if copyErr != nil || chmodErr != nil || closeErr != nil {
				return fmt.Errorf("copy artifact file %q: %w", entryPath, errors.Join(copyErr, chmodErr, closeErr))
			}
		case unix.S_IFLNK:
			target, err := readlinkAt(int(src.Fd()), name)
			if err != nil {
				return fmt.Errorf("read artifact symlink %q: %w", entryPath, err)
			}
			if err := validateReproducibleSymlink(entryPath, target); err != nil {
				return err
			}
			if err := unix.Symlinkat(target, int(dst.Fd()), name); err != nil {
				return fmt.Errorf("copy artifact symlink %q: %w", entryPath, err)
			}
		default:
			return fmt.Errorf("artifact entry %q has unsupported file type", entryPath)
		}
	}
	return nil
}

func copyOpenedFileContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// Local constants avoid importing archive/tar into this low-level file while
// retaining the shared permission sanitizer's type distinction.
const (
	tarRegularType   = byte('0')
	tarDirectoryType = byte('5')
)

func readlinkAt(dirfd int, name string) (string, error) {
	for size := 256; size <= 64*1024; size *= 2 {
		buffer := make([]byte, size)
		n, err := unix.Readlinkat(dirfd, name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
	}
	return "", fmt.Errorf("symlink target is too long")
}

// validateReproducibleSymlink is the single symlink rule for every path that
// stores, serves, copies or mirrors an artifact.
//
// A symlink is content, not a path this daemon resolves, so the only targets
// refused are the ones that cannot be reproduced at all: empty, or containing
// NUL. Everything else — including absolute targets — is carried through
// verbatim.
//
// Judging targets by their text was the wrong boundary, and it cost three
// separate outages' worth of rejected-but-legitimate content. Container rootfs
// trees are built from absolute symlinks (/var/spool/mail -> /var/mail ships in
// every Debian-derived image, and busybox points hundreds of applets at one
// binary), so a text rule against them means the daemon cannot hold an image at
// all. Dereferencing instead of reproducing would be worse still: an absolute
// target resolves against *this* process's filesystem, so copying its contents
// turns any artifact into a read primitive against the daemon's own disk.
//
// The boundary that actually matters is that nothing is ever written *through*
// a symlink, and that is enforced structurally rather than textually:
//
//   - extraction writes through an *os.Root, which refuses to resolve any path
//     leaving the root and fails with "path escapes from parent" — proven for
//     absolute and relative-escaping targets in TestOsRootBlocksSymlinkEscape;
//   - copying creates entries with Symlinkat, which never dereferences, and
//     descends only into directories that are real in the source tree;
//   - tarring is read-only: a symlink becomes a header, resolving nothing.
func validateReproducibleSymlink(name, target string) error {
	if target == "" || strings.ContainsRune(target, '\x00') {
		return fmt.Errorf("unrepresentable artifact symlink %q -> %q", name, target)
	}
	return nil
}

func removeTreeAt(parent *os.File, name string) error {
	return removeTreeAtContext(context.Background(), parent, name)
}

func removeTreeAtContext(ctx context.Context, parent *os.File, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(int(parent.Fd()), name, 0)
	}
	directory, err := openDirAtNoFollow(parent, name, false)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	if readErr == nil {
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				readErr = err
				break
			}
			if err := removeTreeAtContext(ctx, directory, entry.Name()); err != nil {
				readErr = err
				break
			}
		}
	}
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}
