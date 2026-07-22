package runtime_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/stretchr/testify/require"
)

func TestSnapshotArtifactStreamsVerifiedIndependentArchives(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{
		"dir/a.txt": "alpha",
		"dir/b.txt": "bravo",
		"tail.txt":  "tail",
	})
	manifest := snapshotManifest(archive)
	store := new(snapshotfakes.FakeContentStore)
	store.OpenCalls(func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	})

	artifact, err := runtime.NewSnapshotArtifact(manifest, store)
	require.NoError(t, err)
	require.Equal(t, "snapshot:9007199254740993", artifact.Handle())
	require.Equal(t, "snapshot", artifact.Source())

	for _, enc := range []compression.Compression{
		nil,
		compression.NewGzipCompression(),
		compression.NewZstdCompression(),
		compression.NewS2Compression(),
	} {
		stream, err := artifact.StreamOut(context.Background(), ".", enc)
		require.NoError(t, err)
		decoded := stream
		if enc != nil {
			decoded, err = enc.NewReader(stream)
			require.NoError(t, err)
		}
		got, err := io.ReadAll(decoded)
		require.NoError(t, err)
		require.NoError(t, decoded.Close())
		require.Equal(t, archive, got)
	}
	require.Equal(t, 4, store.OpenCallCount())

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, streamErr := artifact.StreamOut(context.Background(), "dir", nil)
			require.NoError(t, streamErr)
			filtered, readErr := io.ReadAll(stream)
			require.NoError(t, readErr)
			require.NoError(t, stream.Close())
			require.Equal(t, map[string]string{"dir/a.txt": "alpha", "dir/b.txt": "bravo"}, untarFiles(t, filtered))
		}()
	}
	wg.Wait()
	require.Equal(t, 8, store.OpenCallCount())
}

func TestSnapshotArtifactRejectsUnsafeAndMissingSubpaths(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"safe.txt": "ok"})
	store := new(snapshotfakes.FakeContentStore)
	store.OpenCalls(func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	})
	artifact, err := runtime.NewSnapshotArtifact(snapshotManifest(archive), store)
	require.NoError(t, err)

	for _, path := range []string{"/absolute", "../escape", `dir\\file`, "bad\x00path"} {
		_, err := artifact.StreamOut(context.Background(), path, nil)
		require.Error(t, err)
	}
	require.Zero(t, store.OpenCallCount())

	stream, err := artifact.StreamOut(context.Background(), "missing", nil)
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.ErrorIs(t, err, runtime.ErrFileNotFound)
	require.NoError(t, stream.Close())
}

func TestSnapshotArtifactRedactsContentStoreFailures(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"safe.txt": "ok"})
	store := new(snapshotfakes.FakeContentStore)
	store.OpenReturns(nil, errors.New("dial https://secret-node:7780/artifacts/secret-handle"))
	artifact, err := runtime.NewSnapshotArtifact(snapshotManifest(archive), store)
	require.NoError(t, err)
	_, err = artifact.StreamOut(context.Background(), ".", nil)
	require.EqualError(t, err, "snapshot artifact content unavailable")
	require.NotContains(t, err.Error(), "secret")
}

func TestSnapshotArtifactClosesEverySourceExactlyOnce(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"safe.txt": "ok"})
	manifest := snapshotManifest(archive)
	store := new(snapshotfakes.FakeContentStore)
	var readers []*countingReadCloser
	store.OpenCalls(func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		reader := &countingReadCloser{Reader: bytes.NewReader(archive)}
		readers = append(readers, reader)
		return reader, nil
	})
	artifact, err := runtime.NewSnapshotArtifact(manifest, store)
	require.NoError(t, err)

	stream, err := artifact.StreamOut(context.Background(), ".", nil)
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Eventually(t, func() bool { return readers[0].closes.Load() == 1 }, time.Second, time.Millisecond)

	stream, err = artifact.StreamOut(context.Background(), ".", nil)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Eventually(t, func() bool { return readers[1].closes.Load() == 1 }, time.Second, time.Millisecond)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err = artifact.StreamOut(canceled, ".", nil)
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, stream.Close())
	require.Eventually(t, func() bool { return readers[2].closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestSnapshotArtifactVerifiesFilteredSourceBeforeFinalizingOutput(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"selected.txt": "selected", "tail.txt": "tail"})
	reachedEOF := make(chan struct{})
	reader := &eofSignalingReadCloser{Reader: bytes.NewReader(archive), reachedEOF: reachedEOF}
	store := new(snapshotfakes.FakeContentStore)
	store.OpenReturns(reader, nil)
	artifact, err := runtime.NewSnapshotArtifact(snapshotManifest(archive), store)
	require.NoError(t, err)

	stream, err := artifact.StreamOut(context.Background(), "selected.txt", nil)
	require.NoError(t, err)
	tarReader := tar.NewReader(stream)
	_, err = tarReader.Next()
	require.NoError(t, err)
	content, err := io.ReadAll(tarReader)
	require.NoError(t, err)
	require.Equal(t, "selected", string(content))
	select {
	case <-reachedEOF:
	case <-time.After(time.Second):
		t.Fatal("filtered stream did not drain and verify the physical source before output finalization")
	}
	require.NoError(t, stream.Close())
}

