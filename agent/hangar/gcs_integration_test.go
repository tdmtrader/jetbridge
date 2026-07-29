//go:build integration

package hangar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/api/iterator"
)

const (
	fakeGCSImage               = "fsouza/fake-gcs-server:1.52.3"
	externalFakeGCSEndpointEnv = "CONCOURSE_HANGAR_TEST_GCS_ENDPOINT"
)

func TestFakeGCSContainerRegistersCleanupBeforeReturningStartError(t *testing.T) {
	startErr := errors.New("partial container start")
	container := &cleanupRecordingContainer{}

	t.Run("non-nil partial container", func(t *testing.T) {
		actual, err := createFakeGCSContainer(
			t,
			t.Context(),
			testcontainers.GenericContainerRequest{},
			func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
				return container, startErr
			},
		)
		require.Same(t, container, actual)
		require.ErrorIs(t, err, startErr)
		require.False(t, container.terminated.Load())
	})
	require.True(t, container.terminated.Load())

	t.Run("nil partial container", func(t *testing.T) {
		actual, err := createFakeGCSContainer(
			t,
			t.Context(),
			testcontainers.GenericContainerRequest{},
			func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
				return nil, startErr
			},
		)
		require.Nil(t, actual)
		require.ErrorIs(t, err, startErr)
	})
}

func TestGCSStoreFakeServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	endpoint := fakeGCSEndpoint(t, ctx)
	client, err := NewStorageClient(ctx, endpoint)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	bucketName := fmt.Sprintf("hangar-it-%d-%d", os.Getpid(), time.Now().UnixNano())
	bucket := client.Bucket(bucketName)
	require.NoError(t, bucket.Create(ctx, "hangar-integration", nil))
	t.Cleanup(func() {
		cleanupFakeGCSBucket(t, client, bucketName)
	})

	initialScratch := t.TempDir()
	store, err := NewGCSStore(client, GCSConfig{
		Bucket:       bucketName,
		ScratchDir:   initialScratch,
		ZstdLevel:    zstd.SpeedDefault,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	})
	require.NoError(t, err)

	t.Run("immutable create conflict verifies and opens pinned generation", func(t *testing.T) {
		content := bytes.Repeat([]byte("verified emulator bytes\n"), 1024)
		digest := integrationDigest(content)

		created, err := store.Ensure(
			ctx,
			KindSnapshot,
			digest,
			bytes.NewReader(content),
			int64(len(content)),
		)
		require.NoError(t, err)
		require.Positive(t, created.Ref.Generation)

		existing, err := store.Ensure(
			ctx,
			KindSnapshot,
			digest,
			bytes.NewReader(content),
			int64(len(content)),
		)
		require.NoError(t, err)
		require.Equal(t, created, existing)

		reader, opened, err := store.Open(ctx, created.Ref, int64(len(content)))
		require.NoError(t, err)
		actual, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		require.Equal(t, created, opened)
		require.Equal(t, content, actual)

		require.NoError(t, store.Delete(ctx, created.Ref))
		require.NoError(t, store.Delete(ctx, created.Ref))
	})

	t.Run("different object at immutable key is a conflict", func(t *testing.T) {
		content := []byte("expected immutable bytes")
		digest := integrationDigest(content)
		key, err := Key(KindSnapshot, digest)
		require.NoError(t, err)

		stored := writeFakeGCSObject(
			t,
			ctx,
			bucket,
			key,
			[]byte("not a zstd representation"),
			map[string]string{
				metadataUncompressedSHA256: string(digest),
				metadataUncompressedBytes:  fmt.Sprintf("%d", len(content)),
				metadataRepresentation:     representationZstd,
			},
		)

		_, err = store.Ensure(
			ctx,
			KindSnapshot,
			digest,
			bytes.NewReader(content),
			int64(len(content)),
		)
		require.ErrorIs(t, err, ErrConflict)

		ref, err := NewObjectRef(KindSnapshot, digest, stored.Generation)
		require.NoError(t, err)
		require.NoError(t, store.Delete(ctx, ref))
	})

	t.Run("concurrent writers converge on one immutable verified generation", func(t *testing.T) {
		content := bytes.Repeat([]byte("concurrent durable bytes\n"), 256)
		digest := integrationDigest(content)
		results := make(chan Attributes, 8)
		errors := make(chan error, 8)
		var writers sync.WaitGroup
		for range cap(results) {
			writers.Add(1)
			go func() {
				defer writers.Done()
				attrs, err := store.Ensure(ctx, KindCheckpoint, digest, bytes.NewReader(content), int64(len(content)))
				if err != nil {
					errors <- err
					return
				}
				results <- attrs
			}()
		}
		writers.Wait()
		close(results)
		close(errors)
		for err := range errors {
			require.NoError(t, err)
		}
		var first Attributes
		for attrs := range results {
			if first.Ref.Generation == 0 {
				first = attrs
			}
			require.Equal(t, first, attrs)
		}
		require.Positive(t, first.Ref.Generation)
		require.NoError(t, store.Delete(ctx, first.Ref))
	})

	t.Run("truncated representations are rejected before recovery", func(t *testing.T) {
		content := []byte("durability requires complete zstd frames")
		digest := integrationDigest(content)
		key, err := Key(KindSnapshot, digest)
		require.NoError(t, err)
		compressed := integrationCompressed(t, content)
		stored := writeFakeGCSObject(t, ctx, bucket, key, compressed[:len(compressed)-1], map[string]string{
			metadataUncompressedSHA256: string(digest),
			metadataUncompressedBytes:  fmt.Sprintf("%d", len(content)),
			metadataRepresentation:     representationZstd,
		})
		ref, err := NewObjectRef(KindSnapshot, digest, stored.Generation)
		require.NoError(t, err)
		reader, _, err := store.Open(ctx, ref, int64(len(content)))
		require.ErrorIs(t, err, ErrCorrupt)
		require.Nil(t, reader)
		require.NoError(t, store.Delete(ctx, ref))
	})

	t.Run("recovery reopens durable content after complete local scratch loss", func(t *testing.T) {
		content := bytes.Repeat([]byte("durable-only recovery\n"), 512)
		digest := integrationDigest(content)
		attrs, err := store.Ensure(ctx, KindCheckpoint, digest, bytes.NewReader(content), int64(len(content)))
		require.NoError(t, err)

		require.NoError(t, os.RemoveAll(initialScratch))
		require.NoDirExists(t, initialScratch)
		// A new store after complete node-local state loss can recover only by
		// reading and verifying the production GCS adapter.
		recoveredScratch := t.TempDir()
		recovered, err := NewGCSStore(client, GCSConfig{
			Bucket: bucketName, ScratchDir: recoveredScratch, ZstdLevel: zstd.SpeedDefault,
			ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		})
		require.NoError(t, err)
		reader, reopened, err := recovered.Open(ctx, attrs.Ref, int64(len(content)))
		require.NoError(t, err)
		require.Equal(t, attrs, reopened)
		actual, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		require.Equal(t, content, actual)
		require.NoError(t, recovered.Delete(ctx, attrs.Ref))
	})
}

