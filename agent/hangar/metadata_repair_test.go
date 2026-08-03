package hangar

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
)

// emptyStoredMetadata reproduces the live failure: the object store persisted
// object content to disk but held custom metadata only in memory, so a restart
// returned intact bytes with no metadata at all.
var emptyStoredMetadata = map[string]string{}

func repairFixture(t *testing.T, content []byte, metadata map[string]string) (
	*GCSStore,
	*memoryObjectClient,
	ObjectRef,
	[]byte,
) {
	t.Helper()
	store, objects, _ := newTestGCSStore(t)
	compressed := compressForTest(t, content)
	digest := testDigest(content)
	ref := objects.putRaw(t, KindSnapshot, digest, compressed, metadata, int64(len(compressed)))
	return store, objects, ref, compressed
}

func TestGCSStoreRepairRestoresLostMetadataAndMakesTheObjectReadableAgain(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("snapshot bytes survived the restart\n"), 64)
	store, objects, ref, compressed := repairFixture(t, content, emptyStoredMetadata)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	limit := int64(len(content)) * 4

	// The strict read path must reject the object before any repair runs.
	_, _, readErr := store.Open(ctx, ref, limit)
	require.ErrorIs(t, readErr, ErrCorrupt)
	require.ErrorContains(t, readErr, "object metadata vocabulary is not exact")

	attributes, err := store.RepairDerivableMetadata(ctx, KindSnapshot, ref.Digest, limit)
	require.NoError(t, err)
	require.Equal(t, ref, attributes.Ref)
	require.Equal(t, int64(len(content)), attributes.UncompressedBytes)
	require.Equal(t, int64(len(compressed)), attributes.CompressedBytes)

	repaired := objects.object(ref.Key)
	require.Equal(t, map[string]string{
		metadataRepresentation:     representationZstd,
		metadataUncompressedSHA256: string(ref.Digest),
		metadataUncompressedBytes:  strconv.Itoa(len(content)),
	}, repaired.metadata)
	// Repair restores metadata only. Bytes and generation must be untouched so
	// that references already recorded against this object stay valid.
	require.Equal(t, compressed, repaired.data)
	require.Equal(t, ref.Generation, repaired.generation)

	reader, opened, err := store.Open(ctx, ref, limit)
	require.NoError(t, err)
	require.Equal(t, content, mustReadAll(t, reader))
	require.NoError(t, reader.Close())
	require.Equal(t, int64(len(content)), opened.UncompressedBytes)

	inspected, err := store.Inspect(ctx, KindSnapshot, ref.Digest, limit)
	require.NoError(t, err)
	require.Equal(t, ref, inspected.Ref)
}

func TestGCSStoreRepairPinsTheProvedGenerationAndMetagenerationOnCommit(t *testing.T) {
	t.Parallel()

	content := []byte("generation pinned repair")
	store, objects, ref, _ := repairFixture(t, content, emptyStoredMetadata)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, ref.Digest, int64(len(content))*4)
	require.NoError(t, err)

	require.Len(t, objects.updateConditions, 1)
	require.Equal(t, ref.Generation, objects.updateConditions[0].GenerationMatch)
	require.Equal(t, int64(1), objects.updateConditions[0].MetagenerationMatch)
}

func TestGCSStoreRepairReportsContentThatDoesNotProveTheKeyDigestAndWritesNothing(t *testing.T) {
	t.Parallel()

	stored := []byte("bytes that are not what the key claims")
	claimed := []byte("the content the key digest names")
	store, objects, _ := newTestGCSStore(t)
	compressed := compressForTest(t, stored)
	claimedDigest := testDigest(claimed)
	ref := objects.putRaw(
		t,
		KindSnapshot,
		claimedDigest,
		compressed,
		emptyStoredMetadata,
		int64(len(compressed)),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, claimedDigest, 4096)
	require.ErrorIs(t, err, ErrCorrupt)
	require.ErrorContains(t, err, "does not match key digest")
	require.NotErrorIs(t, err, ErrUnrepairable)

	// A genuinely corrupt object is reported, never rewritten and never deleted.
	require.Empty(t, objects.updates)
	require.Empty(t, objects.updateConditions)
	require.Empty(t, objects.deleteConditions)
	untouched := objects.object(ref.Key)
	require.Equal(t, compressed, untouched.data)
	require.Empty(t, untouched.metadata)
	require.Equal(t, int64(1), untouched.metaGen)
}

func TestGCSStoreRepairReportsTruncatedContentAndWritesNothing(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("truncated durable object\n"), 128)
	store, objects, _ := newTestGCSStore(t)
	compressed := compressForTest(t, content)
	digest := testDigest(content)
	truncated := compressed[:len(compressed)-4]
	ref := objects.putRaw(
		t,
		KindSnapshot,
		digest,
		truncated,
		emptyStoredMetadata,
		int64(len(truncated)),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, digest, int64(len(content))*4)
	require.ErrorIs(t, err, ErrCorrupt)
	require.Empty(t, objects.updates)
	require.Equal(t, truncated, objects.object(ref.Key).data)
}