func TestSnapshotArtifactRedactsSourceCloseFailure(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"safe.txt": "ok"})
	store := new(snapshotfakes.FakeContentStore)
	store.OpenReturns(&countingReadCloser{
		Reader: bytes.NewReader(archive), closeErr: errors.New("secret daemon path"),
	}, nil)
	artifact, err := runtime.NewSnapshotArtifact(snapshotManifest(archive), store)
	require.NoError(t, err)
	stream, err := artifact.StreamOut(context.Background(), ".", nil)
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.EqualError(t, err, "snapshot artifact: content stream close failed")
	require.NotContains(t, err.Error(), "secret")
	require.NoError(t, stream.Close())
}

func TestSnapshotArtifactRedactsMidEntrySourceFailure(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"selected.txt": strings.Repeat("x", 1024)})
	sensitive := errors.New("read https://secret-node:7780/snapshots/sha256/private")
	store := new(snapshotfakes.FakeContentStore)
	store.OpenReturns(&countingReadCloser{
		Reader: io.MultiReader(bytes.NewReader(archive[:600]), errorReader{sensitive}),
	}, nil)
	artifact, err := runtime.NewSnapshotArtifact(snapshotManifest(archive), store)
	require.NoError(t, err)

	stream, err := artifact.StreamOut(context.Background(), "selected.txt", nil)
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.EqualError(t, err, "snapshot artifact: read canonical tar failed")
	require.NotContains(t, err.Error(), "secret")
	require.NoError(t, stream.Close())
}

func TestSnapshotArtifactSurfacesTerminalIntegrityFailures(t *testing.T) {
	archive := snapshotArchive(t, map[string]string{"selected.txt": "selected", "tail.txt": "tail"})
	manifest := snapshotManifest(archive)

	for name, mutate := range map[string]func([]byte, *snapshot.Snapshot) []byte{
		"digest": func(value []byte, manifest *snapshot.Snapshot) []byte {
			corrupt := append([]byte(nil), value...)
			corrupt[len(corrupt)-1] ^= 1
			return corrupt
		},
		"length": func(value []byte, manifest *snapshot.Snapshot) []byte {
			manifest.ByteSize++
			return value
		},
		"truncated tar": func(value []byte, manifest *snapshot.Snapshot) []byte {
			value = value[:700]
			sum := sha256.Sum256(value)
			manifest.ByteSize = int64(len(value))
			manifest.Digest = snapshot.Digest("sha256:" + fmt.Sprintf("%x", sum[:]))
			return value
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := manifest.Clone()
			value := mutate(archive, &candidate)
			store := new(snapshotfakes.FakeContentStore)
			store.OpenReturns(io.NopCloser(bytes.NewReader(value)), nil)
			artifact, err := runtime.NewSnapshotArtifact(candidate, store)
			require.NoError(t, err)
			stream, err := artifact.StreamOut(context.Background(), "selected.txt", nil)
			require.NoError(t, err)
			_, err = io.ReadAll(stream)
			require.Error(t, err)
			require.NoError(t, stream.Close())
		})
	}
}

func snapshotArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range []string{"dir/a.txt", "dir/b.txt", "safe.txt", "selected.txt", "tail.txt"} {
		content, found := files[name]
		if !found {
			continue
		}
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}))
		_, err := io.WriteString(writer, content)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}

func snapshotManifest(archive []byte) snapshot.Snapshot {
	sum := sha256.Sum256(archive)
	return snapshot.Snapshot{
		ID:             snapshot.SnapshotID(9007199254740993),
		Type:           snapshot.TypeRef("review/v1"),
		Digest:         snapshot.Digest("sha256:" + fmt.Sprintf("%x", sum[:])),
		ByteSize:       int64(len(archive)),
		FileCount:      3,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Now().UTC(),
	}
}

func untarFiles(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	files := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return files
		}
		require.NoError(t, err)
		content, err := io.ReadAll(reader)
		require.NoError(t, err)
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			files[header.Name] = string(content)
		}
	}
}

type countingReadCloser struct {
	io.Reader
	closes   atomic.Int32
	closeErr error
}

func (reader *countingReadCloser) Close() error {
	reader.closes.Add(1)
	return reader.closeErr
}

type eofSignalingReadCloser struct {
	io.Reader
	reachedEOF chan struct{}
	once       sync.Once
}

func (reader *eofSignalingReadCloser) Read(buffer []byte) (int, error) {
	n, err := reader.Reader.Read(buffer)
	if err == io.EOF {
		reader.once.Do(func() { close(reader.reachedEOF) })
	}
	return n, err
}

func (*eofSignalingReadCloser) Close() error { return nil }

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