func integrationCompressed(t *testing.T, content []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	require.NoError(t, err)
	_, err = encoder.Write(content)
	require.NoError(t, err)
	require.NoError(t, encoder.Close())
	return compressed.Bytes()
}

type cleanupRecordingContainer struct {
	testcontainers.Container
	terminated atomic.Bool
}

type genericContainerCreator func(
	context.Context,
	testcontainers.GenericContainerRequest,
) (testcontainers.Container, error)

func createFakeGCSContainer(
	t *testing.T,
	ctx context.Context,
	request testcontainers.GenericContainerRequest,
	create genericContainerCreator,
) (testcontainers.Container, error) {
	t.Helper()
	container, err := create(ctx, request)
	testcontainers.CleanupContainer(t, container)
	return container, err
}

func (container *cleanupRecordingContainer) Terminate(
	context.Context,
	...testcontainers.TerminateOption,
) error {
	container.terminated.Store(true)
	return nil
}

func fakeGCSEndpoint(t *testing.T, ctx context.Context) string {
	t.Helper()

	if endpoint, configured := os.LookupEnv(externalFakeGCSEndpointEnv); configured {
		require.NotEmpty(t, strings.TrimSpace(endpoint))
		parsed, err := url.Parse(endpoint)
		require.NoError(t, err)
		require.Contains(t, []string{"http", "https"}, parsed.Scheme)
		require.NotEmpty(t, parsed.Host)
		t.Logf("using explicitly configured fake GCS endpoint from %s", externalFakeGCSEndpointEnv)
		return endpoint
	}

	req := testcontainers.ContainerRequest{
		Image:        fakeGCSImage,
		ExposedPorts: []string{"4443/tcp"},
		Cmd:          []string{"-scheme", "http", "-port", "4443", "-backend", "memory"},
		WaitingFor:   wait.ForHTTP("/storage/v1/b").WithPort("4443/tcp"),
	}
	container, err := createFakeGCSContainer(
		t,
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		},
		testcontainers.GenericContainer,
	)
	require.NoError(t, err)

	endpoint, err := container.PortEndpoint(ctx, "4443/tcp", "http")
	require.NoError(t, err)
	return strings.TrimRight(endpoint, "/") + "/storage/v1/"
}

func integrationDigest(content []byte) Digest {
	sum := sha256.Sum256(content)
	return Digest(fmt.Sprintf("sha256:%x", sum))
}

func writeFakeGCSObject(
	t *testing.T,
	ctx context.Context,
	bucket *storage.BucketHandle,
	key string,
	content []byte,
	metadata map[string]string,
) *storage.ObjectAttrs {
	t.Helper()

	writer := bucket.Object(key).NewWriter(ctx)
	writer.Metadata = metadata
	_, err := writer.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	attrs := writer.Attrs()
	require.NotNil(t, attrs)
	require.Positive(t, attrs.Generation)
	return attrs
}

func cleanupFakeGCSBucket(t *testing.T, client *storage.Client, bucketName string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bucket := client.Bucket(bucketName)
	objects := bucket.Objects(ctx, nil)
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Errorf("list fake GCS cleanup objects: %v", err)
			return
		}
		if err := bucket.Object(attrs.Name).Delete(ctx); err != nil &&
			!errors.Is(err, storage.ErrObjectNotExist) {
			t.Errorf("delete fake GCS cleanup object %q: %v", attrs.Name, err)
		}
	}
	if err := bucket.Delete(ctx); err != nil {
		t.Errorf("delete fake GCS cleanup bucket %q: %v", bucketName, err)
	}
}
