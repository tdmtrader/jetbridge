package worker_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
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

func TestStreamer_StreamFile_ObservesSnapshotTerminalFailure(t *testing.T) {
	fileContent := "verified only after the complete archive"
	tarData := makeTar(t, "result.txt", fileContent)
	manifest := snapshotManifestForStreamer(tarData)
	store := new(snapshotfakes.FakeContentStore)
	store.OpenReturns(&streamerCloseErrorReader{
		Reader: bytes.NewReader(tarData),
		err:    errors.New("secret replica close failure"),
	}, nil)
	artifact, err := runtime.NewSnapshotArtifact(manifest, store)
	if err != nil {
		t.Fatalf("NewSnapshotArtifact: %v", err)
	}

	reader, err := worker.NewStreamer(compression.NewGzipCompression()).StreamFile(
		context.Background(), artifact, "result.txt",
	)
	if err != nil {
		t.Fatalf("StreamFile: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err == nil {
		t.Fatal("expected the late snapshot terminal failure")
	}
	if string(data) != fileContent {
		t.Fatalf("selected content = %q, want %q", data, fileContent)
	}
	if err.Error() != "snapshot artifact: content stream close failed" {
		t.Fatalf("unexpected terminal error: %v", err)
	}
}

func TestStreamer_StreamFile_PreservesSnapshotFileNotFound(t *testing.T) {
	tarData := makeTar(t, "present.txt", "present")
	store := new(snapshotfakes.FakeContentStore)
	store.OpenReturns(io.NopCloser(bytes.NewReader(tarData)), nil)
	artifact, err := runtime.NewSnapshotArtifact(snapshotManifestForStreamer(tarData), store)
	if err != nil {
		t.Fatalf("NewSnapshotArtifact: %v", err)
	}

	reader, err := worker.NewStreamer(compression.NewGzipCompression()).StreamFile(
		context.Background(), artifact, "missing.txt",
	)
	if reader != nil {
		reader.Close()
		t.Fatal("missing file returned a reader")
	}
	if !errors.Is(err, runtime.ErrFileNotFound) {
		t.Fatalf("StreamFile error = %v, want ErrFileNotFound", err)
	}
}

func snapshotManifestForStreamer(archive []byte) snapshot.Snapshot {
	digest := sha256.Sum256(archive)
	return snapshot.Snapshot{
		ID:             snapshot.SnapshotID(9007199254740993),
		Type:           snapshot.TypeRef("review/v1"),
		Digest:         snapshot.Digest(fmt.Sprintf("sha256:%x", digest)),
		ByteSize:       int64(len(archive)),
		FileCount:      1,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Now().UTC(),
	}
}

type streamerCloseErrorReader struct {
	*bytes.Reader
	err error
}

func (reader *streamerCloseErrorReader) Close() error { return reader.err }

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
