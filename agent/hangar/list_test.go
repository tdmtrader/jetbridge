package hangar

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const listTestDigestA = Digest("sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111")
const listTestDigestB = Digest("sha256:" +
	"2222222222222222222222222222222222222222222222222222222222222222")

func TestParseKeyRoundTripsCanonicalKeys(t *testing.T) {
	for _, kind := range []Kind{KindSnapshot, KindCheckpoint} {
		key, err := Key(kind, listTestDigestA)
		require.NoError(t, err)
		parsedKind, parsedDigest, err := ParseKey(key)
		require.NoError(t, err)
		require.Equal(t, kind, parsedKind)
		require.Equal(t, listTestDigestA, parsedDigest)
	}
}

// ParseKey gates deletion, so every shape it does not fully recognize has to
// be an error rather than a best-effort guess.
func TestParseKeyRejectsNonCanonicalKeys(t *testing.T) {
	canonical, err := Key(KindSnapshot, listTestDigestA)
	require.NoError(t, err)

	for name, key := range map[string]string{
		"empty":             "",
		"wrong prefix":      strings.Replace(canonical, "hangar/v1/", "hangar/v2/", 1),
		"unknown kind":      "hangar/v1/blobs/sha256/" + strings.TrimPrefix(string(listTestDigestA), "sha256:") + ".tar.zst",
		"wrong suffix":      strings.TrimSuffix(canonical, ".tar.zst") + ".tar",
		"no suffix":         strings.TrimSuffix(canonical, ".tar.zst"),
		"short digest":      "hangar/v1/snapshots/sha256/abc.tar.zst",
		"uppercase digest":  strings.ToUpper(canonical),
		"non hex digest":    "hangar/v1/snapshots/sha256/" + strings.Repeat("z", 64) + ".tar.zst",
		"nested extra path": "hangar/v1/snapshots/sha256/nested/" + strings.TrimPrefix(string(listTestDigestA), "sha256:") + ".tar.zst",
		"leading dot scratch": "hangar/v1/snapshots/sha256/." +
			strings.TrimPrefix(string(listTestDigestA), "sha256:") + ".tar.zst6443335912594057790",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseKey(key)
			require.Error(t, err, "ParseKey accepted a non-canonical key")
		})
	}
}

func TestGCSStoreListReportsCanonicalObjects(t *testing.T) {
	store, client, _ := newTestGCSStore(t)
	refA := client.put(t, KindSnapshot, listTestDigestA, []byte("alpha"))
	refB := client.put(t, KindSnapshot, listTestDigestB, []byte("beta"))

	var listed []Attributes
	require.NoError(t, store.List(context.Background(), KindSnapshot, func(attributes Attributes) error {
		listed = append(listed, attributes)
		return nil
	}))

	require.Len(t, listed, 2)
	require.Equal(t, refA, listed[0].Ref)
	require.Equal(t, refB, listed[1].Ref)
	for _, attributes := range listed {
		require.Positive(t, attributes.CompressedBytes)
		require.False(t, attributes.CreatedAt.IsZero())
	}
}

// Listing is scoped by kind because the sweep that consumes it reasons about
// one namespace's metadata; a checkpoint must never appear in a snapshot sweep.
func TestGCSStoreListIsScopedToOneKind(t *testing.T) {
	store, client, _ := newTestGCSStore(t)
	client.put(t, KindSnapshot, listTestDigestA, []byte("alpha"))
	client.put(t, KindCheckpoint, listTestDigestB, []byte("beta"))

	var listed []Attributes
	require.NoError(t, store.List(context.Background(), KindSnapshot, func(attributes Attributes) error {
		listed = append(listed, attributes)
		return nil
	}))

	require.Len(t, listed, 1)
	require.Equal(t, KindSnapshot, listed[0].Ref.Kind)
	require.Equal(t, listTestDigestA, listed[0].Ref.Digest)
}

// A caller's only action on a listed object is deletion, so an unrecognized key
// must be withheld entirely rather than reported with partial identity.
func TestGCSStoreListSkipsUnrecognizedKeys(t *testing.T) {
	store, client, _ := newTestGCSStore(t)
	ref := client.put(t, KindSnapshot, listTestDigestA, []byte("alpha"))
	client.mu.Lock()
	client.objects["hangar/v1/snapshots/sha256/not-a-digest.tar.zst"] = memoryObject{
		data: []byte("junk"), generation: 99, size: 4, metaGen: 1,
	}
	client.objects["hangar/v1/snapshots/sha256/stray.txt"] = memoryObject{
		data: []byte("junk"), generation: 98, size: 4, metaGen: 1,
	}
	client.mu.Unlock()

	var listed []Attributes
	require.NoError(t, store.List(context.Background(), KindSnapshot, func(attributes Attributes) error {
		listed = append(listed, attributes)
		return nil
	}))

	require.Len(t, listed, 1)
	require.Equal(t, ref, listed[0].Ref)
}

// A failed upload is exactly the object the sweep must be able to see, and it
// is also the object most likely to carry absent or malformed metadata.
func TestGCSStoreListReportsObjectsWithUnusableMetadata(t *testing.T) {
	store, client, _ := newTestGCSStore(t)
	ref := client.putRaw(t, KindSnapshot, listTestDigestA, []byte("truncated"), map[string]string{
		metadataUncompressedBytes: "not-a-number",
	}, 9)

	var listed []Attributes
	require.NoError(t, store.List(context.Background(), KindSnapshot, func(attributes Attributes) error {
		listed = append(listed, attributes)
		return nil
	}))

	require.Len(t, listed, 1)
	require.Equal(t, ref, listed[0].Ref)
	require.Zero(t, listed[0].UncompressedBytes)
}

func TestGCSStoreListPropagatesVisitorAndIterationErrors(t *testing.T) {
	store, client, _ := newTestGCSStore(t)
	client.put(t, KindSnapshot, listTestDigestA, []byte("alpha"))

	sentinel := errors.New("visitor stopped")
	require.ErrorIs(t, store.List(context.Background(), KindSnapshot, func(Attributes) error {
		return sentinel
	}), sentinel)

	client.listErr = errors.New("backend unavailable")
	err := store.List(context.Background(), KindSnapshot, func(Attributes) error { return nil })
	require.ErrorContains(t, err, "backend unavailable")
}

func TestGCSStoreListRejectsInvalidArguments(t *testing.T) {
	store, _, _ := newTestGCSStore(t)
	require.Error(t, store.List(context.Background(), Kind("bogus"), func(Attributes) error { return nil }))
	require.Error(t, store.List(context.Background(), KindSnapshot, nil))
}
