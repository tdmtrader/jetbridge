package compression

import (
	"io"
)

// Encoding represents a compression encoding type. Gzip is the one codec:
// the artifact daemon detects it by magic number and the artifact server
// and fly speak nothing else.
type Encoding string

const GzipEncoding Encoding = "gzip"

type Compression interface {
	NewReader(io.ReadCloser) (io.ReadCloser, error)
	Encoding() Encoding
}
