package publisher_test

// The publisher's own refusals: the ones decided from its arguments and its
// derived namespace before any RPC is issued. The conformance suite beside
// this package proves what the role does WITH a store; this file proves what
// it refuses to do without one, and the recorder is what makes "without one"
// a fact rather than a reading of the code.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
	testsupport "github.com/concourse/concourse/hangar/output/testsupport"
)

const (
	bucket      = "publisher-spec"
	epoch       = executioncontrol.ActivationEpoch(7)
	reservation = output.ReservationID("44444444-4444-4444-8444-444444444444")
	timeout     = 10 * time.Second
)

func namespaceFor(t *testing.T, tenant string) output.OutputNamespace {
	return testsupport.Namespace(t, bucket, tenant, epoch)
}

// role builds a publisher over a recorded tier-1 store.
func role(t *testing.T, namespace output.OutputNamespace) (*publisher.Publisher, *gcstest.Recorder) {
	t.Helper()

	_, recorder := testsupport.RecordedMemory(bucket)
	built, err := publisher.New(namespace, publisher.Restrict(recorder), timeout)
	if err != nil {
		t.Fatalf("building the publisher: %v", err)
	}

	return built, recorder
}

func TestNewRefusesAnIncompleteRole(t *testing.T) {
	namespace := namespaceFor(t, "tenant-a")
	store := publisher.Restrict(gcstest.NewMemory())

	for name, build := range map[string]func() (*publisher.Publisher, error){
		"a zero namespace": func() (*publisher.Publisher, error) {
			return publisher.New(output.OutputNamespace{}, store, timeout)
		},
		"no store": func() (*publisher.Publisher, error) {
			return publisher.New(namespace, nil, timeout)
		},
		"a zero timeout": func() (*publisher.Publisher, error) {
			return publisher.New(namespace, store, 0)
		},
		"a negative timeout": func() (*publisher.Publisher, error) {
			return publisher.New(namespace, store, -time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			built, err := build()
			if !errors.Is(err, output.ErrIncomplete) {
				t.Fatalf("expected ErrIncomplete, got %v", err)
			}
			if built != nil {
				t.Error("a refused constructor still returned a publisher")
			}
		})
	}

	if _, err := publisher.New(namespace, store, timeout); err != nil {
		t.Fatalf("the complete shape was refused: %v", err)
	}
}

func TestEnsurePublicationRefusesBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a")
	digest := testsupport.Digest("ab")
	resolved := func(t *testing.T, in output.OutputNamespace) output.ObjectMarker {
		return testsupport.Reservation(t, in, reservation, digest)
	}

	t.Run("no canonical bytes", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsurePublication(ctx, resolved(t, namespace), nil, 3)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a negative canonical size", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsurePublication(ctx, resolved(t, namespace), bytes.NewReader([]byte("abc")), -1)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a marker that does not validate", func(t *testing.T) {
		built, recorder := role(t, namespace)
		unresolved := resolved(t, namespace)
		unresolved.ReservationID = ""
		_, err := built.EnsurePublication(ctx, unresolved, bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrCorrupt) {
			t.Errorf("expected ErrCorrupt, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	// A capture publishes into the namespace its epoch derived and no other.
	// The reservation is resolved by the control plane, so a scope that is
	// not this publisher's is not a caller's mistake to be corrected: it is a
	// publisher being asked to write into somebody else's namespace.
	t.Run("a reservation resolved to another tenant's scope", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsurePublication(ctx, resolved(t, namespaceFor(t, "tenant-b")),
			bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrUnauthorized) {
			t.Errorf("expected ErrUnauthorized, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	// The marker is the ownership evidence written once at creation. A
	// marker naming another epoch under this namespace's scope would be
	// evidence about an epoch this publisher was not derived under.
	t.Run("a marker naming another activation epoch", func(t *testing.T) {
		built, recorder := role(t, namespace)
		stale := resolved(t, namespace)
		stale.ActivationEpoch = epoch + 1
		if err := stale.Validate(); err != nil {
			t.Fatalf("the fixture must be a valid reservation for the case to be about the epoch: %v", err)
		}
		_, err := built.EnsurePublication(ctx, stale, bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrConflict) {
			t.Errorf("expected ErrConflict, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})
}

func TestStatExactObjectRefusesARefOutsideItsNamespace(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a")
	built, recorder := role(t, namespace)

	other := namespaceFor(t, "tenant-b")
	_, err := built.StatExactObject(ctx, other.Ref(testsupport.Digest("cd"), 1))
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
	testsupport.ExpectNoRPC(t, recorder)

	// And a ref that does not validate is refused as what it is, not as
	// somebody else's.
	_, err = built.StatExactObject(ctx, hangar.TreeRef{Scope: namespace.Scope()})
	if err == nil {
		t.Error("an incomplete ref was accepted")
	}
	if errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("an incomplete ref was reported as unauthorized: %v", err)
	}
	testsupport.ExpectNoRPC(t, recorder)
}

func TestOpenExactObjectRefusesAWarrantForAnotherRefBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a")
	built, recorder := role(t, namespace)

	ref := namespace.Ref(testsupport.Digest("ef"), 1)
	warrant := testsupport.Warrant(t, ref, epoch)

	another := ref
	another.Generation++
	if _, _, err := built.OpenExactObject(ctx, another, warrant); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a read outside the warrant was answered with %v, expected ErrUnauthorized", err)
	}
	testsupport.ExpectNoRPC(t, recorder)

	incomplete := warrant
	incomplete.Nonce = ""
	if _, _, err := built.OpenExactObject(ctx, ref, incomplete); err == nil {
		t.Error("a warrant that does not validate authorized a read")
	}
	testsupport.ExpectNoRPC(t, recorder)
}

// A result published under scope v1 -- H(v1 domain, tenant, epoch) -- before
// the scope dropped the epoch is still readable by exact generation: the
// stat and the open derive its key from its own scope.
func TestARefPublishedUnderScopeV1IsStillReadable(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a")
	memory, recorder := testsupport.RecordedMemory(bucket)
	built, err := publisher.New(namespace, publisher.Restrict(recorder), timeout)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.New()
	sum.Write([]byte("hangar-output-scope-v1"))
	sum.Write([]byte{0})
	sum.Write([]byte("tenant-a"))
	sum.Write([]byte{0})
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(epoch))
	sum.Write(encoded[:])
	legacy := hangar.Scope("o" + hex.EncodeToString(sum.Sum(nil)[:20]))

	digest := testsupport.Digest("ab")
	key, err := hangar.TreeKey(namespace.Prefix(), legacy, digest)
	if err != nil {
		t.Fatal(err)
	}
	marker := output.ObjectMarker{Version: output.MarkerVersion, Scope: legacy, Digest: digest,
		ReservationID: reservation, ActivationEpoch: epoch, CreatedAt: output.NewTimestamp(testsupport.FixedInstant)}
	attrs := memory.Seed(bucket, key, []byte("published before scope v2"), marker.Metadata())

	ref := hangar.TreeRef{Scope: legacy, Digest: digest, Generation: attrs.Generation}
	object, err := built.StatExactObject(ctx, ref)
	if err != nil {
		t.Fatalf("a scope-v1 ref of this tenant was refused: %v", err)
	}
	if object.Attributes.Ref != ref {
		t.Errorf("the stat answered %v, want %v", object.Attributes.Ref, ref)
	}
}
