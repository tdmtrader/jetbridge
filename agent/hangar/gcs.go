package hangar

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	metadataUncompressedSHA256 = "concourse-uncompressed-sha256"
	metadataUncompressedBytes  = "concourse-uncompressed-bytes"
	metadataRepresentation     = "concourse-representation"
	representationZstd         = "zstd"

	// Hangar writes use zstd's default maximum eight-megabyte window. Keeping
	// the decoder below this fixed ceiling prevents an object from selecting an
	// attacker-controlled window while still accepting every Hangar encoding.
	decoderMaxMemory = 64 << 20
)

type GCSConfig struct {
	Bucket       string
	ScratchDir   string
	ZstdLevel    zstd.EncoderLevel
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

type GCSStore struct {
	objects       objectClient
	config        GCSConfig
	createScratch func(string, string) (scratchFile, error)
	removeScratch func(string) error
}

// NewStorageClient constructs the authenticated production client when
// endpoint is empty. An explicit endpoint is reserved for GCS-compatible test
// servers, which do not authenticate and require JSON object reads.
func NewStorageClient(ctx context.Context, endpoint string) (*storage.Client, error) {
	if endpoint == "" {
		return storage.NewClient(ctx)
	}
	return storage.NewClient(
		ctx,
		option.WithEndpoint(endpoint),
		option.WithoutAuthentication(),
		storage.WithJSONReads(),
	)
}

// NewGCSStore constructs a Hangar store backed by the supplied GCS client.
func NewGCSStore(client *storage.Client, config GCSConfig) (*GCSStore, error) {
	if client == nil {
		return nil, fmt.Errorf("hangar: GCS client is required")
	}
	return newGCSStore(storageObjectClient{client: client}, config)
}

func newGCSStore(objects objectClient, config GCSConfig) (*GCSStore, error) {
	if objects == nil {
		return nil, fmt.Errorf("hangar: GCS object client is required")
	}
	if strings.TrimSpace(config.Bucket) == "" {
		return nil, fmt.Errorf("hangar: GCS bucket is required")
	}
	if !filepath.IsAbs(config.ScratchDir) {
		return nil, fmt.Errorf("hangar: GCS scratch directory must be absolute")
	}
	info, err := os.Stat(config.ScratchDir)
	if err != nil {
		return nil, fmt.Errorf("hangar: stat GCS scratch directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("hangar: GCS scratch path is not a directory")
	}
	if config.ReadTimeout <= 0 {
		return nil, fmt.Errorf("hangar: GCS read timeout must be positive")
	}
	if config.WriteTimeout <= 0 {
		return nil, fmt.Errorf("hangar: GCS write timeout must be positive")
	}

	encoder, err := zstd.NewWriter(
		io.Discard,
		zstd.WithEncoderLevel(config.ZstdLevel),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		return nil, fmt.Errorf("hangar: invalid zstd encoder configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("hangar: close zstd configuration probe: %w", err)
	}

	config.ScratchDir = filepath.Clean(config.ScratchDir)
	return &GCSStore{
		objects: objects,
		config:  config,
		createScratch: func(directory, pattern string) (scratchFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		removeScratch: os.Remove,
	}, nil
}

func (store *GCSStore) Ensure(
	ctx context.Context,
	kind Kind,
	digest Digest,
	source io.Reader,
	maxUncompressedBytes int64,
) (attributes Attributes, err error) {
	if source == nil {
		return Attributes{}, fmt.Errorf("hangar: ensure source is required")
	}
	key, err := Key(kind, digest)
	if err != nil {
		return Attributes{}, fmt.Errorf("hangar: ensure object identity: %w", err)
	}
	if err := validateLimit(maxUncompressedBytes); err != nil {
		return Attributes{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, store.config.WriteTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Attributes{}, err
	}

	scratch, cleanup, err := store.newScratch("hangar-ensure-*.tar.zst")
	if err != nil {
		return Attributes{}, err
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			attributes = Attributes{}
			err = errors.Join(err, cleanupErr)
		}
	}()

	hasher := sha256.New()
	encoder, err := zstd.NewWriter(
		scratch,
		zstd.WithEncoderLevel(store.config.ZstdLevel),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		return Attributes{}, fmt.Errorf("hangar: create zstd encoder: %w", err)
	}

	stopCancelClose := closeReadCloserOnCancel(ctx, source)
	defer stopCancelClose()
	uncompressedBytes, copyErr := io.Copy(
		io.MultiWriter(encoder, hasher),
		io.LimitReader(contextReader{ctx: ctx, reader: source}, maxUncompressedBytes+1),
	)
	stopCancelClose()
	closeErr := encoder.Close()
	if copyErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Attributes{}, ctxErr
		}
		return Attributes{}, fmt.Errorf("hangar: read uncompressed source: %w", copyErr)
	}
	if closeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Attributes{}, ctxErr
		}
		return Attributes{}, fmt.Errorf("hangar: finish zstd source: %w", closeErr)
	}
	if uncompressedBytes > maxUncompressedBytes {
		return Attributes{}, fmt.Errorf(
			"%w: uncompressed object exceeds %d-byte limit",
			ErrCorrupt,
			maxUncompressedBytes,
		)
	}
	actualDigest := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
	if actualDigest != digest {
		return Attributes{}, fmt.Errorf(
			"%w: supplied digest %s does not match source digest %s",
			ErrCorrupt,
			digest,
			actualDigest,
		)
	}
	if err := ctx.Err(); err != nil {
		return Attributes{}, err
	}

	compressedBytes, err := scratch.Seek(0, io.SeekEnd)
	if err != nil {
		return Attributes{}, fmt.Errorf("hangar: inspect compressed scratch: %w", err)
	}
	if _, err := scratch.Seek(0, io.SeekStart); err != nil {
		return Attributes{}, fmt.Errorf("hangar: rewind compressed scratch: %w", err)
	}

	handle := store.objects.Object(store.config.Bucket, key).
		If(storage.Conditions{DoesNotExist: true})
	writer := handle.NewWriter(ctx)
	writer.SetMetadata(map[string]string{
		metadataUncompressedSHA256: string(digest),
		metadataUncompressedBytes:  strconv.FormatInt(uncompressedBytes, 10),
		metadataRepresentation:     representationZstd,
	})
	_, copyErr = io.Copy(writer, scratch)
	if copyErr != nil {
		abortErr := writer.Abort(copyErr)
		cancel()
		if abortErr != nil {
			copyErr = errors.Join(copyErr, abortErr)
		}
		return Attributes{}, fmt.Errorf("hangar: upload compressed object: %w", copyErr)
	}
	if err := writer.Close(); err != nil {
		if isPreconditionFailed(err) {
			return store.inspectAfterCreateConflict(
				ctx,
				kind,
				digest,
				maxUncompressedBytes,
			)
		}
		return Attributes{}, fmt.Errorf("hangar: commit compressed object: %w", err)
	}

	uploaded := writer.Attrs()
	if uploaded.Generation <= 0 {
		return Attributes{}, fmt.Errorf("hangar: committed object returned invalid generation")
	}
	if uploaded.Size != compressedBytes {
		return Attributes{}, fmt.Errorf(
			"hangar: committed object size %d does not match upload size %d",
			uploaded.Size,
			compressedBytes,
		)
	}
	ref, err := NewObjectRef(kind, digest, uploaded.Generation)
	if err != nil {
		return Attributes{}, fmt.Errorf("hangar: committed object reference: %w", err)
	}
	return Attributes{
		Ref:               ref,
		CompressedBytes:   uploaded.Size,
		UncompressedBytes: uncompressedBytes,
		CreatedAt:         uploaded.Created,
	}, nil
}

