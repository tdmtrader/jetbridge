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
	metadataLogicalSHA256  = "concourse-uncompressed-sha256"
	metadataLogicalBytes   = "concourse-uncompressed-bytes"
	metadataRepresentation = "concourse-representation"
	representationZstd     = "zstd"
	decoderMaxMemory       = 64 << 20
)

type GCSConfig struct {
	Bucket       string
	Prefix       string
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

func NewStorageClient(ctx context.Context, endpoint string) (*storage.Client, error) {
	if endpoint == "" {
		return storage.NewClient(ctx)
	}
	return storage.NewClient(ctx, option.WithEndpoint(endpoint), option.WithoutAuthentication(), storage.WithJSONReads())
}

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
	if err := ValidateDeploymentPrefix(config.Prefix); err != nil {
		return nil, fmt.Errorf("hangar: GCS prefix: %w", err)
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
	if config.ZstdLevel == 0 {
		config.ZstdLevel = zstd.SpeedDefault
	}
	encoder, err := zstd.NewWriter(io.Discard, zstd.WithEncoderLevel(config.ZstdLevel), zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err != nil {
		return nil, fmt.Errorf("hangar: invalid zstd encoder configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("hangar: close zstd configuration probe: %w", err)
	}
	config.ScratchDir = filepath.Clean(config.ScratchDir)
	return &GCSStore{objects: objects, config: config, createScratch: func(directory, pattern string) (scratchFile, error) { return os.CreateTemp(directory, pattern) }, removeScratch: os.Remove}, nil
}

func (store *GCSStore) EnsureTree(ctx context.Context, scope Scope, digest Digest, source io.Reader, maxLogicalBytes int64) (attributes TreeAttributes, created bool, err error) {
	if source == nil {
		return TreeAttributes{}, false, fmt.Errorf("hangar: ensure source is required")
	}
	key, err := TreeKey(store.config.Prefix, scope, digest)
	if err != nil {
		return TreeAttributes{}, false, fmt.Errorf("hangar: ensure tree identity: %w", err)
	}
	if err := validateGCSLimit(maxLogicalBytes); err != nil {
		return TreeAttributes{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, store.config.WriteTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return TreeAttributes{}, false, err
	}

	scratch, cleanup, err := store.newScratch("hangar-ensure-*.tar.zst")
	if err != nil {
		return TreeAttributes{}, false, err
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			attributes = TreeAttributes{}
			created = false
			err = errors.Join(err, cleanupErr)
		}
	}()
	hasher := sha256.New()
	encoder, err := zstd.NewWriter(scratch, zstd.WithEncoderLevel(store.config.ZstdLevel), zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err != nil {
		return TreeAttributes{}, false, fmt.Errorf("hangar: create zstd encoder: %w", err)
	}
	stopClose := closeReadCloserOnCancel(ctx, source)
	defer stopClose()
	logicalBytes, copyErr := io.Copy(io.MultiWriter(encoder, hasher), io.LimitReader(contextReader{ctx: ctx, reader: source}, maxLogicalBytes+1))
	stopClose()
	closeErr := encoder.Close()
	if copyErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TreeAttributes{}, false, ctxErr
		}
		return TreeAttributes{}, false, fmt.Errorf("hangar: read logical source: %w", copyErr)
	}
	if closeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TreeAttributes{}, false, ctxErr
		}
		return TreeAttributes{}, false, fmt.Errorf("hangar: finish zstd source: %w", closeErr)
	}
	if logicalBytes > maxLogicalBytes {
		return TreeAttributes{}, false, fmt.Errorf("%w: logical object exceeds %d-byte limit", ErrCorrupt, maxLogicalBytes)
	}
	actualDigest := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
	if actualDigest != digest {
		return TreeAttributes{}, false, fmt.Errorf("%w: supplied digest %s does not match source digest %s", ErrCorrupt, digest, actualDigest)
	}
	if err := ctx.Err(); err != nil {
		return TreeAttributes{}, false, err
	}
	storedBytes, err := scratch.Seek(0, io.SeekEnd)
	if err != nil {
		return TreeAttributes{}, false, fmt.Errorf("hangar: inspect compressed scratch: %w", err)
	}
	if _, err := scratch.Seek(0, io.SeekStart); err != nil {
		return TreeAttributes{}, false, fmt.Errorf("hangar: rewind compressed scratch: %w", err)
	}

	handle := store.objects.Object(store.config.Bucket, key).If(storage.Conditions{DoesNotExist: true})
	writer := handle.NewWriter(ctx)
	writer.SetMetadata(map[string]string{metadataLogicalSHA256: string(digest), metadataLogicalBytes: strconv.FormatInt(logicalBytes, 10), metadataRepresentation: representationZstd})
	_, copyErr = io.Copy(writer, scratch)
	if copyErr != nil {
		ctxErr := ctx.Err()
		abortErr := writer.Abort(copyErr)
		cancel()
		if abortErr != nil {
			copyErr = errors.Join(copyErr, abortErr)
		}
		if ctxErr != nil {
			return TreeAttributes{}, false, ctxErr
		}
		return TreeAttributes{}, false, infrastructure("upload compressed tree", copyErr)
	}
	if err := writer.Close(); err != nil {
		if isPreconditionFailed(err) {
			return store.inspectAfterCreateConflict(ctx, scope, digest, maxLogicalBytes)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TreeAttributes{}, false, ctxErr
		}
		return TreeAttributes{}, false, infrastructure("commit compressed tree", err)
	}
	uploaded := writer.Attrs()
	if uploaded.Generation <= 0 {
		return TreeAttributes{}, false, infrastructure("commit compressed tree", fmt.Errorf("invalid generation %d", uploaded.Generation))
	}
	if uploaded.Size != storedBytes {
		return TreeAttributes{}, false, infrastructure("commit compressed tree", fmt.Errorf("object size %d does not match upload size %d", uploaded.Size, storedBytes))
	}
	ref, err := NewTreeRef(scope, digest, uploaded.Generation)
	if err != nil {
		return TreeAttributes{}, false, infrastructure("construct committed tree reference", err)
	}
	return TreeAttributes{Ref: ref, StoredBytes: uploaded.Size, LogicalBytes: logicalBytes, CreatedAt: uploaded.Created}, true, nil
}

