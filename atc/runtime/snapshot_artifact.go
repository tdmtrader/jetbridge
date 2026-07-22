package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"strings"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/compression"
)

// SnapshotArtifact exposes one immutable durable snapshot through the ordinary
// runtime.Artifact contract. Verification errors arrive at the end of the
// returned stream; consumers must discard all partial output when Read fails.
type SnapshotArtifact struct {
	manifest snapshot.Snapshot
	content  snapshot.ContentStore
}

type snapshotStreamResult struct {
	err error
}

// snapshotStream exposes the producer's terminal status separately from the
// encoded byte stream. Compression readers can otherwise accept a complete
// frame and hide an error reported immediately after its footer.
type snapshotStream struct {
	*io.PipeReader
	done   <-chan struct{}
	result *snapshotStreamResult
}

func (stream *snapshotStream) TerminalError() error {
	<-stream.done
	return stream.result.err
}

func NewSnapshotArtifact(manifest snapshot.Snapshot, content snapshot.ContentStore) (*SnapshotArtifact, error) {
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("snapshot artifact manifest: %w", err)
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return nil, fmt.Errorf("snapshot artifact content is unavailable")
	}
	if manifest.Representation != "application/x-tar" {
		return nil, fmt.Errorf("snapshot artifact representation is not a canonical tar")
	}
	if content == nil {
		return nil, fmt.Errorf("snapshot artifact content store is required")
	}
	return &SnapshotArtifact{manifest: manifest.Clone(), content: content}, nil
}

func (artifact *SnapshotArtifact) Handle() string {
	return "snapshot:" + artifact.manifest.ID.String()
}

func (artifact *SnapshotArtifact) Source() string { return "snapshot" }

// SnapshotRef returns the immutable semantic identity represented by this
// concrete adapter. It is used only for trusted in-process restart idempotency.
func (artifact *SnapshotArtifact) SnapshotRef() snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: artifact.manifest.ID, Type: artifact.manifest.Type, Digest: artifact.manifest.Digest,
	}
}

func (artifact *SnapshotArtifact) StreamOut(
	ctx context.Context,
	subpath string,
	encoding compression.Compression,
) (io.ReadCloser, error) {
	target, filter, err := normalizeSnapshotSubpath(subpath)
	if err != nil {
		return nil, err
	}
	requested := compression.RawEncoding
	if encoding != nil {
		requested = encoding.Encoding()
	}
	if !supportedSnapshotEncoding(requested) {
		return nil, fmt.Errorf("snapshot artifact: unsupported compression %q", requested)
	}

	source, err := artifact.content.Open(ctx, artifact.manifest.Clone())
	if err != nil {
		return nil, fmt.Errorf("snapshot artifact content unavailable")
	}

	reader, writer := io.Pipe()
	done := make(chan struct{})
	result := &snapshotStreamResult{}
	go func() {
		streamErr := artifact.writeStream(ctx, source, writer, target, filter, requested)
		if closeErr := source.Close(); streamErr == nil && closeErr != nil {
			streamErr = fmt.Errorf("snapshot artifact: content stream close failed")
		}
		result.err = streamErr
		_ = writer.CloseWithError(streamErr)
		close(done)
	}()
	return &snapshotStream{PipeReader: reader, done: done, result: result}, nil
}

func (artifact *SnapshotArtifact) writeStream(
	ctx context.Context,
	source io.Reader,
	destination io.Writer,
	target string,
	filter bool,
	encoding compression.Encoding,
) error {
	compressed, err := newSnapshotCompressionWriter(destination, encoding)
	if err != nil {
		return err
	}
	output := destination
	if compressed != nil {
		output = compressed
	}

	verified := &snapshotVerifyingReader{
		ctx:      ctx,
		reader:   source,
		hash:     sha256.New(),
		expected: artifact.manifest,
	}
	if filter {
		err = filterVerifiedSnapshotTar(verified, output, target)
	} else {
		err = copyAndValidateSnapshotTar(verified, output)
		if err == nil {
			err = verified.Verify()
		}
	}
	if compressed != nil {
		if closeErr := compressed.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}
	return err
}

