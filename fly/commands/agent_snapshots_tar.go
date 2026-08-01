package commands

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

var agentSnapshotEpoch = time.Unix(0, 0).UTC()

func parseAgentSnapshotID(raw string) (string, error) {
	id, err := snapshot.ParseSnapshotID(raw)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// writeAgentSnapshotTarFromRoot emits the portable, deterministic directory
// representation accepted by the snapshot API, archiving the exact directory
// object opened and preflighted by the caller. The server canonicalizer is
// still authoritative; the local checks prevent obviously unsafe entries and
// make retries/content hashes stable before the request leaves Fly. Keeping
// that descriptor anchored across the HTTP setup and stream prevents a
// pathname swap from changing the trust boundary after preflight.
func writeAgentSnapshotTarFromRoot(ctx context.Context, root *os.Root, output io.Writer) (err error) {
	if ctx == nil || root == nil || output == nil {
		return fmt.Errorf("agent snapshot: context, source root, and output are required")
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("agent snapshot: inspect source directory: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("agent snapshot: source must be a directory")
	}

	tw := tar.NewWriter(output)
	defer func() { err = errors.Join(err, tw.Close()) }()

	err = fs.WalkDir(root.FS(), ".", func(walkPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkPath == "." {
			return nil
		}

		name := filepath.ToSlash(walkPath)
		info, err := root.Lstat(walkPath)
		if err != nil {
			return fmt.Errorf("agent snapshot: inspect %q: %w", name, err)
		}
		mode := info.Mode()
		if err := validateAgentSnapshotMode(name, mode); err != nil {
			return err
		}

		hdr := &tar.Header{
			Name:       name,
			Uid:        0,
			Gid:        0,
			Uname:      "",
			Gname:      "",
			ModTime:    agentSnapshotEpoch,
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
			Format:     tar.FormatPAX,
		}

		switch {
		case mode.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		case mode.IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = 0o644
			if mode.Perm()&0o111 != 0 {
				hdr.Mode = 0o755
			}
			hdr.Size = info.Size()
		case mode&os.ModeSymlink != 0:
			target, err := root.Readlink(walkPath)
			if err != nil {
				return fmt.Errorf("agent snapshot: read symlink %q: %w", name, err)
			}
			if err := validateAgentSnapshotSymlink(walkPath, target); err != nil {
				return fmt.Errorf("agent snapshot: symlink %q: %w", name, err)
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Mode = 0o777
			hdr.Linkname = filepath.ToSlash(target)
		default:
			return fmt.Errorf("agent snapshot: unsupported filesystem entry %q (%s)", name, mode.Type())
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("agent snapshot: write header for %q: %w", name, err)
		}
		if !mode.IsRegular() {
			return nil
		}

		file, err := root.Open(walkPath)
		if err != nil {
			return fmt.Errorf("agent snapshot: open %q: %w", name, err)
		}
		copyErr := copyStableAgentSnapshotFile(ctx, root, walkPath, info, file, tw)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	if err != nil {
		return fmt.Errorf("agent snapshot: archive source directory: %w", err)
	}
	return nil
}

func validateAgentSnapshotMode(name string, mode os.FileMode) error {
	if mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("agent snapshot: %q has setuid or setgid permissions", name)
	}
	return nil
}

func validateAgentSnapshotSymlink(linkPath, target string) error {
	if target == "" {
		return fmt.Errorf("empty target is not allowed")
	}
	if filepath.IsAbs(target) {
		return fmt.Errorf("absolute target is not allowed")
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target escapes the source directory")
	}
	return nil
}

func copyStableAgentSnapshotFile(
	ctx context.Context,
	root *os.Root,
	path string,
	initial os.FileInfo,
	file *os.File,
	destination io.Writer,
) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("agent snapshot: inspect opened file %q: %w", filepath.ToSlash(path), err)
	}
	current, err := root.Lstat(path)
	if err != nil {
		return fmt.Errorf("agent snapshot: recheck file %q: %w", filepath.ToSlash(path), err)
	}
	if !opened.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(initial, opened) || !os.SameFile(opened, current) {
		return fmt.Errorf("agent snapshot: file %q changed while it was being archived", filepath.ToSlash(path))
	}

	written, err := io.Copy(destination, contextReader{ctx: ctx, reader: file})
	if err != nil {
		return fmt.Errorf("agent snapshot: stream file %q: %w", filepath.ToSlash(path), err)
	}
	if written != initial.Size() {
		return fmt.Errorf("agent snapshot: file %q changed size while it was being archived", filepath.ToSlash(path))
	}

	final, err := file.Stat()
	if err != nil {
		return fmt.Errorf("agent snapshot: inspect archived file %q: %w", filepath.ToSlash(path), err)
	}
	finalPath, err := root.Lstat(path)
	if err != nil {
		return fmt.Errorf("agent snapshot: final recheck for %q: %w", filepath.ToSlash(path), err)
	}
	if !finalPath.Mode().IsRegular() || !os.SameFile(opened, final) || !os.SameFile(final, finalPath) || final.Size() != initial.Size() {
		return fmt.Errorf("agent snapshot: file %q changed while it was being archived", filepath.ToSlash(path))
	}
	return ctx.Err()
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
