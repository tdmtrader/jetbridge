package native

import (
	"io"

	"github.com/concourse/concourse/atc/compression"
)

// ExportNewCompressWriter exposes newCompressWriter for testing.
var ExportNewCompressWriter = func(w io.Writer, encoding compression.Encoding) io.WriteCloser {
	return newCompressWriter(w, encoding)
}
