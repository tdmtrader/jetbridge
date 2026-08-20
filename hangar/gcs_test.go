package hangar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
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

const testScope Scope = "build-artifacts"

func TestGCSStoreValidatesConfigurationAndDefaultsCompression(t *testing.T) {
	t.Parallel()
	valid := GCSConfig{Bucket: "bucket", Prefix: "deployment/blue", ScratchDir: t.TempDir(), ReadTimeout: time.Second, WriteTimeout: time.Second}
	tests := map[string]GCSConfig{
		"empty bucket":       withGCSConfig(valid, func(c *GCSConfig) { c.Bucket = "" }),
		"invalid prefix":     withGCSConfig(valid, func(c *GCSConfig) { c.Prefix = "/deployment" }),
		"relative scratch":   withGCSConfig(valid, func(c *GCSConfig) { c.ScratchDir = "scratch" }),
		"missing scratch":    withGCSConfig(valid, func(c *GCSConfig) { c.ScratchDir = filepath.Join(t.TempDir(), "missing") }),
		"zero read timeout":  withGCSConfig(valid, func(c *GCSConfig) { c.ReadTimeout = 0 }),
		"zero write timeout": withGCSConfig(valid, func(c *GCSConfig) { c.WriteTimeout = 0 }),
		"invalid zstd level": withGCSConfig(valid, func(c *GCSConfig) { c.ZstdLevel = zstd.EncoderLevel(999) }),
	}
	for name, config := range tests {
		config := config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := newGCSStore(newMemoryObjectClient(), config)
			require.Error(t, err)
		})
	}
	store, err := newGCSStore(newMemoryObjectClient(), valid)
	require.NoError(t, err)
	require.Equal(t, zstd.SpeedDefault, store.config.ZstdLevel)
	_, err = NewGCSStore(nil, valid)
	require.Error(t, err)
}

func TestGCSEnsureTreeCreatesConditionallyWithExactMetadata(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := bytes.Repeat([]byte("canonical tree\n"), 100)
	digest := digestFor(content)
	attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), int64(len(content)))
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, TreeRef{Scope: testScope, Digest: digest, Generation: 1}, attrs.Ref)
	require.Equal(t, int64(len(content)), attrs.LogicalBytes)
	require.Equal(t, []storage.Conditions{{DoesNotExist: true}}, objects.writeConditions)
	key := "deployment/blue/hangar/v1/scopes/build-artifacts/trees/sha256/" + string(digest)[7:] + ".tar.zst"
	object := objects.objects[key]
	require.Equal(t, metadataFor(digest, int64(len(content))), object.metadata)
	require.Equal(t, content, decompressTest(t, object.data))
	requireScratchEmpty(t, scratch)
}

func TestGCSEnsureTreeRejectsUnverifiedOrOversizedSourceBeforeUpload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []byte
		digest  Digest
		limit   int64
		wantErr error
	}{
		{name: "digest mismatch", content: []byte("actual"), digest: digestFor([]byte("expected")), limit: 64, wantErr: ErrCorrupt},
		{name: "logical size overflow", content: []byte("too large"), digest: digestFor([]byte("too large")), limit: 3, wantErr: ErrLimitExceeded},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, objects, scratch := newTestGCSStore(t)
			attrs, created, err := store.EnsureTree(context.Background(), testScope, test.digest, bytes.NewReader(test.content), test.limit)
			require.ErrorIs(t, err, test.wantErr)
			require.Zero(t, attrs)
			require.False(t, created)
			require.Empty(t, objects.writeConditions)
			require.Empty(t, objects.objects)
			requireScratchEmpty(t, scratch)
		})
	}
}

func TestGCSEnsureTreeRejectsOversizedCompressedScratchBeforeCreatingWriter(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := []byte("x")
	digest := digestFor(content)
	store.createScratch = func(directory, pattern string) (scratchFile, error) {
		file, err := os.CreateTemp(directory, pattern)
		if err != nil {
			return nil, err
		}
		return &expandingScratch{scratchFile: file, extra: 128}, nil
	}

	attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), 64)

	require.ErrorIs(t, err, ErrLimitExceeded)
	require.Zero(t, attrs)
	require.False(t, created)
	require.Empty(t, objects.writeConditions)
	require.Empty(t, objects.objects)
	requireScratchEmpty(t, scratch)
}

