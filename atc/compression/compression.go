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

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . Compression
type Compression interface {
	NewReader(io.ReadCloser) (io.ReadCloser, error)
	Encoding() Encoding
}