func (store *GCSStore) Inspect(
	ctx context.Context,
	kind Kind,
	digest Digest,
	maxUncompressedBytes int64,
) (Attributes, error) {
	key, err := Key(kind, digest)
	if err != nil {
		return Attributes{}, fmt.Errorf("hangar: inspect object identity: %w", err)
	}
	if err := validateLimit(maxUncompressedBytes); err != nil {
		return Attributes{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, store.config.ReadTimeout)
	defer cancel()
	reader, attrs, err := store.inspect(ctx, kind, digest, key, maxUncompressedBytes)
	if err != nil {
		return Attributes{}, err
	}
	if err := reader.Close(); err != nil {
		return Attributes{}, fmt.Errorf("hangar: close verified inspect scratch: %w", err)
	}
	return attrs, nil
}

func (store *GCSStore) Open(
	ctx context.Context,
	ref ObjectRef,
	maxUncompressedBytes int64,
) (io.ReadCloser, Attributes, error) {
	if err := ref.Validate(); err != nil {
		return nil, Attributes{}, fmt.Errorf("hangar: open object reference: %w", err)
	}
	if err := validateLimit(maxUncompressedBytes); err != nil {
		return nil, Attributes{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, store.config.ReadTimeout)
	defer cancel()
	handle := store.objects.Object(store.config.Bucket, ref.Key).Generation(ref.Generation)
	return store.openVerified(ctx, handle, ref, maxUncompressedBytes, false)
}

func (store *GCSStore) Delete(ctx context.Context, ref ObjectRef) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("hangar: delete object reference: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, store.config.WriteTimeout)
	defer cancel()
	err := store.objects.Object(store.config.Bucket, ref.Key).
		If(storage.Conditions{GenerationMatch: ref.Generation}).
		Delete(ctx)
	if err == nil || isNotFound(err) {
		return nil
	}
	if isPreconditionFailed(err) {
		return wrapSentinel(ErrConflict, "delete generation no longer matches", err)
	}
	return fmt.Errorf("hangar: delete object: %w", err)
}

func (store *GCSStore) inspectAfterCreateConflict(
	ctx context.Context,
	kind Kind,
	digest Digest,
	maxUncompressedBytes int64,
) (Attributes, error) {
	key, err := Key(kind, digest)
	if err != nil {
		return Attributes{}, err
	}
	reader, attrs, err := store.inspect(ctx, kind, digest, key, maxUncompressedBytes)
	if err != nil {
		switch {
		case errors.Is(err, ErrCorrupt), errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict):
			return Attributes{}, wrapSentinel(
				ErrConflict,
				"existing immutable object failed verification",
				err,
			)
		default:
			return Attributes{}, err
		}
	}
	if err := reader.Close(); err != nil {
		return Attributes{}, fmt.Errorf("hangar: close verified conflict scratch: %w", err)
	}
	return attrs, nil
}

func (store *GCSStore) inspect(
	ctx context.Context,
	kind Kind,
	digest Digest,
	key string,
	maxUncompressedBytes int64,
) (io.ReadCloser, Attributes, error) {
	handle := store.objects.Object(store.config.Bucket, key)
	current, err := handle.Attrs(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, Attributes{}, wrapSentinel(ErrNotFound, "inspect object", err)
		}
		return nil, Attributes{}, fmt.Errorf("hangar: inspect object attributes: %w", err)
	}
	ref, err := NewObjectRef(kind, digest, current.Generation)
	if err != nil {
		return nil, Attributes{}, fmt.Errorf("%w: invalid stored generation: %v", ErrCorrupt, err)
	}
	reader, attrs, err := store.openVerified(
		ctx,
		handle.Generation(current.Generation),
		ref,
		maxUncompressedBytes,
		true,
	)
	if err != nil {
		return nil, Attributes{}, err
	}
	latest, err := handle.Attrs(ctx)
	if err != nil {
		closeErr := reader.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if isNotFound(err) || isPreconditionFailed(err) {
			return nil, Attributes{}, wrapSentinel(
				ErrConflict,
				"object changed during generation-pinned verification",
				err,
			)
		}
		return nil, Attributes{}, fmt.Errorf(
			"hangar: recheck object after generation-pinned verification: %w",
			err,
		)
	}
	if latest.Generation != current.Generation ||
		latest.Metageneration != current.Metageneration {
		closeErr := reader.Close()
		conflictErr := fmt.Errorf(
			"%w: object generation changed from %d/%d to %d/%d during verification",
			ErrConflict,
			current.Generation,
			current.Metageneration,
			latest.Generation,
			latest.Metageneration,
		)
		if closeErr != nil {
			conflictErr = errors.Join(conflictErr, closeErr)
		}
		return nil, Attributes{}, conflictErr
	}
	return reader, attrs, nil
}

