package tgzfs

import (
	"io"

	"github.com/concourse/concourse/go-archive/tarfs"
	"github.com/klauspost/compress/gzip"
)

// Extract writes the gzipped tar stream src into dest with tarfs.Extract's
// containment. It never shells out to a system tar: whether extraction is
// contained must not depend on which tar, if any, the host has on its PATH.
func Extract(src io.Reader, dest string) error {
	gr, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer gr.Close()

	return tarfs.Extract(gr, dest)
}
