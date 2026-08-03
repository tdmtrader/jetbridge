package hangar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewStorageClientUsesExplicitUnauthenticatedJSONEndpoint(t *testing.T) {
	t.Parallel()

	content := []byte("json download")
	requests := newHTTPRequestLog()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.record(request)
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		writer.Header().Set("X-Goog-Generation", "37")
		writer.Header().Set("X-Goog-Metageneration", "1")
		writer.Header().Set("X-Goog-Stored-Content-Length", strconv.Itoa(len(content)))
		_, _ = writer.Write(content)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := NewStorageClient(ctx, server.URL+"/storage/v1/")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	reader, err := client.
		Bucket("hangar-endpoint-test").
		Object("reader-object").
		Generation(37).
		NewReader(ctx)
	require.NoError(t, err)
	actual, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, content, actual)

	recorded := requests.snapshot()
	require.Len(t, recorded, 1)
	request := recorded[0]
	require.Equal(t, http.MethodGet, request.method)
	require.Equal(t, "/storage/v1/b/hangar-endpoint-test/o/reader-object", request.escapedPath)
	require.Equal(t, "media", request.query.Get("alt"))
	require.Equal(t, "37", request.query.Get("generation"))
	require.Empty(t, request.query.Get("ifGenerationMatch"))
	require.Empty(t, request.authorization)
}

func TestNewStorageClientEmptyEndpointUsesApplicationDefaultCredentials(t *testing.T) {
	missingCredentials := filepath.Join(t.TempDir(), "missing-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missingCredentials)
	t.Setenv("STORAGE_EMULATOR_HOST", "")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := NewStorageClient(ctx, "")
	if client != nil {
		require.NoError(t, client.Close())
	}
	require.Error(t, err)
	require.ErrorContains(t, err, missingCredentials)
}

func TestGCSStoreOfficialClientPinsReadsAndMatchesDeleteGeneration(t *testing.T) {
	t.Parallel()

	const (
		bucket     = "hangar"
		generation = int64(73)
	)
	content := bytes.Repeat([]byte("official client request contract\n"), 32)
	compressed := compressForTest(t, content)
	digest := testDigest(content)
	ref, err := NewObjectRef(KindSnapshot, digest, generation)
	require.NoError(t, err)

	requests := newHTTPRequestLog()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.record(request)
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("alt") == "json":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"bucket":         bucket,
				"name":           ref.Key,
				"generation":     strconv.FormatInt(generation, 10),
				"metageneration": "1",
				"size":           strconv.Itoa(len(compressed)),
				"timeCreated":    "2026-07-27T00:00:00Z",
				"updated":        "2026-07-27T00:00:00Z",
				"metadata": testMetadata(
					digest,
					int64(len(content)),
					representationZstd,
				),
			})
		case request.Method == http.MethodGet && request.URL.Query().Get("alt") == "media":
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
			writer.Header().Set("X-Goog-Generation", strconv.FormatInt(generation, 10))
			writer.Header().Set("X-Goog-Metageneration", "1")
			writer.Header().Set("X-Goog-Stored-Content-Length", strconv.Itoa(len(compressed)))
			_, _ = writer.Write(compressed)
		case request.Method == http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := NewStorageClient(ctx, server.URL+"/storage/v1/")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})
	store, err := NewGCSStore(client, GCSConfig{
		Bucket:       bucket,
		ScratchDir:   t.TempDir(),
		ZstdLevel:    zstd.SpeedFastest,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	})
	require.NoError(t, err)

	reader, opened, err := store.Open(ctx, ref, int64(len(content)))
	require.NoError(t, err)
	require.Equal(t, ref, opened.Ref)
	require.Equal(t, content, mustReadAll(t, reader))
	require.NoError(t, reader.Close())
	require.NoError(t, store.Delete(ctx, ref))

	recorded := requests.snapshot()
	require.Len(t, recorded, 3)
	objectPath := "/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(ref.Key)
	for _, request := range recorded[:2] {
		require.Equal(t, http.MethodGet, request.method)
		require.Equal(t, objectPath, request.escapedPath)
		require.Equal(t, strconv.FormatInt(generation, 10), request.query.Get("generation"))
		require.Empty(t, request.query.Get("ifGenerationMatch"))
		require.Empty(t, request.authorization)
	}
	require.Equal(t, "json", recorded[0].query.Get("alt"))
	require.Equal(t, "media", recorded[1].query.Get("alt"))

	deleteRequest := recorded[2]
	require.Equal(t, http.MethodDelete, deleteRequest.method)
	require.Equal(t, objectPath, deleteRequest.escapedPath)
	require.Empty(t, deleteRequest.query.Get("generation"))
	require.Equal(
		t,
		strconv.FormatInt(generation, 10),
		deleteRequest.query.Get("ifGenerationMatch"),
	)
	require.Empty(t, deleteRequest.authorization)
}