func TestGCSEnsureTreeIsIdempotentOnlyForFullyVerifiedExistingObject(t *testing.T) {
	t.Parallel()
	content := []byte("immutable bytes")
	digest := digestFor(content)
	t.Run("identical concurrent ensure", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), int64(len(content)))
		require.NoError(t, err)
		require.False(t, created)
		require.Equal(t, ref, attrs.Ref)
		require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
		requireScratchEmpty(t, scratch)
	})
	t.Run("same key unverifiable object", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		key, err := TreeKey(store.config.Prefix, testScope, digest)
		require.NoError(t, err)
		objects.putRaw(key, []byte("not zstd"), metadataFor(digest, int64(len(content))), int64(len("not zstd")))
		attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), int64(len(content)))
		require.ErrorIs(t, err, ErrConflict)
		require.Zero(t, attrs)
		require.False(t, created)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSConcurrentIdenticalEnsureHasOneCreator(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := bytes.Repeat([]byte("same immutable tree\n"), 64)
	digest := digestFor(content)
	type result struct {
		attrs   TreeAttributes
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), int64(len(content)))
			results <- result{attrs: attrs, created: created, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEqual(t, first.created, second.created)
	require.Equal(t, first.attrs, second.attrs)
	require.Len(t, objects.objects, 1)
	requireScratchEmpty(t, scratch)
}

func TestGCSEnsureTreeVerifiesPreconditionReportedDuringUploadWrite(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := []byte("already committed by a concurrent writer")
	digest := digestFor(content)
	ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
	objects.writeErr = &googleapi.Error{Code: http.StatusPreconditionFailed, Message: "doesNotExist failed"}
	objects.abortErr = errors.New("writer already stopped after precondition failure")

	attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), int64(len(content)))

	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, ref, attrs.Ref)
	require.Equal(t, 1, objects.abortCalls)
	require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
	requireScratchEmpty(t, scratch)
}

func TestGCSOfficialClientPinsGenerationAndConditionsDeleteQuery(t *testing.T) {
	t.Parallel()
	const generation int64 = 73
	content := []byte("official JSON client boundary")
	digest := digestFor(content)
	compressed := compressTest(t, content)
	key, err := TreeKey("deployment/blue", testScope, digest)
	require.NoError(t, err)
	ref, err := NewTreeRef(testScope, digest, generation)
	require.NoError(t, err)
	type requestRecord struct {
		method, path string
		query        url.Values
	}
	var requests []requestRecord
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, requestRecord{method: request.Method, path: request.URL.EscapedPath(), query: request.URL.Query()})
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("alt") == "json":
			writer.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"bucket": "bucket", "name": key, "generation": "73", "metageneration": "1", "size": strconv.Itoa(len(compressed)), "timeCreated": "2026-08-19T00:00:00Z", "updated": "2026-08-19T00:00:00Z", "metadata": metadataFor(digest, int64(len(content)))}))
		case request.Method == http.MethodGet && request.URL.Query().Get("alt") == "media":
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
			writer.Header().Set("X-Goog-Generation", "73")
			writer.Header().Set("X-Goog-Metageneration", "1")
			writer.Header().Set("X-Goog-Stored-Content-Length", strconv.Itoa(len(compressed)))
			_, _ = writer.Write(compressed)
		case request.Method == http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewStorageClient(context.Background(), server.URL+"/storage/v1/")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store, err := NewGCSStore(client, GCSConfig{Bucket: "bucket", Prefix: "deployment/blue", ScratchDir: t.TempDir(), ReadTimeout: time.Second, WriteTimeout: time.Second})
	require.NoError(t, err)
	reader, _, err := store.OpenTree(context.Background(), ref, int64(len(content)))
	require.NoError(t, err)
	require.Equal(t, content, readAll(t, reader))
	require.NoError(t, reader.Close())
	require.NoError(t, store.DeleteTree(context.Background(), ref))
	require.Len(t, requests, 3)
	objectPath := "/storage/v1/b/bucket/o/" + url.PathEscape(key)
	for _, request := range requests[:2] {
		require.Equal(t, objectPath, request.path)
		require.Equal(t, "73", request.query.Get("generation"))
		require.Empty(t, request.query.Get("ifGenerationMatch"))
	}
	require.Equal(t, http.MethodDelete, requests[2].method)
	require.Equal(t, objectPath, requests[2].path)
	require.Empty(t, requests[2].query.Get("generation"))
	require.Equal(t, "73", requests[2].query.Get("ifGenerationMatch"))
}