func (store *GCSStore) openVerified(
	ctx context.Context,
	handle objectHandle,
	ref ObjectRef,
	maxUncompressedBytes int64,
	replacementIsConflict bool,
) (reader io.ReadCloser, attributes Attributes, err error) {
	stored, err := handle.Attrs(ctx)
	if err != nil {
		if isNotFound(err) {
			if replacementIsConflict {
				return nil, Attributes{}, wrapSentinel(
					ErrConflict,
					"object changed after generation lookup",
					err,
				)
			}
			return nil, Attributes{}, wrapSentinel(ErrNotFound, "open object", err)
		}
		if isPreconditionFailed(err) {
			return nil, Attributes{}, wrapSentinel(ErrConflict, "open object generation", err)
		}
		return nil, Attributes{}, fmt.Errorf("hangar: read object attributes: %w", err)
	}
	if stored.Generation != ref.Generation {
		return nil, Attributes{}, fmt.Errorf(
			"%w: requested generation %d resolved to generation %d",
			ErrConflict,
			ref.Generation,
			stored.Generation,
		)
	}

	uncompressedBytes, err := validateStoredMetadata(stored.Metadata, ref.Digest, maxUncompressedBytes)
	if err != nil {
		return nil, Attributes{}, err
	}
	if stored.Size < 0 {
		return nil, Attributes{}, fmt.Errorf("%w: compressed object has negative size", ErrCorrupt)
	}
	maxCompressedBytes, err := maxCompressedRepresentation(uncompressedBytes)
	if err != nil {
		return nil, Attributes{}, fmt.Errorf(
			"%w: derive compressed representation limit: %v",
			ErrCorrupt,
			err,
		)
	}
	if stored.Size > maxCompressedBytes {
		return nil, Attributes{}, fmt.Errorf(
			"%w: compressed object size %d exceeds %d-byte representation limit",
			ErrCorrupt,
			stored.Size,
			maxCompressedBytes,
		)
	}

	compressed, err := handle.NewReader(ctx)
	if err != nil {
		if isNotFound(err) {
			if replacementIsConflict {
				return nil, Attributes{}, wrapSentinel(
					ErrConflict,
					"object changed before generation-pinned read",
					err,
				)
			}
			return nil, Attributes{}, wrapSentinel(ErrNotFound, "open object bytes", err)
		}
		if isPreconditionFailed(err) {
			return nil, Attributes{}, wrapSentinel(
				ErrConflict,
				"generation-pinned read failed",
				err,
			)
		}
		return nil, Attributes{}, fmt.Errorf("hangar: open compressed object: %w", err)
	}
	compressedClosed := false
	defer func() {
		if !compressedClosed {
			if closeErr := compressed.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("hangar: close compressed object: %w", closeErr))
			}
		}
	}()

	countedCompressed := &countingReader{reader: contextReader{ctx: ctx, reader: compressed}}
	decoder, err := zstd.NewReader(
		countedCompressed,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(decoderMaxMemory),
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, Attributes{}, ctxErr
		}
		return nil, Attributes{}, wrapSentinel(ErrCorrupt, "initialize zstd decoder", err)
	}
	decoderClosed := false
	defer func() {
		if !decoderClosed {
			decoder.Close()
		}
	}()

	scratch, cleanup, err := store.newScratch("hangar-open-*.tar")
	if err != nil {
		return nil, Attributes{}, err
	}
	keepScratch := false
	defer func() {
		if !keepScratch {
			err = errors.Join(err, cleanup())
		}
	}()

	hasher := sha256.New()
	actualBytes, err := io.Copy(
		io.MultiWriter(scratch, hasher),
		io.LimitReader(decoder, maxUncompressedBytes+1),
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, Attributes{}, ctxErr
		}
		return nil, Attributes{}, wrapSentinel(ErrCorrupt, "decompress object", err)
	}
	if actualBytes > maxUncompressedBytes {
		return nil, Attributes{}, fmt.Errorf(
			"%w: uncompressed object exceeds %d-byte limit",
			ErrCorrupt,
			maxUncompressedBytes,
		)
	}
	if actualBytes != uncompressedBytes {
		return nil, Attributes{}, fmt.Errorf(
			"%w: uncompressed byte count %d does not match metadata %d",
			ErrCorrupt,
			actualBytes,
			uncompressedBytes,
		)
	}
	if countedCompressed.bytes != stored.Size {
		return nil, Attributes{}, fmt.Errorf(
			"%w: compressed byte count %d does not match object size %d",
			ErrCorrupt,
			countedCompressed.bytes,
			stored.Size,
		)
	}
	actualDigest := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
	if actualDigest != ref.Digest {
		return nil, Attributes{}, fmt.Errorf(
			"%w: object digest %s does not match requested digest %s",
			ErrCorrupt,
			actualDigest,
			ref.Digest,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, Attributes{}, err
	}
	decoder.Close()
	decoderClosed = true
	closeErr := compressed.Close()
	compressedClosed = true
	if closeErr != nil {
		return nil, Attributes{}, fmt.Errorf("hangar: close compressed object: %w", closeErr)
	}
	if _, err := scratch.Seek(0, io.SeekStart); err != nil {
		return nil, Attributes{}, fmt.Errorf("hangar: rewind verified scratch: %w", err)
	}

	keepScratch = true
	return &scratchReadCloser{
			file:   scratch,
			path:   scratch.Name(),
			remove: store.removeScratch,
		}, Attributes{
			Ref:               ref,
			CompressedBytes:   stored.Size,
			UncompressedBytes: actualBytes,
			CreatedAt:         stored.Created,
		}, nil
}