func TestGCSStoreValidatesConfiguration(t *testing.T) {
	t.Parallel()

	client := newMemoryObjectClient()
	valid := GCSConfig{
		Bucket:       "hangar",
		ScratchDir:   t.TempDir(),
		ZstdLevel:    zstd.SpeedDefault,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}

	tests := map[string]GCSConfig{
		"empty bucket":          withGCSConfig(valid, func(config *GCSConfig) { config.Bucket = "" }),
		"relative scratch":      withGCSConfig(valid, func(config *GCSConfig) { config.ScratchDir = "scratch" }),
		"missing scratch":       withGCSConfig(valid, func(config *GCSConfig) { config.ScratchDir = filepath.Join(t.TempDir(), "missing") }),
		"zero read timeout":     withGCSConfig(valid, func(config *GCSConfig) { config.ReadTimeout = 0 }),
		"zero write timeout":    withGCSConfig(valid, func(config *GCSConfig) { config.WriteTimeout = 0 }),
		"invalid encoder level": withGCSConfig(valid, func(config *GCSConfig) { config.ZstdLevel = zstd.EncoderLevel(999) }),
	}
	for name, config := range tests {
		config := config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := newGCSStore(client, config)
			require.Error(t, err)
		})
	}

	store, err := newGCSStore(client, valid)
	require.NoError(t, err)
	require.NotNil(t, store)
}

func TestGCSStoreEnsureWritesVerifiedZstdObject(t *testing.T) {
	t.Parallel()

	store, objects, scratch := newTestGCSStore(t)
	content := bytes.Repeat([]byte("verified source\n"), 2048)
	digest := testDigest(content)

	attrs, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), int64(len(content)))
	require.NoError(t, err)
	require.Equal(t, KindSnapshot, attrs.Ref.Kind)
	require.Equal(t, digest, attrs.Ref.Digest)
	require.Positive(t, attrs.Ref.Generation)
	require.Equal(t, int64(len(content)), attrs.UncompressedBytes)

	object := objects.object(attrs.Ref.Key)
	require.Equal(t, storage.Conditions{DoesNotExist: true}, objects.writeConditions[0])
	require.Equal(t, string(digest), object.metadata[metadataUncompressedSHA256])
	require.Equal(t, strconv.Itoa(len(content)), object.metadata[metadataUncompressedBytes])
	require.Equal(t, representationZstd, object.metadata[metadataRepresentation])
	require.Equal(t, int64(len(object.data)), attrs.CompressedBytes)
	require.Equal(t, content, decompressForTest(t, object.data))
	requireScratchEmpty(t, scratch)
}

func TestGCSStoreEnsureRejectsUnverifiedSourceBeforeWriting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content []byte
		digest  Digest
		limit   int64
	}{
		{
			name:    "digest mismatch",
			content: []byte("actual"),
			digest:  testDigest([]byte("expected")),
			limit:   64,
		},
		{
			name:    "size overflow",
			content: []byte("too large"),
			digest:  testDigest([]byte("too large")),
			limit:   3,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, objects, scratch := newTestGCSStore(t)

			_, err := store.Ensure(context.Background(), KindSnapshot, test.digest, bytes.NewReader(test.content), test.limit)
			require.ErrorIs(t, err, ErrCorrupt)
			require.Empty(t, objects.writeConditions)
			require.Empty(t, objects.objects)
			requireScratchEmpty(t, scratch)
		})
	}
}

