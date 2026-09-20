package jetbridge

import (
	"bytes"
	"io"
	"sync"
)

// Serialize main command writes with all sidecars falling back to build stdout.
type serializedLogWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *serializedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

// Preserve line boundaries across network reads without buffering an unbounded
// line or holding an unterminated message until the sidecar exits.
type prefixedLogWriter struct {
	writer    io.Writer
	prefix    string
	lineStart bool
}

func (w *prefixedLogWriter) Write(p []byte) (int, error) {
	consumed := 0
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		} else {
			end++
		}
		part := p[:end]
		prefixLen := 0
		if w.lineStart {
			prefixLen = len(w.prefix)
			part = append([]byte(w.prefix), part...)
		}
		n, err := w.writer.Write(part)
		if n < len(part) && err == nil {
			err = io.ErrShortWrite
		}
		used := max(0, n-prefixLen)
		consumed += used
		if err != nil {
			return consumed, err
		}
		w.lineStart = p[end-1] == '\n'
		p = p[end:]
	}
	return consumed, nil
}
func copyPrefixedLogs(dst io.Writer, src io.Reader, prefix string) {
	io.Copy(&prefixedLogWriter{writer: dst, prefix: prefix, lineStart: true}, src)
}