func validateStoredMetadata(metadata map[string]string, digest Digest, maxUncompressedBytes int64) (int64, error) {
	if len(metadata) != 3 {
		return 0, fmt.Errorf("%w: object metadata vocabulary is not exact", ErrCorrupt)
	}
	if metadata[metadataRepresentation] != representationZstd {
		return 0, fmt.Errorf("%w: object representation metadata is not zstd", ErrCorrupt)
	}
	if metadata[metadataUncompressedSHA256] != string(digest) {
		return 0, fmt.Errorf("%w: object digest metadata does not match key", ErrCorrupt)
	}
	rawSize, found := metadata[metadataUncompressedBytes]
	if !found {
		return 0, fmt.Errorf("%w: object is missing uncompressed byte metadata", ErrCorrupt)
	}
	size, err := strconv.ParseInt(rawSize, 10, 64)
	if err != nil || size < 0 || strconv.FormatInt(size, 10) != rawSize {
		return 0, fmt.Errorf("%w: invalid uncompressed byte metadata", ErrCorrupt)
	}
	if size > maxUncompressedBytes {
		return 0, fmt.Errorf(
			"%w: declared uncompressed size exceeds %d-byte limit",
			ErrCorrupt,
			maxUncompressedBytes,
		)
	}
	return size, nil
}