func TestGCSStoreEnsureVerifiesCreateConflicts(t *testing.T) {
	t.Parallel()

	content := []byte("immutable bytes")
	digest := testDigest(content)

	t.Run("identical existing object", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ref := objects.put(t, KindSnapshot, digest, content)

		attrs, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), int64(len(content)))
		require.NoError(t, err)
		require.Equal(t, ref, attrs.Ref)
		require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
		requireScratchEmpty(t, scratch)
	})

	t.Run("different existing object", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		objects.putAtDigest(t, KindSnapshot, digest, []byte("different bytes"), string(digest), int64(len("different bytes")))

		_, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), int64(len(content)))
		require.ErrorIs(t, err, ErrConflict)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSStoreEnsurePreservesUploadErrorsAndCleansScratch(t *testing.T) {
	t.Parallel()

	content := []byte("upload me")
	digest := testDigest(content)
	sentinel := errors.New("writer failed")

	t.Run("write", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		objects.writeErr = sentinel

		_, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, sentinel)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})

	t.Run("close", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		objects.closeErr = sentinel

		_, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, sentinel)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSStoreEnsureClosesBlockingSourceOnlyOnCancellation(t *testing.T) {
	t.Parallel()

	t.Run("timeout closes a blocked read closer", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, time.Second, 20*time.Millisecond)
		source := newBlockingEnsureReadCloser()
		result := make(chan error, 1)
		go func() {
			_, err := store.Ensure(
				context.Background(),
				KindSnapshot,
				testDigest([]byte("unreachable")),
				source,
				64,
			)
			result <- err
		}()

		select {
		case <-source.started:
		case <-time.After(2 * time.Second):
			t.Fatal("Ensure did not begin reading the source")
		}
		select {
		case err := <-result:
			require.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(2 * time.Second):
			t.Fatal("Ensure did not close the blocked source after timeout")
		}
		require.Equal(t, 1, source.closeCalls())
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})

	t.Run("ordinary success leaves source ownership with caller", func(t *testing.T) {
		t.Parallel()
		store, _, scratch := newTestGCSStore(t)
		content := []byte("caller owns this source")
		source := &trackingEnsureReadCloser{Reader: bytes.NewReader(content)}

		_, err := store.Ensure(
			context.Background(),
			KindSnapshot,
			testDigest(content),
			source,
			int64(len(content)),
		)

		require.NoError(t, err)
		require.Equal(t, 0, source.closeCalls)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSStoreEnsureSurfacesScratchFailures(t *testing.T) {
	t.Parallel()

	content := []byte("scratch failures are not acknowledgements")
	digest := testDigest(content)

	t.Run("ENOSPC while compressing", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		store.createScratch = func(directory, pattern string) (scratchFile, error) {
			file, err := os.CreateTemp(directory, pattern)
			if err != nil {
				return nil, err
			}
			return &writeFailingScratch{scratchFile: file, err: syscall.ENOSPC}, nil
		}

		attrs, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), 64)

		require.ErrorIs(t, err, syscall.ENOSPC)
		require.Zero(t, attrs)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})

	t.Run("cleanup failure converts success to an error", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		sentinel := errors.New("scratch cleanup failed")
		store.removeScratch = func(path string) error {
			return errors.Join(os.Remove(path), sentinel)
		}

		attrs, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), 64)

		require.ErrorIs(t, err, sentinel)
		require.Zero(t, attrs)
		require.Len(t, objects.objects, 1)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSStoreInspectPinsAndVerifiesTheCurrentGeneration(t *testing.T) {
	t.Parallel()

	store, objects, scratch := newTestGCSStore(t)
	content := []byte("inspect me")
	digest := testDigest(content)
	ref := objects.put(t, KindCheckpoint, digest, content)

	attrs, err := store.Inspect(context.Background(), KindCheckpoint, digest, 64)
	require.NoError(t, err)
	require.Equal(t, ref, attrs.Ref)
	require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
	require.Equal(t, int64(len(content)), attrs.UncompressedBytes)
	requireScratchEmpty(t, scratch)
}

