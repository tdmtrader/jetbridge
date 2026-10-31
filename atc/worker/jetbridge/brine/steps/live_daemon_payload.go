package steps

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// liveDaemonPayloads keeps the last gzipped daemon executable so scenarios
// after the first skip recompressing it. Every call still reads and hashes
// the actual file, and every pod still decompresses its upload and verifies
// the original executable's sha256. Only the transport bytes are shared --
// never a daemon, pod, store or artifact -- and they are never mutated, so a
// payload handed out earlier stays valid when a different executable
// replaces it.
var liveDaemonPayloads liveDaemonPayloadCache

type liveDaemonPayloadCache struct {
	mu     sync.Mutex
	digest [sha256.Size]byte
	packed []byte
}

// payload returns file's gzipped bytes and the hex sha256 of its contents.
func (c *liveDaemonPayloadCache) payload(file *os.File) ([]byte, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, "", err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.packed != nil && c.digest == digest {
		return c.packed, fmt.Sprintf("%x", digest), nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	var packed bytes.Buffer
	zipper := gzip.NewWriter(&packed)
	hash.Reset()
	_, copyErr := io.Copy(io.MultiWriter(hash, zipper), file)
	if err := errors.Join(copyErr, zipper.Close()); err != nil {
		return nil, "", err
	}
	if !bytes.Equal(hash.Sum(nil), digest[:]) {
		return nil, "", fmt.Errorf("live daemon executable changed while preparing its transport")
	}
	c.digest, c.packed = digest, packed.Bytes()
	return c.packed, fmt.Sprintf("%x", digest), nil
}