func TestGCSOpenTreePinsGenerationAndSpoolsBeforeExposure(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := bytes.Repeat([]byte("verified first\n"), 50)
	digest := digestFor(content)
	ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
	reader, attrs, err := store.OpenTree(context.Background(), ref, int64(len(content)))
	require.NoError(t, err)
	require.Equal(t, ref, attrs.Ref)
	require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
	require.Len(t, scratchEntries(t, scratch), 1)
	require.Equal(t, content, readAll(t, reader))
	require.NoError(t, reader.Close())
	requireScratchEmpty(t, scratch)
}

func TestGCSOpenTreeRejectsCorruptionBeforeReturningReader(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("bounded content"), 16)
	digest := digestFor(content)
	compressed := compressTest(t, content)
	wrongCompressed := compressTest(t, []byte("different bytes"))
	tests := []struct {
		name        string
		data        []byte
		metadata    map[string]string
		size, limit int64
	}{
		{name: "malformed zstd", data: []byte("not-zstd"), metadata: metadataFor(digest, int64(len(content))), size: 8, limit: int64(len(content))},
		{name: "truncated zstd", data: compressed[:len(compressed)-1], metadata: metadataFor(digest, int64(len(content))), size: int64(len(compressed) - 1), limit: int64(len(content))},
		{name: "wrong digest", data: wrongCompressed, metadata: metadataFor(digest, int64(len("different bytes"))), size: int64(len(wrongCompressed)), limit: int64(len(content))},
		{name: "wrong declared size", data: compressed, metadata: metadataFor(digest, int64(len(content))+1), size: int64(len(compressed)), limit: int64(len(content)) + 1},
		{name: "compressed size mismatch", data: compressed, metadata: metadataFor(digest, int64(len(content))), size: int64(len(compressed)) + 1, limit: int64(len(content))},
		{name: "extra metadata", data: compressed, metadata: func() map[string]string { m := metadataFor(digest, int64(len(content))); m["extra"] = "bad"; return m }(), size: int64(len(compressed)), limit: int64(len(content))},
		{name: "compressed representation limit", data: bytes.Repeat([]byte{0}, 128), metadata: metadataFor(digest, 1), size: 128, limit: 64},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, objects, scratch := newTestGCSStore(t)
			key, err := TreeKey(store.config.Prefix, testScope, digest)
			require.NoError(t, err)
			generation := objects.putRaw(key, test.data, test.metadata, test.size)
			ref, err := NewTreeRef(testScope, digest, generation)
			require.NoError(t, err)
			reader, _, err := store.OpenTree(context.Background(), ref, test.limit)
			require.ErrorIs(t, err, ErrCorrupt)
			require.Nil(t, reader)
			requireScratchEmpty(t, scratch)
		})
	}
	_, err := maxCompressedRepresentation((math.MaxInt64-32)/4 + 1)
	require.Error(t, err)
}