func (store *GCSStore) InspectTree(ctx context.Context, scope Scope, digest Digest, maxLogicalBytes int64) (TreeAttributes, error) {
	key, err := TreeKey(store.config.Prefix, scope, digest)
	if err != nil {
		return TreeAttributes{}, fmt.Errorf("hangar: inspect tree identity: %w", err)
	}
	if err := validateGCSLimit(maxLogicalBytes); err != nil {
		return TreeAttributes{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, store.config.ReadTimeout)
	defer cancel()
	reader, attrs, err := store.inspect(ctx, scope, digest, key, maxLogicalBytes)
	if err != nil {
		return TreeAttributes{}, err
	}
	if err := reader.Close(); err != nil {
		return TreeAttributes{}, fmt.Errorf("hangar: close verified inspect scratch: %w", err)
	}
	return attrs, nil
}

func (store *GCSStore) OpenTree(ctx context.Context, ref TreeRef, maxLogicalBytes int64) (io.ReadCloser, TreeAttributes, error) {
	if err := ref.Validate(); err != nil {
		return nil, TreeAttributes{}, fmt.Errorf("hangar: open tree reference: %w", err)
	}
	if err := validateGCSLimit(maxLogicalBytes); err != nil {
		return nil, TreeAttributes{}, err
	}
	key, err := TreeKey(store.config.Prefix, ref.Scope, ref.Digest)
	if err != nil {
		return nil, TreeAttributes{}, fmt.Errorf("hangar: open tree identity: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, store.config.ReadTimeout)
	defer cancel()
	return store.openVerified(ctx, store.objects.Object(store.config.Bucket, key).Generation(ref.Generation), ref, maxLogicalBytes, false)
}

func (store *GCSStore) DeleteTree(ctx context.Context, ref TreeRef) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("hangar: delete tree reference: %w", err)
	}
	key, err := TreeKey(store.config.Prefix, ref.Scope, ref.Digest)
	if err != nil {
		return fmt.Errorf("hangar: delete tree identity: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, store.config.WriteTimeout)
	defer cancel()
	err = store.objects.Object(store.config.Bucket, key).If(storage.Conditions{GenerationMatch: ref.Generation}).Delete(ctx)
	if err == nil || isNotFound(err) {
		return nil
	}
	if isPreconditionFailed(err) {
		return wrapSentinel(ErrConflict, "delete generation no longer matches", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return infrastructure("delete tree", err)
}

func (store *GCSStore) inspectAfterCreateConflict(ctx context.Context, scope Scope, digest Digest, maxLogicalBytes int64) (TreeAttributes, bool, error) {
	key, err := TreeKey(store.config.Prefix, scope, digest)
	if err != nil {
		return TreeAttributes{}, false, err
	}
	reader, attrs, err := store.inspect(ctx, scope, digest, key, maxLogicalBytes)
	if err != nil {
		if errors.Is(err, ErrCorrupt) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			return TreeAttributes{}, false, wrapSentinel(ErrConflict, "existing immutable tree failed verification", err)
		}
		return TreeAttributes{}, false, err
	}
	if err := reader.Close(); err != nil {
		return TreeAttributes{}, false, fmt.Errorf("hangar: close verified conflict scratch: %w", err)
	}
	return attrs, false, nil
}

func (store *GCSStore) inspect(ctx context.Context, scope Scope, digest Digest, key string, maxLogicalBytes int64) (io.ReadCloser, TreeAttributes, error) {
	handle := store.objects.Object(store.config.Bucket, key)
	current, err := handle.Attrs(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, TreeAttributes{}, wrapSentinel(ErrNotFound, "inspect tree", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, TreeAttributes{}, ctxErr
		}
		return nil, TreeAttributes{}, infrastructure("inspect tree attributes", err)
	}
	ref, err := NewTreeRef(scope, digest, current.Generation)
	if err != nil {
		return nil, TreeAttributes{}, fmt.Errorf("%w: invalid stored generation: %v", ErrCorrupt, err)
	}
	reader, attrs, err := store.openVerified(ctx, handle.Generation(current.Generation), ref, maxLogicalBytes, true)
	if err != nil {
		return nil, TreeAttributes{}, err
	}
	latest, err := handle.Attrs(ctx)
	if err != nil {
		closeErr := reader.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if isNotFound(err) || isPreconditionFailed(err) {
			return nil, TreeAttributes{}, wrapSentinel(ErrConflict, "tree changed during generation-pinned verification", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, TreeAttributes{}, errors.Join(ctxErr, closeErr)
		}
		return nil, TreeAttributes{}, infrastructure("recheck tree after generation-pinned verification", err)
	}
	if latest.Generation != current.Generation || latest.Metageneration != current.Metageneration {
		closeErr := reader.Close()
		conflictErr := fmt.Errorf("%w: tree generation changed from %d/%d to %d/%d during verification", ErrConflict, current.Generation, current.Metageneration, latest.Generation, latest.Metageneration)
		return nil, TreeAttributes{}, errors.Join(conflictErr, closeErr)
	}
	return reader, attrs, nil
}

func (store *GCSStore) openVerified(ctx context.Context, handle objectHandle, ref TreeRef, maxLogicalBytes int64, replacementIsConflict bool) (reader io.ReadCloser, attributes TreeAttributes, err error) {
	stored, err := handle.Attrs(ctx)
	if err != nil {
		if isNotFound(err) {
			if replacementIsConflict {
				return nil, TreeAttributes{}, wrapSentinel(ErrConflict, "tree changed after generation lookup", err)
			}
			return nil, TreeAttributes{}, wrapSentinel(ErrNotFound, "open tree", err)
		}
		if isPreconditionFailed(err) {
			return nil, TreeAttributes{}, wrapSentinel(ErrConflict, "open tree generation", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, TreeAttributes{}, ctxErr
		}
		return nil, TreeAttributes{}, infrastructure("read tree attributes", err)
	}
	if stored.Generation != ref.Generation {
		return nil, TreeAttributes{}, fmt.Errorf("%w: requested generation %d resolved to generation %d", ErrConflict, ref.Generation, stored.Generation)
	}
	logicalBytes, err := validateStoredMetadata(stored.Metadata, ref.Digest, maxLogicalBytes)
	if err != nil {
		return nil, TreeAttributes{}, err
	}
	if stored.Size < 0 {
		return nil, TreeAttributes{}, fmt.Errorf("%w: compressed tree has negative size", ErrCorrupt)
	}
	maxStoredBytes, err := maxCompressedRepresentation(logicalBytes)
	if err != nil {
		return nil, TreeAttributes{}, fmt.Errorf("%w: derive compressed representation limit: %v", ErrCorrupt, err)
	}
	if stored.Size > maxStoredBytes {
		return nil, TreeAttributes{}, fmt.Errorf("%w: compressed tree size %d exceeds %d-byte representation limit", ErrCorrupt, stored.Size, maxStoredBytes)
	}
	compressed, err := handle.NewReader(ctx)
	if err != nil {
		if isNotFound(err) {
			if replacementIsConflict {
				return nil, TreeAttributes{}, wrapSentinel(ErrConflict, "tree changed before generation-pinned read", err)
			}
			return nil, TreeAttributes{}, wrapSentinel(ErrNotFound, "open tree bytes", err)
		}
		if isPreconditionFailed(err) {
			return nil, TreeAttributes{}, wrapSentinel(ErrConflict, "generation-pinned read failed", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, TreeAttributes{}, ctxErr
		}
		return nil, TreeAttributes{}, infrastructure("open compressed tree", err)
	}
	compressedClosed := false
	defer func() {
		if !compressedClosed {
			if closeErr := compressed.Close(); closeErr != nil {
				err = errors.Join(err, infrastructure("close compressed tree", closeErr))
			}
		}
	}()
	countedCompressed := &countingReader{reader: contextReader{ctx: ctx, reader: compressed}}
	decoder, err := zstd.NewReader(countedCompressed, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(decoderMaxMemory))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, TreeAttributes{}, ctxErr
		}
		return nil, TreeAttributes{}, wrapSentinel(ErrCorrupt, "initialize zstd decoder", err)
	}
	decoderClosed := false
	defer func() {
		if !decoderClosed {
			decoder.Close()
		}
	}()
	scratch, cleanup, err := store.newScratch("hangar-open-*.tar")
	if err != nil {
		return nil, TreeAttributes{}, err
	}
	keepScratch := false
	defer func() {
		if !keepScratch {
			err = errors.Join(err, cleanup())
		}
	}()
	hasher := sha256.New()
	actualBytes, err := io.Copy(io.MultiWriter(scratch, hasher), io.LimitReader(decoder, maxLogicalBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, TreeAttributes{}, ctxErr
		}
		return nil, TreeAttributes{}, wrapSentinel(ErrCorrupt, "decompress tree", err)
	}
	if actualBytes > maxLogicalBytes {
		return nil, TreeAttributes{}, fmt.Errorf("%w: logical tree exceeds %d-byte limit", ErrCorrupt, maxLogicalBytes)
	}
	if actualBytes != logicalBytes {
		return nil, TreeAttributes{}, fmt.Errorf("%w: logical byte count %d does not match metadata %d", ErrCorrupt, actualBytes, logicalBytes)
	}
	if countedCompressed.bytes != stored.Size {
		return nil, TreeAttributes{}, fmt.Errorf("%w: compressed byte count %d does not match object size %d", ErrCorrupt, countedCompressed.bytes, stored.Size)
	}
	actualDigest := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
	if actualDigest != ref.Digest {
		return nil, TreeAttributes{}, fmt.Errorf("%w: tree digest %s does not match requested digest %s", ErrCorrupt, actualDigest, ref.Digest)
	}
	if err := ctx.Err(); err != nil {
		return nil, TreeAttributes{}, err
	}
	decoder.Close()
	decoderClosed = true
	closeErr := compressed.Close()
	compressedClosed = true
	if closeErr != nil {
		return nil, TreeAttributes{}, infrastructure("close compressed tree", closeErr)
	}
	if _, err := scratch.Seek(0, io.SeekStart); err != nil {
		return nil, TreeAttributes{}, fmt.Errorf("hangar: rewind verified scratch: %w", err)
	}
	keepScratch = true
	return &scratchReadCloser{file: scratch, path: scratch.Name(), remove: store.removeScratch}, TreeAttributes{Ref: ref, StoredBytes: stored.Size, LogicalBytes: actualBytes, CreatedAt: stored.Created}, nil
}

func validateStoredMetadata(metadata map[string]string, digest Digest, maxLogicalBytes int64) (int64, error) {
	if len(metadata) != 3 {
		return 0, fmt.Errorf("%w: tree metadata vocabulary is not exact", ErrCorrupt)
	}
	if metadata[metadataRepresentation] != representationZstd {
		return 0, fmt.Errorf("%w: tree representation metadata is not zstd", ErrCorrupt)
	}
	if metadata[metadataLogicalSHA256] != string(digest) {
		return 0, fmt.Errorf("%w: tree digest metadata does not match key", ErrCorrupt)
	}
	rawSize, found := metadata[metadataLogicalBytes]
	if !found {
		return 0, fmt.Errorf("%w: tree is missing logical byte metadata", ErrCorrupt)
	}
	size, err := strconv.ParseInt(rawSize, 10, 64)
	if err != nil || size < 0 || strconv.FormatInt(size, 10) != rawSize {
		return 0, fmt.Errorf("%w: invalid logical byte metadata", ErrCorrupt)
	}
	if size > maxLogicalBytes {
		return 0, fmt.Errorf("%w: declared logical size exceeds %d-byte limit", ErrCorrupt, maxLogicalBytes)
	}
	return size, nil
}

func maxCompressedRepresentation(logicalBytes int64) (int64, error) {
	const fixedOverhead int64 = 32
	if logicalBytes < 0 || logicalBytes > (math.MaxInt64-fixedOverhead)/4 {
		return 0, fmt.Errorf("logical size cannot be represented safely")
	}
	return logicalBytes*4 + fixedOverhead, nil
}
func validateGCSLimit(limit int64) error {
	if limit <= 0 || limit == math.MaxInt64 {
		return fmt.Errorf("hangar: maximum logical bytes must be positive and bounded")
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
	if errors.Is(err, storage.ErrObjectNotExist) || status.Code(err) == codes.NotFound {
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
func infrastructure(message string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return wrapSentinel(ErrInfrastructure, message, cause)
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

func (reader *scratchReadCloser) Read(buffer []byte) (int, error) { return reader.file.Read(buffer) }
func (reader *scratchReadCloser) Close() error {
	reader.once.Do(func() {
		closeErr := reader.file.Close()
		remove := reader.remove
		if remove == nil {
			remove = os.Remove
		}
		removeErr := remove(reader.path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		reader.err = errors.Join(closeErr, removeErr)
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
	Generation, Metageneration, Size int64
	Created                          time.Time
	Metadata                         map[string]string
}

type storageObjectClient struct{ client *storage.Client }

func (client storageObjectClient) Object(bucket, key string) objectHandle {
	return storageObjectHandle{handle: client.client.Bucket(bucket).Object(key)}
}

type storageObjectHandle struct{ handle *storage.ObjectHandle }

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
func (handle storageObjectHandle) Delete(ctx context.Context) error { return handle.handle.Delete(ctx) }

type storageObjectWriter struct{ writer *storage.Writer }

func (writer *storageObjectWriter) Write(content []byte) (int, error) {
	return writer.writer.Write(content)
}
func (writer *storageObjectWriter) Close() error          { return writer.writer.Close() }
func (writer *storageObjectWriter) Abort(err error) error { return writer.writer.CloseWithError(err) }
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
	return objectAttrs{Generation: attrs.Generation, Metageneration: attrs.Metageneration, Size: attrs.Size, Created: attrs.Created, Metadata: attrs.Metadata}
}

var _ Store = (*GCSStore)(nil)
