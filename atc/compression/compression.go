package compression

import (
	"io"
)

// Encoding represents a compression encoding type.
type Encoding string

const GzipEncoding Encoding = "gzip"
const ZstdEncoding Encoding = "zstd"
const S2Encoding Encoding = "s2"
const RawEncoding Encoding = "raw"

type Compression interface {
	NewReader(io.ReadCloser) (io.ReadCloser, error)
	Encoding() Encoding
}