func maxCompressedRepresentation(uncompressedBytes int64) (int64, error) {
	// Hangar emits one unpadded zstd frame. Even under the deliberately
	// conservative assumption of a one-byte raw block, each source byte can
	// cost at most itself plus a three-byte block header; 32 bytes covers the
	// frame header, content-size field, checksum, and final empty block.
	const fixedOverhead int64 = 32
	if uncompressedBytes < 0 ||
		uncompressedBytes > (math.MaxInt64-fixedOverhead)/4 {
		return 0, fmt.Errorf("uncompressed size cannot be represented safely")
	}
	return uncompressedBytes*4 + fixedOverhead, nil
}

func validateLimit(maxUncompressedBytes int64) error {
	if maxUncompressedBytes <= 0 || maxUncompressedBytes == math.MaxInt64 {
		return fmt.Errorf("hangar: maximum uncompressed bytes must be positive and bounded")
	}
	return nil
}

type scratchFile interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Name() string
}

func (store *GCSStore) newScratch(pattern string) (scratchFile, func() error, error) {
	file, err := store.createScratch(store.config.ScratchDir, pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("hangar: create scratch file: %w", err)
	}
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			closeErr := file.Close()
			removeErr := store.removeScratch(file.Name())
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
			if err := errors.Join(closeErr, removeErr); err != nil {
				cleanupErr = fmt.Errorf("hangar: cleanup scratch file: %w", err)
			}
		})
		return cleanupErr
	}
	return file, cleanup, nil
}

func isNotFound(err error) bool {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return true
	}
	if status.Code(err) == codes.NotFound {
		return true
	}
	var apiError *googleapi.Error
	return errors.As(err, &apiError) && apiError.Code == 404
}

func isPreconditionFailed(err error) bool {
	if status.Code(err) == codes.FailedPrecondition {
		return true
	}
	var apiError *googleapi.Error
	return errors.As(err, &apiError) && apiError.Code == 412
}

