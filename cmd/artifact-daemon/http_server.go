package main

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	artifactReadHeaderTimeout = 10 * time.Second
	artifactConnectionTimeout = 2 * time.Minute
	artifactIOIdleTimeout     = 30 * time.Second
	artifactMaxHeaderBytes    = 1 << 20
)

// newArtifactHTTPServer bounds connection setup and keep-alive lifetime while
// deliberately leaving whole-request ReadTimeout and WriteTimeout disabled.
// Snapshot transfers can legitimately take longer than a fixed wall-clock
// deadline; withArtifactIOIdleDeadlines instead refreshes a deadline for each
// read and write so only stalled transfers are terminated.
func newArtifactHTTPServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           withArtifactIOIdleDeadlines(handler),
		ReadHeaderTimeout: artifactReadHeaderTimeout,
		IdleTimeout:       artifactConnectionTimeout,
		MaxHeaderBytes:    artifactMaxHeaderBytes,
	}
}

func withArtifactIOIdleDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if r.Body != nil {
			r.Body = &deadlineRequestBody{ReadCloser: r.Body, controller: controller}
		}
		wrapped := &deadlineResponseWriter{ResponseWriter: w, controller: controller}
		defer func() {
			// A zero deadline restores the connection for keep-alive reuse. Errors
			// mean the ResponseWriter does not expose deadline control; net/http's
			// production writer does.
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}()
		next.ServeHTTP(wrapped, r)
	})
}

type deadlineRequestBody struct {
	io.ReadCloser
	controller *http.ResponseController
}

func (b *deadlineRequestBody) Read(p []byte) (int, error) {
	_ = b.controller.SetReadDeadline(time.Now().Add(artifactIOIdleTimeout))
	return b.ReadCloser.Read(p)
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
}

func (w *deadlineResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *deadlineResponseWriter) WriteHeader(status int) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(artifactIOIdleTimeout))
	w.ResponseWriter.WriteHeader(status)
}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(artifactIOIdleTimeout))
	return w.ResponseWriter.Write(p)
}
