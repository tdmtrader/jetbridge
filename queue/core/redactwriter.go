package core

import (
	"bytes"
	"io"
	"sync"
)

// RedactWriter passes each whole line through Redact before writing it on, so a
// URL split across writes is still caught. Flush writes a last partial line.
type RedactWriter struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

func NewRedactWriter(w io.Writer) *RedactWriter { return &RedactWriter{w: w} }

func (r *RedactWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	i := bytes.LastIndexByte(r.buf, '\n')
	if i < 0 {
		return len(p), nil
	}
	line := Redact(string(r.buf[:i+1]))
	r.buf = append(r.buf[:0], r.buf[i+1:]...)
	if _, err := io.WriteString(r.w, line); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes what is held of an unfinished line.
func (r *RedactWriter) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := Redact(string(r.buf))
	r.buf = r.buf[:0]
	_, err := io.WriteString(r.w, line)
	return err
}
