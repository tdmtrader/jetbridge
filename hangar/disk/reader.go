package disk

import (
	"context"
	"encoding/hex"
	"hash"
	"io"
	"os"

	"github.com/concourse/concourse/hangar"
)

type verifiedReader struct {
	file     *os.File
	ctx      context.Context
	hash     hash.Hash
	expected string
}

func (r *verifiedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.file.Read(p)
	_, _ = r.hash.Write(p[:n])
	if err == io.EOF && hex.EncodeToString(r.hash.Sum(nil)) != r.expected {
		err = hangar.ErrCorrupt
	}
	return n, err
}

func (r *verifiedReader) Close() error { return r.file.Close() }
