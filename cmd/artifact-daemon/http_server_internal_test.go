package main

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestArtifactHTTPServerBoundsHeadersAndIdleConnectionsWithoutWholeTransferDeadline(t *testing.T) {
	server := newArtifactHTTPServer(7780, http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout must be bounded")
	}
	if server.IdleTimeout <= 0 {
		t.Fatal("IdleTimeout must be bounded")
	}
	if server.MaxHeaderBytes <= 0 {
		t.Fatal("MaxHeaderBytes must be bounded")
	}
	if server.ReadTimeout != 0 || server.WriteTimeout != 0 {
		t.Fatal("whole-transfer read/write deadlines would break large snapshot streams")
	}
	if artifactIOIdleTimeout <= 0 {
		t.Fatal("stream reads and writes need a refreshable idle deadline")
	}
}

func TestArtifactHTTPServerRefreshesPerIODeadlinesAndClearsThemAfterRequest(t *testing.T) {
	w := &deadlineRecordingWriter{header: make(http.Header)}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1)
		for {
			_, err := r.Body.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		_, _ = w.Write([]byte("ok"))
	})
	server := newArtifactHTTPServer(7780, handler)
	req, err := http.NewRequest(http.MethodPut, "http://artifact-daemon/artifacts/key", bytes.NewBufferString("ab"))
	if err != nil {
		t.Fatal(err)
	}

	server.Handler.ServeHTTP(w, req)

	if w.positiveReadDeadlines < 2 {
		t.Fatalf("read deadline refreshes = %d, want at least 2", w.positiveReadDeadlines)
	}
	if w.positiveWriteDeadlines < 1 {
		t.Fatalf("write deadline refreshes = %d, want at least 1", w.positiveWriteDeadlines)
	}
	if !w.readDeadlineCleared || !w.writeDeadlineCleared {
		t.Fatal("connection deadlines were not cleared after request")
	}
}

type deadlineRecordingWriter struct {
	header                 http.Header
	positiveReadDeadlines  int
	positiveWriteDeadlines int
	readDeadlineCleared    bool
	writeDeadlineCleared   bool
}

func (w *deadlineRecordingWriter) Header() http.Header { return w.header }
func (w *deadlineRecordingWriter) WriteHeader(int)     {}
func (w *deadlineRecordingWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
func (w *deadlineRecordingWriter) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.readDeadlineCleared = true
	} else {
		w.positiveReadDeadlines++
	}
	return nil
}
func (w *deadlineRecordingWriter) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.writeDeadlineCleared = true
	} else {
		w.positiveWriteDeadlines++
	}
	return nil
}