func TestGCSOpenTreeSeparatesBackendAndScratchFailuresFromCorruption(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("transport provenance must survive\n"), 4096)
	digest := digestFor(content)
	sentinel := errors.New("GCS response body failed")

	t.Run("reader open", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		objects.newReaderErr = sentinel
		reader, _, err := store.OpenTree(context.Background(), ref, int64(len(content)))
		require.ErrorIs(t, err, ErrInfrastructure)
		require.ErrorIs(t, err, sentinel)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("decoder initialization", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		objects.readErr = sentinel
		reader, _, err := store.OpenTree(context.Background(), ref, int64(len(content)))
		require.ErrorIs(t, err, ErrInfrastructure)
		require.ErrorIs(t, err, sentinel)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("midstream body read", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		key, err := TreeKey(store.config.Prefix, testScope, digest)
		require.NoError(t, err)
		objects.readErr = sentinel
		objects.readErrAfter = len(objects.objects[key].data) / 2
		reader, _, err := store.OpenTree(context.Background(), ref, int64(len(content)))
		require.ErrorIs(t, err, ErrInfrastructure)
		require.ErrorIs(t, err, sentinel)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("local scratch write", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		store.createScratch = func(directory, pattern string) (scratchFile, error) {
			file, err := os.CreateTemp(directory, pattern)
			if err != nil {
				return nil, err
			}
			return &failingScratch{scratchFile: file, err: syscall.ENOSPC}, nil
		}
		reader, _, err := store.OpenTree(context.Background(), ref, int64(len(content)))
		require.ErrorIs(t, err, syscall.ENOSPC)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSOpenTreeCapsCompressedBodyBeforeDecoder(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := []byte("x")
	digest := digestFor(content)
	compressed := compressTest(t, content)
	skippable := make([]byte, 8+128)
	binary.LittleEndian.PutUint32(skippable[:4], 0x184d2a50)
	binary.LittleEndian.PutUint32(skippable[4:8], 128)
	data := append(compressed, skippable...)
	key, err := TreeKey(store.config.Prefix, testScope, digest)
	require.NoError(t, err)
	generation := objects.putRaw(key, data, metadataFor(digest, 1), int64(len(compressed)))
	ref, err := NewTreeRef(testScope, digest, generation)
	require.NoError(t, err)

	reader, _, err := store.OpenTree(context.Background(), ref, 64)

	require.ErrorIs(t, err, ErrCorrupt)
	require.Nil(t, reader)
	require.LessOrEqual(t, objects.readBytes, int64(37))
	requireScratchEmpty(t, scratch)
}

func TestGCSCallerLogicalLimitViolationsAreTypedAndDeclaredSizeBoundsSpooling(t *testing.T) {
	t.Parallel()

	t.Run("declared size exceeds caller maximum", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		content := []byte("12345")
		digest := digestFor(content)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		reader, _, err := store.OpenTree(context.Background(), ref, 4)
		require.ErrorIs(t, err, ErrLimitExceeded)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("downloaded logical bytes exceed caller maximum", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		content := []byte("1234")
		digest := digestFor(content)
		compressed := compressTest(t, content)
		key, err := TreeKey(store.config.Prefix, testScope, digest)
		require.NoError(t, err)
		generation := objects.putRaw(key, compressed, metadataFor(digest, 3), int64(len(compressed)))
		ref, err := NewTreeRef(testScope, digest, generation)
		require.NoError(t, err)
		reader, _, err := store.OpenTree(context.Background(), ref, 3)
		require.ErrorIs(t, err, ErrLimitExceeded)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("metadata body mismatch below caller maximum remains corrupt", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		content := []byte("1234")
		digest := digestFor(content)
		compressed := compressTest(t, content)
		key, err := TreeKey(store.config.Prefix, testScope, digest)
		require.NoError(t, err)
		generation := objects.putRaw(key, compressed, metadataFor(digest, 3), int64(len(compressed)))
		ref, err := NewTreeRef(testScope, digest, generation)
		require.NoError(t, err)
		reader, _, err := store.OpenTree(context.Background(), ref, 10)
		require.ErrorIs(t, err, ErrCorrupt)
		require.NotErrorIs(t, err, ErrLimitExceeded)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})

	t.Run("declared size plus one caps scratch writes", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		content := bytes.Repeat([]byte("x"), 100)
		digest := digestFor(content)
		compressed := compressTest(t, content)
		key, err := TreeKey(store.config.Prefix, testScope, digest)
		require.NoError(t, err)
		generation := objects.putRaw(key, compressed, metadataFor(digest, 3), int64(len(compressed)))
		ref, err := NewTreeRef(testScope, digest, generation)
		require.NoError(t, err)
		var observed *countingScratch
		store.createScratch = func(directory, pattern string) (scratchFile, error) {
			file, err := os.CreateTemp(directory, pattern)
			if err != nil {
				return nil, err
			}
			observed = &countingScratch{scratchFile: file}
			return observed, nil
		}
		reader, _, err := store.OpenTree(context.Background(), ref, 100)
		require.ErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		require.NotNil(t, observed)
		require.LessOrEqual(t, observed.written, int64(4))
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSEnsureTreeKeepsConflictVerificationTransportFailureAsInfrastructure(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	content := []byte("existing immutable object")
	digest := digestFor(content)
	putLogical(t, objects, store.config.Prefix, testScope, digest, content)
	sentinel := errors.New("GCS read outage")
	objects.readErr = sentinel

	attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), int64(len(content)))

	require.ErrorIs(t, err, ErrInfrastructure)
	require.ErrorIs(t, err, sentinel)
	require.NotErrorIs(t, err, ErrConflict)
	require.Zero(t, attrs)
	require.False(t, created)
	requireScratchEmpty(t, scratch)
}

func TestGCSMissingTreeIsTypedNotFound(t *testing.T) {
	t.Parallel()
	store, _, scratch := newTestGCSStore(t)
	digest := digestFor([]byte("missing"))
	_, err := store.InspectTree(context.Background(), testScope, digest, 64)
	require.ErrorIs(t, err, ErrNotFound)
	ref, err := NewTreeRef(testScope, digest, 99)
	require.NoError(t, err)
	reader, _, err := store.OpenTree(context.Background(), ref, 64)
	require.ErrorIs(t, err, ErrNotFound)
	require.Nil(t, reader)
	requireScratchEmpty(t, scratch)
}

func TestGCSInspectTreeRechecksGenerationAndMetageneration(t *testing.T) {
	t.Parallel()
	content := []byte("stable bytes")
	digest := digestFor(content)
	tests := []struct {
		name   string
		mutate func(*memoryObjectClient, string, []byte, map[string]string)
	}{
		{name: "generation replacement", mutate: func(objects *memoryObjectClient, key string, data []byte, metadata map[string]string) {
			objects.retainVersions = true
			objects.afterRead = func(string) { objects.replace(key, data, metadata) }
		}},
		{name: "metageneration replacement", mutate: func(objects *memoryObjectClient, key string, _ []byte, metadata map[string]string) {
			objects.afterRead = func(string) { objects.updateMetadata(key, metadata) }
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, objects, scratch := newTestGCSStore(t)
			ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
			key, err := TreeKey(store.config.Prefix, testScope, digest)
			require.NoError(t, err)
			compressed := objects.objects[key].data
			test.mutate(objects, key, compressed, metadataFor(digest, int64(len(content))))
			_, err = store.InspectTree(context.Background(), testScope, digest, int64(len(content)))
			require.ErrorIs(t, err, ErrConflict)
			require.Equal(t, []int64{ref.Generation}, objects.readGenerations)
			requireScratchEmpty(t, scratch)
		})
	}
}

func TestGCSDeleteTreeUsesGenerationCondition(t *testing.T) {
	t.Parallel()
	content := []byte("delete")
	digest := digestFor(content)
	store, objects, _ := newTestGCSStore(t)
	ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
	require.NoError(t, store.DeleteTree(context.Background(), ref))
	require.Equal(t, []storage.Conditions{{GenerationMatch: ref.Generation}}, objects.deleteConditions)
	require.NoError(t, store.DeleteTree(context.Background(), ref))
	ref = putLogical(t, objects, store.config.Prefix, testScope, digest, content)
	ref.Generation++
	require.ErrorIs(t, store.DeleteTree(context.Background(), ref), ErrConflict)
}

func TestGCSMapsBackendErrorsToInfrastructureWithoutHidingCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("transport broke")
	store, objects, scratch := newTestGCSStore(t)
	objects.attrsErr = sentinel
	_, err := store.InspectTree(context.Background(), testScope, digestFor([]byte("missing")), 64)
	require.ErrorIs(t, err, ErrInfrastructure)
	require.ErrorIs(t, err, sentinel)
	requireScratchEmpty(t, scratch)
	objects.attrsErr = nil
	ref := putLogical(t, objects, store.config.Prefix, testScope, digestFor([]byte("x")), []byte("x"))
	objects.deleteErr = sentinel
	err = store.DeleteTree(context.Background(), ref)
	require.ErrorIs(t, err, ErrInfrastructure)
	require.ErrorIs(t, err, sentinel)
}

func TestGCSClassifiesUnauthorizedAcrossBackendOperations(t *testing.T) {
	t.Parallel()
	authorizationErrors := []struct {
		name string
		err  error
	}{
		{name: "HTTP 401", err: &googleapi.Error{Code: http.StatusUnauthorized, Message: "unauthenticated"}},
		{name: "HTTP 403", err: &googleapi.Error{Code: http.StatusForbidden, Message: "forbidden"}},
		{name: "gRPC Unauthenticated", err: status.Error(codes.Unauthenticated, "unauthenticated")},
		{name: "gRPC PermissionDenied", err: status.Error(codes.PermissionDenied, "forbidden")},
	}
	operations := []struct {
		name string
		run  func(*testing.T, *GCSStore, *memoryObjectClient, error) error
	}{
		{name: "attributes", run: func(t *testing.T, store *GCSStore, objects *memoryObjectClient, backendErr error) error {
			objects.attrsErr = backendErr
			_, err := store.InspectTree(context.Background(), testScope, digestFor([]byte("missing")), 64)
			return err
		}},
		{name: "reader open", run: func(t *testing.T, store *GCSStore, objects *memoryObjectClient, backendErr error) error {
			content := []byte("read")
			ref := putLogical(t, objects, store.config.Prefix, testScope, digestFor(content), content)
			objects.newReaderErr = backendErr
			_, _, err := store.OpenTree(context.Background(), ref, 64)
			return err
		}},
		{name: "reader body", run: func(t *testing.T, store *GCSStore, objects *memoryObjectClient, backendErr error) error {
			content := []byte("read body")
			ref := putLogical(t, objects, store.config.Prefix, testScope, digestFor(content), content)
			objects.readErr = backendErr
			_, _, err := store.OpenTree(context.Background(), ref, 64)
			return err
		}},
		{name: "upload write", run: func(t *testing.T, store *GCSStore, objects *memoryObjectClient, backendErr error) error {
			content := []byte("upload")
			objects.writeErr = backendErr
			_, _, err := store.EnsureTree(context.Background(), testScope, digestFor(content), bytes.NewReader(content), 64)
			return err
		}},
		{name: "upload commit", run: func(t *testing.T, store *GCSStore, objects *memoryObjectClient, backendErr error) error {
			content := []byte("commit")
			objects.closeErr = backendErr
			_, _, err := store.EnsureTree(context.Background(), testScope, digestFor(content), bytes.NewReader(content), 64)
			return err
		}},
		{name: "delete", run: func(t *testing.T, store *GCSStore, objects *memoryObjectClient, backendErr error) error {
			content := []byte("delete")
			ref := putLogical(t, objects, store.config.Prefix, testScope, digestFor(content), content)
			objects.deleteErr = backendErr
			return store.DeleteTree(context.Background(), ref)
		}},
	}
	for _, authorizationError := range authorizationErrors {
		authorizationError := authorizationError
		for _, operation := range operations {
			operation := operation
			t.Run(authorizationError.name+"/"+operation.name, func(t *testing.T) {
				t.Parallel()
				store, objects, scratch := newTestGCSStore(t)
				err := operation.run(t, store, objects, authorizationError.err)
				require.ErrorIs(t, err, ErrUnauthorized)
				require.ErrorIs(t, err, authorizationError.err)
				require.NotErrorIs(t, err, ErrInfrastructure)
				requireScratchEmpty(t, scratch)
			})
		}
	}
}

func TestGCSDoesNotReclassifyContextErrorsAsInfrastructure(t *testing.T) {
	t.Parallel()
	store, objects, scratch := newTestGCSStore(t)
	objects.attrsErr = context.Canceled
	_, err := store.InspectTree(context.Background(), testScope, digestFor([]byte("missing")), 64)
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrInfrastructure)
	requireScratchEmpty(t, scratch)
}

func TestGCSCancellationAndInterruptedWritesLeaveNoVisibleObjectOrScratch(t *testing.T) {
	t.Parallel()
	content := []byte("cancel me")
	digest := digestFor(content)
	t.Run("blocked source", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, time.Second, 20*time.Millisecond)
		source := newBlockingGCSReadCloser()
		result := make(chan error, 1)
		go func() {
			_, _, err := store.EnsureTree(context.Background(), testScope, digest, source, 64)
			result <- err
		}()
		select {
		case <-source.started:
		case <-time.After(time.Second):
			t.Fatal("source read did not start")
		}
		require.ErrorIs(t, <-result, context.DeadlineExceeded)
		require.Equal(t, 1, source.closeCalls())
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})
	t.Run("interrupted upload", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		objects.writeErr = io.ErrUnexpectedEOF
		attrs, created, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, ErrInfrastructure)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.Zero(t, attrs)
		require.False(t, created)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})
	t.Run("write timeout remains a context error", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, time.Second, 20*time.Millisecond)
		objects.blockWrite = true
		_, _, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotErrorIs(t, err, ErrInfrastructure)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})
	t.Run("read body timeout remains a context error", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStoreWithTimeouts(t, 20*time.Millisecond, time.Second)
		ref := putLogical(t, objects, store.config.Prefix, testScope, digest, content)
		objects.blockReadBody = true
		reader, _, err := store.OpenTree(context.Background(), ref, 64)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		requireScratchEmpty(t, scratch)
	})
	t.Run("scratch ENOSPC", func(t *testing.T) {
		t.Parallel()
		store, objects, scratch := newTestGCSStore(t)
		store.createScratch = func(directory, pattern string) (scratchFile, error) {
			file, err := os.CreateTemp(directory, pattern)
			if err != nil {
				return nil, err
			}
			return &failingScratch{scratchFile: file, err: syscall.ENOSPC}, nil
		}
		_, _, err := store.EnsureTree(context.Background(), testScope, digest, bytes.NewReader(content), 64)
		require.ErrorIs(t, err, syscall.ENOSPC)
		require.Empty(t, objects.objects)
		requireScratchEmpty(t, scratch)
	})
}