func normalizeSnapshotSubpath(value string) (string, bool, error) {
	if value == "" || value == "." {
		return ".", false, nil
	}
	if strings.ContainsRune(value, '\x00') || strings.Contains(value, `\`) || path.IsAbs(value) {
		return "", false, fmt.Errorf("snapshot artifact: unsafe subpath")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("snapshot artifact: unsafe subpath")
	}
	return strings.TrimSuffix(clean, "/"), true, nil
}

func supportedSnapshotEncoding(value compression.Encoding) bool {
	switch value {
	case compression.RawEncoding, compression.GzipEncoding, compression.ZstdEncoding, compression.S2Encoding:
		return true
	default:
		return false
	}
}

func newSnapshotCompressionWriter(destination io.Writer, encoding compression.Encoding) (io.WriteCloser, error) {
	switch encoding {
	case compression.RawEncoding:
		return nil, nil
	case compression.GzipEncoding:
		return gzip.NewWriter(destination), nil
	case compression.ZstdEncoding:
		return zstd.NewWriter(destination)
	case compression.S2Encoding:
		return s2.NewWriter(destination), nil
	default:
		return nil, fmt.Errorf("snapshot artifact: unsupported compression %q", encoding)
	}
}

func copyAndValidateSnapshotTar(source io.Reader, destination io.Writer) error {
	tee := io.TeeReader(source, destination)
	reader := tar.NewReader(tee)
	for {
		_, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("snapshot artifact: malformed canonical tar")
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("snapshot artifact: read canonical tar failed")
		}
	}
	if _, err := io.Copy(destination, source); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("snapshot artifact: drain canonical tar failed")
	}
	return nil
}

func filterVerifiedSnapshotTar(source *snapshotVerifyingReader, destination io.Writer, target string) error {
	reader := tar.NewReader(source)
	writer := tar.NewWriter(destination)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = writer.Close()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("snapshot artifact: malformed canonical tar")
		}
		name := strings.TrimPrefix(path.Clean(header.Name), "./")
		selected := name == target || strings.HasPrefix(name, target+"/")
		if !selected {
			continue
		}
		found = true
		copied := *header
		if err := writer.WriteHeader(&copied); err != nil {
			_ = writer.Close()
			return err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			_ = writer.Close()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("snapshot artifact: read canonical tar failed")
		}
	}
	// archive/tar stops at its logical terminator. Drain the physical stream so
	// corruption after a selected entry or after the tar EOF cannot evade the
	// immutable digest/length check.
	if _, err := io.Copy(io.Discard, source); err != nil {
		_ = writer.Close()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("snapshot artifact: drain canonical tar failed")
	}
	// Verify before writing the filtered tar terminators. A StreamFile consumer
	// may stop after the selected body; verification must not be blocked behind
	// final output that such a consumer has not requested yet.
	if err := source.Verify(); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if !found {
		return ErrFileNotFound
	}
	return nil
}

type snapshotVerifyingReader struct {
	ctx      context.Context
	reader   io.Reader
	hash     hash.Hash
	read     int64
	tail     []byte
	expected snapshot.Snapshot
}

func (reader *snapshotVerifyingReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.reader.Read(buffer)
	if n > 0 {
		reader.read += int64(n)
		_, _ = reader.hash.Write(buffer[:n])
		reader.rememberTail(buffer[:n])
	}
	return n, err
}

func (reader *snapshotVerifyingReader) Verify() error {
	if reader.read != reader.expected.ByteSize {
		return fmt.Errorf("snapshot artifact: content length verification failed")
	}
	actual := fmt.Sprintf("sha256:%x", reader.hash.Sum(nil))
	if actual != reader.expected.Digest.String() {
		return fmt.Errorf("snapshot artifact: content digest verification failed")
	}
	if reader.read%512 != 0 || len(reader.tail) < 1024 {
		return fmt.Errorf("snapshot artifact: malformed canonical tar terminator")
	}
	for _, value := range reader.tail[len(reader.tail)-1024:] {
		if value != 0 {
			return fmt.Errorf("snapshot artifact: malformed canonical tar terminator")
		}
	}
	return nil
}

func (reader *snapshotVerifyingReader) rememberTail(value []byte) {
	const retained = 1024
	if len(value) >= retained {
		if cap(reader.tail) < retained {
			reader.tail = make([]byte, retained)
		} else {
			reader.tail = reader.tail[:retained]
		}
		copy(reader.tail, value[len(value)-retained:])
		return
	}
	if reader.tail == nil {
		reader.tail = make([]byte, 0, retained)
	}
	overflow := len(reader.tail) + len(value) - retained
	if overflow > 0 {
		copy(reader.tail, reader.tail[overflow:])
		reader.tail = reader.tail[:len(reader.tail)-overflow]
	}
	reader.tail = append(reader.tail, value...)
}