func TestGCSStoreInspectMapsMissingAndReplacement(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		store, _, scratch := newTestGCSStore(t)

		_, err := store.Inspect(context.Background(), KindSnapshot, testDigest([]byte("missing")), 64)
		require.ErrorIs(t, err, ErrNotFound)
		requireScratchEmpty(t, scratch)
	})

	t.Run("grpc missing", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		objects.attrsErr = status.Error(codes.NotFound, "missing")

		_, err := store.Inspect(context.Background(), KindSnapshot, testDigest([]byte("missing")), 64)
		require.ErrorIs(t, err, ErrNotFound)
		requireScratchEmpty(t, scratch)
	})

	t.Run("replacement after attrs", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		content := []byte("first")
		digest := testDigest(content)
		objects.put(t, KindSnapshot, digest, content)
		objects.retainVersions = true
		objects.afterAttrs = func(key string) {
			objects.replace(t, key, []byte("replacement"), string(digest), int64(len("replacement")))
		}

		_, err := store.Inspect(context.Background(), KindSnapshot, digest, 64)
		require.ErrorIs(t, err, ErrConflict)
		requireScratchEmpty(t, scratch)
	})

	t.Run("metadata replacement during read", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		content := []byte("stable bytes")
		digest := testDigest(content)
		objects.put(t, KindSnapshot, digest, content)
		objects.afterRead = func(key string) {
			objects.updateMetadata(key, map[string]string{
				metadataUncompressedSHA256: string(digest),
				metadataUncompressedBytes:  strconv.Itoa(len(content)),
				metadataRepresentation:     "changed",
			})
		}

		_, err := store.Inspect(context.Background(), KindSnapshot, digest, 64)
		require.ErrorIs(t, err, ErrConflict)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSStoreOpenReturnsVerifiedScratchBackedReader(t *testing.T) {
	t.Parallel()

	store, objects, scratch := newTestGCSStore(t)
	content := bytes.Repeat([]byte("read me\n"), 100)
	digest := testDigest(content)
	ref := objects.put(t, KindSnapshot, digest, content)

	reader, attrs, err := store.Open(context.Background(), ref, int64(len(content)))
	require.NoError(t, err)
	require.Equal(t, ref, attrs.Ref)
	require.Equal(t, content, mustReadAll(t, reader))
	require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
	require.Len(t, scratchEntries(t, scratch), 1)
	require.NoError(t, reader.Close())
	requireScratchEmpty(t, scratch)
}

func TestGCSStoreOpenDoesNotExposeReaderWhenCompressedReaderCloseFails(t *testing.T) {
	t.Parallel()

	store, objects, scratch := newTestGCSStore(t)
	content := []byte("close must be part of verification")
	ref := objects.put(t, KindSnapshot, testDigest(content), content)
	sentinel := errors.New("compressed reader close failed")
	objects.readCloseErr = sentinel

	reader, _, err := store.Open(context.Background(), ref, int64(len(content)))
	require.ErrorIs(t, err, sentinel)
	require.Nil(t, reader)
	requireScratchEmpty(t, scratch)
}

func TestGCSStoreOpenRejectsCorruptionBeforeReturningReader(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("bounded content"), 32)
	digest := testDigest(content)
	compressed := compressForTest(t, content)

	tests := []struct {
		name     string
		data     []byte
		metadata map[string]string
		size     int64
		limit    int64
	}{
		{
			name:     "invalid representation metadata",
			data:     compressed,
			metadata: testMetadata(digest, int64(len(content)), "gzip"),
			size:     int64(len(compressed)),
			limit:    int64(len(content)),
		},
		{
			name:     "invalid digest metadata",
			data:     compressed,
			metadata: testMetadata(testDigest([]byte("other")), int64(len(content)), representationZstd),
			size:     int64(len(compressed)),
			limit:    int64(len(content)),
		},
		{
			name:     "invalid size metadata",
			data:     compressed,
			metadata: testMetadata(digest, int64(len(content))+1, representationZstd),
			size:     int64(len(compressed)),
			limit:    int64(len(content)) + 1,
		},
		{
			name:     "zstd bomb",
			data:     compressed,
			metadata: testMetadata(digest, 8, representationZstd),
			size:     int64(len(compressed)),
			limit:    8,
		},
		{
			name:     "truncated zstd",
			data:     compressed[:len(compressed)-1],
			metadata: testMetadata(digest, int64(len(content)), representationZstd),
			size:     int64(len(compressed) - 1),
			limit:    int64(len(content)),
		},
		{
			name:     "compressed size mismatch",
			data:     compressed,
			metadata: testMetadata(digest, int64(len(content)), representationZstd),
			size:     int64(len(compressed)) + 1,
			limit:    int64(len(content)),
		},
		{
			name:     "uncompressed digest mismatch",
			data:     compressForTest(t, []byte("different")),
			metadata: testMetadata(digest, int64(len("different")), representationZstd),
			size:     int64(len(compressForTest(t, []byte("different")))),
			limit:    int64(len(content)),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, objects, scratch := newTestGCSStore(t)
			ref := objects.putRaw(t, KindSnapshot, digest, test.data, test.metadata, test.size)

			reader, _, err := store.Open(context.Background(), ref, test.limit)
			require.ErrorIs(t, err, ErrCorrupt)
			require.Nil(t, reader)
			requireScratchEmpty(t, scratch)
		})
	}
}

func TestGCSStoreOpenRejectsNonCanonicalMetadataAndCompressedOverflowBeforeRead(t *testing.T) {
	t.Parallel()

	content := []byte("x")
	digest := testDigest(content)
	compressed := compressForTest(t, content)

	metadataCases := map[string]map[string]string{
		"extra key": func() map[string]string {
			metadata := testMetadata(digest, int64(len(content)), representationZstd)
			metadata["unexpected"] = "value"
			return metadata
		}(),
		"plus-prefixed size": {
			metadataUncompressedSHA256: string(digest),
			metadataUncompressedBytes:  "+1",
			metadataRepresentation:     representationZstd,
		},
		"leading-zero size": {
			metadataUncompressedSHA256: string(digest),
			metadataUncompressedBytes:  "01",
			metadataRepresentation:     representationZstd,
		},
	}
	for name, metadata := range metadataCases {
		name, metadata := name, metadata
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, objects, scratch := newTestGCSStore(t)
			ref := objects.putRaw(t, KindSnapshot, digest, compressed, metadata, int64(len(compressed)))

			reader, _, err := store.Open(context.Background(), ref, 64)

			require.ErrorIs(t, err, ErrCorrupt)
			require.Nil(t, reader)
			require.Empty(t, objects.readGenerations)
			requireScratchEmpty(t, scratch)
		})
	}

	t.Run("oversized representation", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		oversized := bytes.Repeat([]byte{0}, 128)
		ref := objects.putRaw(
			t,
			KindSnapshot,
			digest,
			oversized,
			testMetadata(digest, int64(len(content)), representationZstd),
			int64(len(oversized)),
		)

		reader, _, err := store.Open(context.Background(), ref, 64)

		require.ErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		require.Empty(t, objects.readGenerations)
		requireScratchEmpty(t, scratch)
	})

	t.Run("checked bound overflow", func(t *testing.T) {
		t.Parallel()
		_, err := maxCompressedRepresentation((math.MaxInt64-32)/4 + 1)
		require.Error(t, err)
	})
}

