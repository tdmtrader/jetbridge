package durable

import (
	"fmt"
	"io"
	"os"
)

// spooled is a body written to a temporary file so it can be re-read.
type spooled struct {
	file *os.File
}

func (s spooled) cleanup() {
	if s.file == nil {
		return
	}
	name := s.file.Name()
	s.file.Close()
	os.Remove(name)
}

// spool copies body to a temporary file and rewinds it, returning the file and
// its size.
//
// The S3 SDK needs a seekable body to sign the payload and to retry a failed
// attempt, and a resource cache is exactly the kind of artifact that is too big
// to hold in memory on every node of a DaemonSet. Disk is the cheap resource
// here; the daemon already owns a hostPath.
func spool(body io.Reader) (spooled, int64, error) {
	file, err := os.CreateTemp("", "durable-put-*")
	if err != nil {
		return spooled{}, 0, fmt.Errorf("durable: create spool: %w", err)
	}

	s := spooled{file: file}

	size, err := io.Copy(file, body)
	if err != nil {
		s.cleanup()
		return spooled{}, 0, fmt.Errorf("durable: spool body: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		s.cleanup()
		return spooled{}, 0, fmt.Errorf("durable: rewind spool: %w", err)
	}

	return s, size, nil
}
