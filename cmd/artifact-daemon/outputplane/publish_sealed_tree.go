package outputplane

// Canonicalizing a sealed step directory.
//
// The publish route hands over no bytes. That is the strongest form of "no
// caller chooses where an object goes": the daemon reads the sealed step
// directory it is already holding, canonicalizes it with the foundation's own
// canonicalizer, and stores it at a key derived from its own namespace.

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// CanonicalizeDirectory turns a sealed incarnation into canonical bytes.
//
// It tars the directory and hands the tar to the foundation's Canonicalizer,
// rather than canonicalizing here: the canonical form -- ordering, headers,
// modes, padding -- is the foundation's, and a second implementation of it
// would be a second answer to "what are these bytes". The intermediate tar is
// not the artifact; it is input to the one canonicalizer this repository has.
func (daemon *Daemon) CanonicalizeDirectory(ctx context.Context, root string) (*hangar.CapturedTree, error) {
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(tarDirectory(root, writer)) }()

	captured, err := daemon.canonicalizer.Capture(ctx, reader)
	if err != nil {
		_ = reader.CloseWithError(err)

		return nil, fmt.Errorf("%w: canonicalizing the sealed source: %v", output.ErrCorrupt, err)
	}

	return captured, nil
}

// tarDirectory writes a deterministic tar of a directory tree.
//
// Deterministic because the canonicalizer's digest is over what comes out of
// it, and a walk whose order depended on the filesystem would give one tree two
// digests on two nodes. Entries are sorted; times, uids and names are
// normalized. Anything that is not a regular file, a directory or a symlink is
// refused rather than skipped: silently dropping a device node or a socket
// would publish a tree that is not the one the producer wrote.
func tarDirectory(root string, out io.Writer) error {
	writer := tar.NewWriter(out)

	var walk func(string) error
	walk = func(relative string) error {
		entries, err := os.ReadDir(filepath.Join(root, relative))
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, entry := range entries {
			name := tarPath(relative, entry.Name())
			info, err := entry.Info()
			if err != nil {
				return err
			}

			switch {
			case info.IsDir():
				if err := writeHeader(writer, &tar.Header{
					Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755,
				}); err != nil {
					return err
				}
				if err := walk(name); err != nil {
					return err
				}
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(filepath.Join(root, name))
				if err != nil {
					return err
				}
				if err := writeHeader(writer, &tar.Header{
					Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777,
				}); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				mode := int64(0o644)
				if info.Mode()&0o111 != 0 {
					mode = 0o755
				}
				if err := writeHeader(writer, &tar.Header{
					Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: info.Size(),
				}); err != nil {
					return err
				}
				file, err := os.Open(filepath.Join(root, name))
				if err != nil {
					return err
				}
				_, err = io.Copy(writer, file)
				_ = file.Close()
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: %s is a %s; a sealed source holds regular files, "+
					"directories and symlinks", output.ErrCorrupt, name, info.Mode().Type())
			}
		}

		return nil
	}

	if err := walk(""); err != nil {
		_ = writer.Close()

		return err
	}

	return writer.Close()
}

// tarEpoch is the one modification time every entry carries.
var tarEpoch = time.Unix(0, 0).UTC()

func writeHeader(writer *tar.Writer, header *tar.Header) error {
	// Every varying field is pinned. The digest is over these bytes, so a
	// modification time or an owner name would make the same tree hash
	// differently on two nodes.
	//
	// AccessTime and ChangeTime are left ZERO rather than pinned: a non-zero
	// value makes the PAX writer emit atime/ctime records, and the foundation's
	// canonicalizer refuses those by name. They carry nothing a tree's identity
	// depends on, so the right pin for them is absence.
	header.ModTime = tarEpoch
	header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}
	header.Uid, header.Gid = 0, 0
	header.Uname, header.Gname = "", ""
	header.Format = tar.FormatPAX

	return writer.WriteHeader(header)
}

func tarPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return strings.TrimSuffix(prefix, "/") + "/" + name
}
