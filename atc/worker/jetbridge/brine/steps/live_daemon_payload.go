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

// The executable's compressed transport is immutable. Every caller still reads
// and hashes its actual file, and every fresh pod still decompresses the upload
// and verifies the original executable's digest. Only one payload is retained;
// readers of an older payload remain valid if the next caller replaces it.
// No daemon, pod, artifact, test outcome, or fixture state is shared here.
type liveDaemonPayloadCache struct {
	mu     sync.Mutex
	digest [sha256.Size]byte
	packed []byte
}

var liveDaemonPayloads liveDaemonPayloadCache

func (c *liveDaemonPayloadCache) reader(file *os.File) (*bytes.Reader, string, error) {
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
		return bytes.NewReader(c.packed), fmt.Sprintf("%x", digest), nil
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
	return bytes.NewReader(c.packed), fmt.Sprintf("%x", digest), nil
}