func TestGCSStoreDeleteUsesGenerationAndIsIdempotentForMissingObjects(t *testing.T) {
	t.Parallel()

	content := []byte("delete me")
	digest := testDigest(content)

	t.Run("matching generation", func(t *testing.T) {
		t.Parallel()
		store, objects, _ := newTestGCSStore(t)
		ref := objects.put(t, KindSnapshot, digest, content)

		require.NoError(t, store.Delete(context.Background(), ref))
		require.Equal(t, []storage.Conditions{{GenerationMatch: ref.Generation}}, objects.deleteConditions)
		require.Empty(t, objects.objects)
		require.NoError(t, store.Delete(context.Background(), ref))
	})

	t.Run("wrong generation", func(t *testing.T) {
		t.Parallel()
		store, objects, _ := newTestGCSStore(t)
		ref := objects.put(t, KindSnapshot, digest, content)
		ref.Generation++

		err := store.Delete(context.Background(), ref)
		require.ErrorIs(t, err, ErrConflict)
	})

	t.Run("grpc generation conflict", func(t *testing.T) {
		t.Parallel()
		store, objects, _ := newTestGCSStore(t)
		ref := objects.put(t, KindSnapshot, digest, content)
		objects.deleteErr = status.Error(codes.FailedPrecondition, "generation mismatch")

		err := store.Delete(context.Background(), ref)
		require.ErrorIs(t, err, ErrConflict)
	})

	t.Run("other error", func(t *testing.T) {
		t.Parallel()
		store, objects, _ := newTestGCSStore(t)
		ref := objects.put(t, KindSnapshot, digest, content)
		sentinel := errors.New("delete failed")
		objects.deleteErr = sentinel

		err := store.Delete(context.Background(), ref)
		require.ErrorIs(t, err, sentinel)
	})
}

func TestGCSStoreHonorsCancellationAndTimeouts(t *testing.T) {
	t.Parallel()

	content := []byte("slow object")
	digest := testDigest(content)

	t.Run("cancelled before ensure", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := store.Ensure(ctx, KindSnapshot, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})

	t.Run("write timeout", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, time.Second, 10*time.Millisecond)
		objects.blockWrite = true

		_, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		requireScratchEmpty(t, scratch)
	})

	t.Run("read timeout", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, 10*time.Millisecond, time.Second)
		ref := objects.put(t, KindSnapshot, digest, content)
		objects.blockRead = true

		reader, _, err := store.Open(context.Background(), ref, 64)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("read body timeout", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, 10*time.Millisecond, time.Second)
		ref := objects.put(t, KindSnapshot, digest, content)
		objects.blockReadBody = true

		reader, _, err := store.Open(context.Background(), ref, 64)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSStoreRejectsInvalidMethodArguments(t *testing.T) {
	t.Parallel()

	store, _, _ := newTestGCSStore(t)
	content := []byte("content")
	digest := testDigest(content)

	_, err := store.Ensure(context.Background(), KindSnapshot, digest, bytes.NewReader(content), 0)
	require.Error(t, err)
	_, err = store.Inspect(context.Background(), KindSnapshot, digest, 0)
	require.Error(t, err)
	_, _, err = store.Open(context.Background(), ObjectRef{}, 1)
	require.Error(t, err)
	require.Error(t, store.Delete(context.Background(), ObjectRef{}))
}

func withGCSConfig(config GCSConfig, mutate func(*GCSConfig)) GCSConfig {
	mutate(&config)
	return config
}

func newTestGCSStore(t *testing.T) (*GCSStore, *memoryObjectClient, string) {
	t.Helper()
	return newTestGCSStoreWithTimeouts(t, time.Second, time.Second)
}

func newTestGCSStoreWithTimeouts(t *testing.T, readTimeout, writeTimeout time.Duration) (*GCSStore, *memoryObjectClient, string) {
	t.Helper()
	scratch := t.TempDir()
	objects := newMemoryObjectClient()
	store, err := newGCSStore(objects, GCSConfig{
		Bucket:       "hangar",
		ScratchDir:   scratch,
		ZstdLevel:    zstd.SpeedFastest,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	})
	require.NoError(t, err)
	return store, objects, scratch
}

func testDigest(content []byte) Digest {
	sum := sha256.Sum256(content)
	return Digest(fmt.Sprintf("sha256:%x", sum[:]))
}

func testMetadata(digest Digest, size int64, representation string) map[string]string {
	return map[string]string{
		metadataUncompressedSHA256: string(digest),
		metadataUncompressedBytes:  strconv.FormatInt(size, 10),
		metadataRepresentation:     representation,
	}
}

