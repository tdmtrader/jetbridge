package worker_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
)

// fakeArtifact wraps raw tar data and simulates a DaemonSetVolume that
// compresses when requested (the fix we're testing).
type fakeArtifact struct {
	tarData []byte
	handle  string
}

func (a *fakeArtifact) Handle() string { return a.handle }
func (a *fakeArtifact) Source() string { return "fake-worker" }

func (a *fakeArtifact) StreamOut(_ context.Context, _ string, enc compression.Compression) (io.ReadCloser, error) {
	rawReader := io.NopCloser(bytes.NewReader(a.tarData))

	if enc == nil || enc.Encoding() == compression.RawEncoding {
		return rawReader, nil
	}

	pr, pw := io.Pipe()
	go func() {
		gw := gzip.NewWriter(pw)
		_, copyErr := io.Copy(gw, rawReader)
		if closeErr := gw.Close(); closeErr != nil && copyErr == nil {
			copyErr = closeErr
		}
		pw.CloseWithError(copyErr)
	}()
	return pr, nil
}

var _ runtime.Artifact = (*fakeArtifact)(nil)

func makeTar(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Size: int64(len(content)), Mode: 0644}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	return buf.Bytes()
}

func TestStreamer_StreamFile_WithGzipCompression(t *testing.T) {
	fileContent := "platform: linux\nrun:\n  path: echo\n"
	tarData := makeTar(t, "task.yml", fileContent)

	artifact := &fakeArtifact{tarData: tarData, handle: "vol-1"}

	s := worker.NewStreamer(compression.NewGzipCompression())
	reader, err := s.StreamFile(context.Background(), artifact, "task.yml")
	if err != nil {
		t.Fatalf("StreamFile: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(data) != fileContent {
		t.Errorf("expected %q, got %q", fileContent, string(data))
	}
}

// TestStreamer_StreamFile_EndToEnd tests the full production path:
// HTTP daemon (raw tar) → artifact.StreamOut (gzip compresses) → Streamer.StreamFile (decompresses + untars)
func TestStreamer_StreamFile_EndToEnd(t *testing.T) {
	fileContent := "platform: linux\nrun:\n  path: /bin/sh\n"
	tarData := makeTar(t, "ci/task.yml", fileContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(tarData)
	}))
	defer srv.Close()

	artifact := &httpArtifact{serverURL: srv.URL, handle: "vol-abc"}

	s := worker.NewStreamer(compression.NewGzipCompression())
	reader, err := s.StreamFile(context.Background(), artifact, "ci/task.yml")
	if err != nil {
		t.Fatalf("StreamFile: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(data) != fileContent {
		t.Errorf("expected %q, got %q", fileContent, string(data))
	}
}

// httpArtifact simulates a DaemonSetVolume — fetches raw tar from an HTTP
// server and gzip-compresses when enc is non-nil (matching the fixed behavior).
type httpArtifact struct {
	serverURL string
	handle    string
}

func (a *httpArtifact) Handle() string { return a.handle }
func (a *httpArtifact) Source() string { return "fake-worker" }

func (a *httpArtifact) StreamOut(ctx context.Context, _ string, enc compression.Compression) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.serverURL+"/artifacts/"+a.handle, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if enc == nil || enc.Encoding() == compression.RawEncoding {
		return resp.Body, nil
	}

	pr, pw := io.Pipe()
	go func() {
		gw := gzip.NewWriter(pw)
		_, copyErr := io.Copy(gw, resp.Body)
		resp.Body.Close()
		if closeErr := gw.Close(); closeErr != nil && copyErr == nil {
			copyErr = closeErr
		}
		pw.CloseWithError(copyErr)
	}()
	return pr, nil
}

var _ runtime.Artifact = (*httpArtifact)(nil)