func wrapSentinel(sentinel error, message string, cause error) error {
	return fmt.Errorf("%w: %s: %w", sentinel, message, cause)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func closeReadCloserOnCancel(ctx context.Context, reader io.Reader) func() {
	closer, ok := reader.(io.ReadCloser)
	if !ok {
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = closer.Close()
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			if !stop() {
				<-done
			}
		})
	}
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.bytes += int64(count)
	return count, err
}

type scratchReadCloser struct {
	once   sync.Once
	file   scratchFile
	path   string
	remove func(string) error
	err    error
}

func (reader *scratchReadCloser) Read(buffer []byte) (int, error) {
	return reader.file.Read(buffer)
}

func (reader *scratchReadCloser) Close() error {
	reader.once.Do(func() {
		closeErr := reader.file.Close()
		remove := reader.remove
		if remove == nil {
			remove = os.Remove
		}
		removeErr := remove(reader.path)
		switch {
		case closeErr != nil && removeErr != nil:
			reader.err = errors.Join(closeErr, removeErr)
		case closeErr != nil:
			reader.err = closeErr
		case removeErr != nil && !errors.Is(removeErr, os.ErrNotExist):
			reader.err = removeErr
		}
	})
	return reader.err
}

type objectClient interface {
	Object(bucket, key string) objectHandle
}

type objectHandle interface {
	If(storage.Conditions) objectHandle
	Generation(int64) objectHandle
	NewWriter(context.Context) objectWriter
	NewReader(context.Context) (io.ReadCloser, error)
	Attrs(context.Context) (objectAttrs, error)
	Delete(context.Context) error
}

type objectWriter interface {
	io.WriteCloser
	Abort(error) error
	SetMetadata(map[string]string)
	Attrs() objectAttrs
}

type objectAttrs struct {
	Generation     int64
	Metageneration int64
	Size           int64
	Created        time.Time
	Metadata       map[string]string
}

type storageObjectClient struct {
	client *storage.Client
}

func (client storageObjectClient) Object(bucket, key string) objectHandle {
	return storageObjectHandle{handle: client.client.Bucket(bucket).Object(key)}
}

type storageObjectHandle struct {
	handle *storage.ObjectHandle
}

func (handle storageObjectHandle) If(conditions storage.Conditions) objectHandle {
	return storageObjectHandle{handle: handle.handle.If(conditions)}
}

func (handle storageObjectHandle) Generation(generation int64) objectHandle {
	return storageObjectHandle{handle: handle.handle.Generation(generation)}
}

func (handle storageObjectHandle) NewWriter(ctx context.Context) objectWriter {
	return &storageObjectWriter{writer: handle.handle.NewWriter(ctx)}
}

func (handle storageObjectHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return handle.handle.NewReader(ctx)
}

func (handle storageObjectHandle) Attrs(ctx context.Context) (objectAttrs, error) {
	attrs, err := handle.handle.Attrs(ctx)
	if err != nil {
		return objectAttrs{}, err
	}
	return attrsFromStorage(attrs), nil
}

func (handle storageObjectHandle) Delete(ctx context.Context) error {
	return handle.handle.Delete(ctx)
}

type storageObjectWriter struct {
	writer *storage.Writer
}

func (writer *storageObjectWriter) Write(content []byte) (int, error) {
	return writer.writer.Write(content)
}

func (writer *storageObjectWriter) Close() error {
	return writer.writer.Close()
}

func (writer *storageObjectWriter) Abort(err error) error {
	return writer.writer.CloseWithError(err)
}

func (writer *storageObjectWriter) SetMetadata(metadata map[string]string) {
	writer.writer.Metadata = metadata
}

func (writer *storageObjectWriter) Attrs() objectAttrs {
	return attrsFromStorage(writer.writer.Attrs())
}

func attrsFromStorage(attrs *storage.ObjectAttrs) objectAttrs {
	if attrs == nil {
		return objectAttrs{}
	}
	return objectAttrs{
		Generation:     attrs.Generation,
		Metageneration: attrs.Metageneration,
		Size:           attrs.Size,
		Created:        attrs.Created,
		Metadata:       attrs.Metadata,
	}
}