func compressForTest(t *testing.T, content []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	require.NoError(t, err)
	_, err = encoder.Write(content)
	require.NoError(t, err)
	require.NoError(t, encoder.Close())
	return compressed.Bytes()
}

func decompressForTest(t *testing.T, compressed []byte) []byte {
	t.Helper()
	decoder, err := zstd.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	defer decoder.Close()
	content, err := io.ReadAll(decoder)
	require.NoError(t, err)
	return content
}

func mustReadAll(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	return content
}

func scratchEntries(t *testing.T, scratch string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	return entries
}

func requireScratchEmpty(t *testing.T, scratch string) {
	t.Helper()
	require.Empty(t, scratchEntries(t, scratch))
}

type recordedHTTPRequest struct {
	method        string
	escapedPath   string
	query         url.Values
	authorization string
}

type httpRequestLog struct {
	mu       sync.Mutex
	requests []recordedHTTPRequest
}

func newHTTPRequestLog() *httpRequestLog {
	return &httpRequestLog{}
}

func (log *httpRequestLog) record(request *http.Request) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.requests = append(log.requests, recordedHTTPRequest{
		method:        request.Method,
		escapedPath:   request.URL.EscapedPath(),
		query:         request.URL.Query(),
		authorization: request.Header.Get("Authorization"),
	})
}

func (log *httpRequestLog) snapshot() []recordedHTTPRequest {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]recordedHTTPRequest(nil), log.requests...)
}

type memoryObjectClient struct {
	mu sync.Mutex

	objects  map[string]memoryObject
	versions map[string]map[int64]memoryObject
	nextGen  int64

	writeConditions  []storage.Conditions
	deleteConditions []storage.Conditions
	updateConditions []storage.Conditions
	updates          []map[string]string
	readGenerations  []int64

	writeErr       error
	closeErr       error
	attrsErr       error
	deleteErr      error
	updateErr      error
	readCloseErr   error
	blockRead      bool
	blockReadBody  bool
	blockWrite     bool
	retainVersions bool
	afterAttrs     func(string)
	afterRead      func(string)
}

type memoryObject struct {
	data       []byte
	metadata   map[string]string
	generation int64
	size       int64
	created    time.Time
	metaGen    int64
}

func newMemoryObjectClient() *memoryObjectClient {
	return &memoryObjectClient{
		objects:  make(map[string]memoryObject),
		versions: make(map[string]map[int64]memoryObject),
		nextGen:  1,
	}
}

func (client *memoryObjectClient) Object(bucket, key string) objectHandle {
	return &memoryObjectHandle{client: client, bucket: bucket, key: key}
}

func (client *memoryObjectClient) object(key string) memoryObject {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.objects[key]
}

func (client *memoryObjectClient) put(t *testing.T, kind Kind, digest Digest, content []byte) ObjectRef {
	t.Helper()
	return client.putAtDigest(t, kind, digest, content, string(digest), int64(len(content)))
}

func (client *memoryObjectClient) putAtDigest(t *testing.T, kind Kind, digest Digest, content []byte, metadataDigest string, metadataSize int64) ObjectRef {
	t.Helper()
	compressed := compressForTest(t, content)
	return client.putRaw(t, kind, digest, compressed, map[string]string{
		metadataUncompressedSHA256: metadataDigest,
		metadataUncompressedBytes:  strconv.FormatInt(metadataSize, 10),
		metadataRepresentation:     representationZstd,
	}, int64(len(compressed)))
}

func (client *memoryObjectClient) putRaw(t *testing.T, kind Kind, digest Digest, data []byte, metadata map[string]string, size int64) ObjectRef {
	t.Helper()
	key, err := Key(kind, digest)
	require.NoError(t, err)
	client.mu.Lock()
	generation := client.nextGen
	client.nextGen++
	client.objects[key] = memoryObject{
		data:       append([]byte(nil), data...),
		metadata:   cloneMetadata(metadata),
		generation: generation,
		size:       size,
		created:    time.Unix(generation, 0).UTC(),
		metaGen:    1,
	}
	client.mu.Unlock()
	ref, err := NewObjectRef(kind, digest, generation)
	require.NoError(t, err)
	return ref
}

func (client *memoryObjectClient) replace(t *testing.T, key string, content []byte, metadataDigest string, metadataSize int64) {
	t.Helper()
	compressed := compressForTest(t, content)
	client.mu.Lock()
	if client.retainVersions {
		if previous, found := client.objects[key]; found {
			if client.versions[key] == nil {
				client.versions[key] = make(map[int64]memoryObject)
			}
			client.versions[key][previous.generation] = previous
		}
	}
	generation := client.nextGen
	client.nextGen++
	client.objects[key] = memoryObject{
		data:       compressed,
		metadata:   testMetadata(Digest(metadataDigest), metadataSize, representationZstd),
		generation: generation,
		size:       int64(len(compressed)),
		created:    time.Unix(generation, 0).UTC(),
		metaGen:    1,
	}
	client.mu.Unlock()
}