func TestGCSRecognizesTypedConditionalErrors(t *testing.T) {
	t.Parallel()
	store, objects, _ := newTestGCSStore(t)
	content := []byte("typed")
	ref := putLogical(t, objects, store.config.Prefix, testScope, digestFor(content), content)
	objects.deleteErr = status.Error(codes.FailedPrecondition, "generation mismatch")
	require.ErrorIs(t, store.DeleteTree(context.Background(), ref), ErrConflict)
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
	store, err := newGCSStore(objects, GCSConfig{Bucket: "bucket", Prefix: "deployment/blue", ScratchDir: scratch, ZstdLevel: zstd.SpeedFastest, ReadTimeout: readTimeout, WriteTimeout: writeTimeout})
	require.NoError(t, err)
	return store, objects, scratch
}
func digestFor(content []byte) Digest {
	sum := sha256.Sum256(content)
	return Digest(fmt.Sprintf("sha256:%x", sum))
}
func metadataFor(digest Digest, size int64) map[string]string {
	return map[string]string{"concourse-uncompressed-sha256": string(digest), "concourse-uncompressed-bytes": strconv.FormatInt(size, 10), "concourse-representation": "zstd"}
}
func putLogical(t *testing.T, objects *memoryObjectClient, prefix string, scope Scope, digest Digest, content []byte) TreeRef {
	t.Helper()
	key, err := TreeKey(prefix, scope, digest)
	require.NoError(t, err)
	compressed := compressTest(t, content)
	generation := objects.putRaw(key, compressed, metadataFor(digest, int64(len(content))), int64(len(compressed)))
	ref, err := NewTreeRef(scope, digest, generation)
	require.NoError(t, err)
	return ref
}
func compressTest(t *testing.T, content []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder, err := zstd.NewWriter(&output)
	require.NoError(t, err)
	_, err = encoder.Write(content)
	require.NoError(t, err)
	require.NoError(t, encoder.Close())
	return output.Bytes()
}
func decompressTest(t *testing.T, content []byte) []byte {
	t.Helper()
	decoder, err := zstd.NewReader(bytes.NewReader(content))
	require.NoError(t, err)
	defer decoder.Close()
	return readAll(t, decoder)
}
func readAll(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	return content
}
func scratchEntries(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	return entries
}
func requireScratchEmpty(t *testing.T, directory string) {
	t.Helper()
	require.Empty(t, scratchEntries(t, directory))
}
