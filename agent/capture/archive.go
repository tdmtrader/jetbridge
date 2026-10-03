package capture

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"sort"
)

// ManifestFile is the name every sealed input gives its manifest.
const ManifestFile = "manifest.json"

// ErrChangedDuringUpload reports a sealed input whose files no longer match
// the digests its loader recorded.
var ErrChangedDuringUpload = errors.New("input changed during upload")

// WriteArchive streams a sealed input as a Run input archive: the manifest
// and every inventory file, in name order, below dir/. The manifest is
// written from memory; every other file is read from root and checked
// against the inventory again as it is written, so an input that changed
// after it was loaded, and verified against the same inventory, is refused
// rather than uploaded. Hangar adds its materialization receipt at the
// archive root, outside dir.
func WriteArchive(ctx context.Context, destination io.Writer, root *os.Root, dir string, manifest []byte, inv Inventory) error {
	names := make([]string, 0, len(inv.Files)+1)
	names = append(names, ManifestFile)
	for name := range inv.Files {
		if name == ManifestFile {
			return errors.New("an inventory file cannot replace the manifest")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	archive := tar.NewWriter(destination)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		data := manifest
		if name != ManifestFile {
			want := inv.Files[name]
			limit := want.Limit
			if limit == 0 {
				limit = MaxFileBytes
			}
			var err error
			data, err = ReadRootFile(root, name, limit)
			if err != nil {
				return err
			}
			if Digest(data) != want.Digest || (want.Size >= 0 && int64(len(data)) != want.Size) {
				return ErrChangedDuringUpload
			}
		}
		if err := archive.WriteHeader(&tar.Header{Name: dir + "/" + name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := archive.Write(data); err != nil {
			return err
		}
	}
	return archive.Close()
}