func (client *memoryObjectClient) updateMetadata(key string, metadata map[string]string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	object := client.objects[key]
	object.metadata = cloneMetadata(metadata)
	object.metaGen++
	client.objects[key] = object
}

type memoryObjectHandle struct {
	client     *memoryObjectClient
	bucket     string
	key        string
	conditions storage.Conditions
	generation int64
}

func (handle *memoryObjectHandle) If(conditions storage.Conditions) objectHandle {
	copy := *handle
	copy.conditions = conditions
	return &copy
}

func (handle *memoryObjectHandle) Generation(generation int64) objectHandle {
	copy := *handle
	copy.generation = generation
	return &copy
}

func (handle *memoryObjectHandle) NewWriter(ctx context.Context) objectWriter {
	handle.client.mu.Lock()
	handle.client.writeConditions = append(handle.client.writeConditions, handle.conditions)
	handle.client.mu.Unlock()
	return &memoryObjectWriter{ctx: ctx, handle: handle}
}

func (handle *memoryObjectHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	handle.client.mu.Lock()
	block := handle.client.blockRead
	blockBody := handle.client.blockReadBody
	closeErr := handle.client.readCloseErr
	object, found := handle.client.lookupLocked(handle.key, handle.generation)
	after := handle.client.afterRead
	handle.client.afterRead = nil
	if handle.generation != 0 {
		handle.client.readGenerations = append(handle.client.readGenerations, handle.generation)
	}
	handle.client.mu.Unlock()
	if after != nil {
		after(handle.key)
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if !found || (handle.generation != 0 && handle.generation != object.generation) {
		return nil, &googleapi.Error{Code: 404, Message: "object generation absent"}
	}
	return &memoryObjectReader{
		ctx:      ctx,
		reader:   bytes.NewReader(object.data),
		block:    blockBody,
		closeErr: closeErr,
	}, nil
}

func (handle *memoryObjectHandle) Attrs(context.Context) (objectAttrs, error) {
	handle.client.mu.Lock()
	if handle.client.attrsErr != nil {
		defer handle.client.mu.Unlock()
		return objectAttrs{}, handle.client.attrsErr
	}
	object, found := handle.client.lookupLocked(handle.key, handle.generation)
	if !found {
		handle.client.mu.Unlock()
		return objectAttrs{}, &googleapi.Error{Code: 404, Message: "object generation absent"}
	}
	attrs := objectAttrs{
		Generation:     object.generation,
		Metageneration: object.metaGen,
		Size:           object.size,
		Created:        object.created,
		Metadata:       cloneMetadata(object.metadata),
	}
	after := handle.client.afterAttrs
	handle.client.afterAttrs = nil
	handle.client.mu.Unlock()
	if after != nil {
		after(handle.key)
	}
	return attrs, nil
}

func (client *memoryObjectClient) lookupLocked(key string, generation int64) (memoryObject, bool) {
	current, found := client.objects[key]
	if generation == 0 {
		return current, found
	}
	if found && current.generation == generation {
		return current, true
	}
	version, found := client.versions[key][generation]
	return version, found
}

func (handle *memoryObjectHandle) Update(
	_ context.Context,
	update storage.ObjectAttrsToUpdate,
) (objectAttrs, error) {
	handle.client.mu.Lock()
	defer handle.client.mu.Unlock()
	handle.client.updateConditions = append(handle.client.updateConditions, handle.conditions)
	handle.client.updates = append(handle.client.updates, cloneMetadata(update.Metadata))
	if handle.client.updateErr != nil {
		return objectAttrs{}, handle.client.updateErr
	}
	object, found := handle.client.objects[handle.key]
	if !found {
		return objectAttrs{}, &googleapi.Error{Code: 404, Message: "missing"}
	}
	if handle.conditions.GenerationMatch != 0 && handle.conditions.GenerationMatch != object.generation {
		return objectAttrs{}, &googleapi.Error{Code: 412, Message: "generation mismatch"}
	}
	if handle.conditions.MetagenerationMatch != 0 && handle.conditions.MetagenerationMatch != object.metaGen {
		return objectAttrs{}, &googleapi.Error{Code: 412, Message: "metageneration mismatch"}
	}
	object.metadata = cloneMetadata(update.Metadata)
	object.metaGen++
	handle.client.objects[handle.key] = object
	return objectAttrs{
		Generation:     object.generation,
		Metageneration: object.metaGen,
		Size:           object.size,
		Created:        object.created,
		Metadata:       cloneMetadata(object.metadata),
	}, nil
}

func (handle *memoryObjectHandle) Delete(context.Context) error {
	handle.client.mu.Lock()
	defer handle.client.mu.Unlock()
	handle.client.deleteConditions = append(handle.client.deleteConditions, handle.conditions)
	if handle.client.deleteErr != nil {
		return handle.client.deleteErr
	}
	object, found := handle.client.objects[handle.key]
	if !found {
		return &googleapi.Error{Code: 404, Message: "missing"}
	}
	if handle.conditions.GenerationMatch != 0 && handle.conditions.GenerationMatch != object.generation {
		return &googleapi.Error{Code: 412, Message: "generation mismatch"}
	}
	delete(handle.client.objects, handle.key)
	return nil
}

type memoryObjectWriter struct {
	ctx      context.Context
	handle   *memoryObjectHandle
	buffer   bytes.Buffer
	metadata map[string]string
	attrs    objectAttrs
	aborted  bool
}

func (writer *memoryObjectWriter) SetMetadata(metadata map[string]string) {
	writer.metadata = cloneMetadata(metadata)
}

func (writer *memoryObjectWriter) Write(content []byte) (int, error) {
	writer.handle.client.mu.Lock()
	block := writer.handle.client.blockWrite
	err := writer.handle.client.writeErr
	writer.handle.client.mu.Unlock()
	if block {
		<-writer.ctx.Done()
		return 0, writer.ctx.Err()
	}
	if err != nil {
		return 0, err
	}
	return writer.buffer.Write(content)
}

func (writer *memoryObjectWriter) Close() error {
	client := writer.handle.client
	client.mu.Lock()
	defer client.mu.Unlock()
	if writer.aborted {
		return nil
	}
	if client.closeErr != nil {
		return client.closeErr
	}
	if writer.handle.conditions.DoesNotExist {
		if _, found := client.objects[writer.handle.key]; found {
			return &googleapi.Error{Code: 412, Message: "already exists"}
		}
	}
	generation := client.nextGen
	client.nextGen++
	object := memoryObject{
		data:       append([]byte(nil), writer.buffer.Bytes()...),
		metadata:   cloneMetadata(writer.metadata),
		generation: generation,
		size:       int64(writer.buffer.Len()),
		created:    time.Unix(generation, 0).UTC(),
		metaGen:    1,
	}
	client.objects[writer.handle.key] = object
	writer.attrs = objectAttrs{
		Generation:     generation,
		Metageneration: object.metaGen,
		Size:           object.size,
		Created:        object.created,
		Metadata:       cloneMetadata(object.metadata),
	}
	return nil
}

func (writer *memoryObjectWriter) Abort(error) error {
	writer.aborted = true
	return nil
}

func (writer *memoryObjectWriter) Attrs() objectAttrs {
	return writer.attrs
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

type memoryObjectReader struct {
	ctx      context.Context
	reader   io.Reader
	block    bool
	closeErr error
}

func (reader *memoryObjectReader) Read(buffer []byte) (int, error) {
	if reader.block {
		<-reader.ctx.Done()
		return 0, reader.ctx.Err()
	}
	return reader.reader.Read(buffer)
}

func (reader *memoryObjectReader) Close() error {
	return reader.closeErr
}

type blockingEnsureReadCloser struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	mu        sync.Mutex
	calls     int
}

func newBlockingEnsureReadCloser() *blockingEnsureReadCloser {
	return &blockingEnsureReadCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (reader *blockingEnsureReadCloser) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.closed
	return 0, io.ErrClosedPipe
}

func (reader *blockingEnsureReadCloser) Close() error {
	reader.mu.Lock()
	reader.calls++
	reader.mu.Unlock()
	reader.closeOnce.Do(func() { close(reader.closed) })
	return nil
}

func (reader *blockingEnsureReadCloser) closeCalls() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type trackingEnsureReadCloser struct {
	io.Reader
	closeCalls int
}

func (reader *trackingEnsureReadCloser) Close() error {
	reader.closeCalls++
	return nil
}

type writeFailingScratch struct {
	scratchFile
	err error
}

func (scratch *writeFailingScratch) Write([]byte) (int, error) {
	return 0, scratch.err
}

// The artifact daemon builds its GCSConfig without naming a compression level
// (cmd/artifact-daemon/main.go). zstd.EncoderLevel's zero value is not a valid
// level, so an unset field made every Hangar-enabled daemon exit at boot with
// "invalid zstd encoder configuration: unknown encoder level" — the store is
// unreachable in production while every test passed, because the tests all set
// the field explicitly. Treat the zero value as "use the default".
func TestGCSStoreDefaultsUnsetZstdLevel(t *testing.T) {
	scratch := t.TempDir()
	store, err := newGCSStore(newMemoryObjectClient(), GCSConfig{
		Bucket:       "bucket",
		ScratchDir:   scratch,
		ReadTimeout:  time.Minute,
		WriteTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("a config with no ZstdLevel must be usable: %v", err)
	}
	if store.config.ZstdLevel != zstd.SpeedDefault {
		t.Fatalf("ZstdLevel = %v, want SpeedDefault", store.config.ZstdLevel)
	}
}