func TestGCSStoreRepairReportsAnObjectWhoseDeclaredSizeDisagreesWithItsBytes(t *testing.T) {
	t.Parallel()

	content := []byte("declared size disagrees with the stream")
	store, objects, _ := newTestGCSStore(t)
	compressed := compressForTest(t, content)
	digest := testDigest(content)
	// The object streams its real bytes but reports a longer size. Proving must
	// weigh what it actually read, not what the store claims it holds.
	ref := objects.putRaw(
		t,
		KindSnapshot,
		digest,
		compressed,
		emptyStoredMetadata,
		int64(len(compressed))+1,
	)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, digest, int64(len(content))*4)
	require.ErrorIs(t, err, ErrCorrupt)
	require.ErrorContains(t, err, "compressed byte count")
	require.Empty(t, objects.updates)
	require.Empty(t, objects.object(ref.Key).metadata)
}

func TestGCSStoreRepairLeavesAHealthyObjectUnreadAndUnwritten(t *testing.T) {
	t.Parallel()

	content := []byte("already valid metadata")
	store, objects, ref, compressed := repairFixture(
		t,
		content,
		testMetadata(testDigest(content), int64(len(content)), representationZstd),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	attributes, err := store.RepairDerivableMetadata(ctx, KindSnapshot, ref.Digest, 4096)
	require.NoError(t, err)
	require.Equal(t, ref, attributes.Ref)
	require.Equal(t, int64(len(content)), attributes.UncompressedBytes)
	require.Equal(t, int64(len(compressed)), attributes.CompressedBytes)

	// Proving an object costs a full download and decompression. An object that
	// passes the vocabulary check must never pay it, or every scheduled pass
	// would decompress the entire bucket.
	require.Empty(t, objects.readGenerations)
	require.Empty(t, objects.updates)
	require.Equal(t, int64(1), objects.object(ref.Key).metaGen)
}

func TestGCSStoreRepairRefusesMetadataOutsideTheStoredVocabulary(t *testing.T) {
	t.Parallel()

	content := []byte("object carrying an unknown key")
	store, objects, ref, _ := repairFixture(t, content, map[string]string{
		metadataRepresentation: representationZstd,
		"concourse-future-key": "value nobody here can derive",
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, ref.Digest, 4096)
	require.ErrorIs(t, err, ErrUnrepairable)
	require.ErrorContains(t, err, "concourse-future-key")
	require.Empty(t, objects.readGenerations)
	require.Empty(t, objects.updates)
}

func TestCheckMetadataIsDerivableRefusesAVocabularyKeyWithoutADerivation(t *testing.T) {
	t.Parallel()

	err := checkMetadataIsDerivable(
		append(append([]string(nil), storedMetadataVocabulary...), "concourse-signed-by"),
		metadataDerivations,
		emptyStoredMetadata,
	)
	require.ErrorIs(t, err, ErrUnrepairable)
	require.ErrorContains(t, err, "concourse-signed-by")
	require.ErrorContains(t, err, "cannot be derived")
}

func TestStoredMetadataVocabularyIsExactlyTheDerivableSet(t *testing.T) {
	t.Parallel()

	require.Len(t, metadataDerivations, len(storedMetadataVocabulary))
	for _, name := range storedMetadataVocabulary {
		require.Contains(t, metadataDerivations, name)
	}
	// validateStoredMetadata's exactness check and the repair vocabulary must
	// stay the same list, or repair could write metadata reads reject.
	require.Equal(t, 3, len(storedMetadataVocabulary))
}

func TestGCSStoreRepairMapsAMissingObjectToNotFound(t *testing.T) {
	t.Parallel()

	store, objects, _ := newTestGCSStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, testDigest([]byte("absent")), 4096)
	require.ErrorIs(t, err, ErrNotFound)
	require.Empty(t, objects.updates)
}

func TestGCSStoreRepairReportsAConflictWhenTheObjectChangesBeforeCommit(t *testing.T) {
	t.Parallel()

	content := []byte("raced repair")
	store, objects, ref, _ := repairFixture(t, content, emptyStoredMetadata)
	objects.updateErr = &googleapi.Error{Code: 412, Message: "metageneration mismatch"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, ref.Digest, 4096)
	require.ErrorIs(t, err, ErrConflict)
	require.Empty(t, objects.object(ref.Key).metadata)
}

func TestGCSStoreRepairRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	store, _, _ := newTestGCSStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, Kind("bogus"), testDigest([]byte("x")), 4096)
	require.ErrorContains(t, err, "invalid object kind")

	_, err = store.RepairDerivableMetadata(ctx, KindSnapshot, Digest("not-a-digest"), 4096)
	require.ErrorContains(t, err, "digest must be sha256")

	_, err = store.RepairDerivableMetadata(ctx, KindSnapshot, testDigest([]byte("x")), 0)
	require.ErrorContains(t, err, "maximum uncompressed bytes must be positive")
}

func TestGCSStoreRepairRefusesContentLargerThanTheSuppliedLimit(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("oversized"), 256)
	store, objects, ref, _ := repairFixture(t, content, emptyStoredMetadata)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := store.RepairDerivableMetadata(ctx, KindSnapshot, ref.Digest, int64(len(content))-1)
	require.ErrorIs(t, err, ErrCorrupt)
	require.ErrorContains(t, err, "exceeds")
	require.Empty(t, objects.updates)
}
