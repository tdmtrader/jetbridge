package publisher_test

// The publisher's own refusals: the ones decided from its arguments and its
// derived namespace before any RPC is issued. The conformance suite beside
// this package proves what the role does WITH a store; this file proves what
// it refuses to do without one, and the recorder is what makes "without one"
// a fact rather than a reading of the code.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

const (
	bucket      = "publisher-spec"
	epoch       = executioncontrol.ActivationEpoch(7)
	reservation = output.ReservationID("44444444-4444-4444-8444-444444444444")
	timeout     = 10 * time.Second
)

var fixedInstant = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func namespaceFor(t *testing.T, tenant string, activation executioncontrol.ActivationEpoch) output.OutputNamespace {
	t.Helper()

	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store:            output.StoreGCS,
		Bucket:           bucket,
		DeploymentPrefix: "deployments/blue",
		TenantID:         tenant,
		ActivationEpoch:  activation,
	})
	if err != nil {
		t.Fatalf("deriving the namespace: %v", err)
	}

	return namespace
}

func digestOf(fill string) hangar.Digest {
	return hangar.Digest("sha256:" + strings.Repeat(fill, 64)[:64])
}

func reservationFor(t *testing.T, namespace output.OutputNamespace, digest hangar.Digest) output.ResolvedReservation {
	t.Helper()

	resolved := output.ResolvedReservation{
		ReservationID: reservation,
		Execution: executioncontrol.Identity{
			ExecutionID: "33333333-3333-4333-8333-333333333333",
			Fence:       1,
		},
		ActivationEpoch: namespace.ActivationEpoch(),
		HandoffID:       "11111111-1111-4111-8111-111111111111",
		CaptureFence:    1,
		Scope:           namespace.Scope(),
		Digest:          digest,
		Marker:          namespace.MarkerFor(reservation, digest, output.NewTimestamp(fixedInstant)),
	}
	if err := resolved.Validate(); err != nil {
		t.Fatalf("the fixture reservation does not validate: %v", err)
	}

	return resolved
}

// role builds a publisher over a recorded tier-1 store, so a test can say
// which RPCs the role issued and, for the refusals here, that it issued none.
func role(t *testing.T, namespace output.OutputNamespace) (*publisher.Publisher, *gcstest.Recorder) {
	t.Helper()

	memory := gcstest.NewMemory()
	memory.CreateBucket(bucket)
	recorder := gcstest.Record(memory)
	built, err := publisher.New(namespace, publisher.Restrict(recorder), timeout)
	if err != nil {
		t.Fatalf("building the publisher: %v", err)
	}

	return built, recorder
}

func expectNoRPC(t *testing.T, recorder *gcstest.Recorder) {
	t.Helper()

	if calls := recorder.Calls(); len(calls) != 0 {
		t.Errorf("the store was reached: %v. A refusal decided from the arguments must be "+
			"decided before any RPC, or the refused caller learns something about the bucket", calls)
	}
}

func TestNewRefusesAnIncompleteRole(t *testing.T) {
	namespace := namespaceFor(t, "tenant-a", epoch)
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

func TestEnsureObjectRefusesBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a", epoch)
	digest := digestOf("ab")

	t.Run("no canonical bytes", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsureObject(ctx, reservationFor(t, namespace, digest), nil, 3)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		expectNoRPC(t, recorder)
	})

	t.Run("a negative canonical size", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsureObject(ctx, reservationFor(t, namespace, digest),
			bytes.NewReader([]byte("abc")), -1)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		expectNoRPC(t, recorder)
	})

	t.Run("a reservation that does not validate", func(t *testing.T) {
		built, recorder := role(t, namespace)
		unresolved := reservationFor(t, namespace, digest)
		unresolved.CaptureFence = 0
		_, err := built.EnsureObject(ctx, unresolved, bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		expectNoRPC(t, recorder)
	})

	// A capture publishes into the namespace its epoch derived and no other.
	// The reservation is resolved by the control plane, so a scope that is
	// not this publisher's is not a caller's mistake to be corrected: it is a
	// publisher being asked to write into somebody else's namespace.
	t.Run("a reservation resolved to another tenant's scope", func(t *testing.T) {
		built, recorder := role(t, namespace)
		other := namespaceFor(t, "tenant-b", epoch)
		_, err := built.EnsureObject(ctx, reservationFor(t, other, digest),
			bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrUnauthorized) {
			t.Errorf("expected ErrUnauthorized, got %v", err)
		}
		expectNoRPC(t, recorder)
	})

	// The marker is the ownership evidence written once at creation. A
	// marker naming another epoch under this namespace's scope would be
	// evidence about an epoch this publisher was not derived under.
	t.Run("a marker naming another activation epoch", func(t *testing.T) {
		built, recorder := role(t, namespace)
		stale := reservationFor(t, namespace, digest)
		stale.ActivationEpoch = epoch + 1
		stale.Marker.ActivationEpoch = epoch + 1
		if err := stale.Validate(); err != nil {
			t.Fatalf("the fixture must be a valid reservation for the case to be about the epoch: %v", err)
		}
		_, err := built.EnsureObject(ctx, stale, bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrConflict) {
			t.Errorf("expected ErrConflict, got %v", err)
		}
		expectNoRPC(t, recorder)
	})
}

func TestStatExactObjectRefusesARefOutsideItsNamespace(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a", epoch)
	built, recorder := role(t, namespace)

	other := namespaceFor(t, "tenant-b", epoch)
	_, err := built.StatExactObject(ctx, other.Ref(digestOf("cd"), 1))
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
	expectNoRPC(t, recorder)

	// And a ref that does not validate is refused as what it is, not as
	// somebody else's.
	_, err = built.StatExactObject(ctx, hangar.TreeRef{Scope: namespace.Scope()})
	if err == nil {
		t.Error("an incomplete ref was accepted")
	}
	if errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("an incomplete ref was reported as unauthorized: %v", err)
	}
	expectNoRPC(t, recorder)
}

func TestOpenExactObjectRefusesALeaseForAnotherRefBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a", epoch)
	built, recorder := role(t, namespace)

	ref := namespace.Ref(digestOf("ef"), 1)
	lease := output.ReadLease{
		ProtocolVersion: output.ProtocolVersion,
		ReadLeaseID:     "22222222-2222-4222-8222-222222222222",
		ClaimID:         "66666666-6666-4666-8666-666666666666",
		Ref:             ref,
		ActivationEpoch: epoch,
		LeaseFence:      1,
		GrantedAt:       output.NewTimestamp(fixedInstant),
		ExpiresAt:       output.NewTimestamp(fixedInstant.Add(20 * time.Minute)),
	}
	if err := lease.Validate(); err != nil {
		t.Fatalf("the fixture lease does not validate: %v", err)
	}

	another := ref
	another.Generation++
	if _, _, err := built.OpenExactObject(ctx, another, lease); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a read outside the lease was answered with %v, expected ErrUnauthorized", err)
	}
	expectNoRPC(t, recorder)

	unleased := lease
	unleased.LeaseFence = 0
	if _, _, err := built.OpenExactObject(ctx, ref, unleased); err == nil {
		t.Error("a lease that does not validate authorized a read")
	}
	expectNoRPC(t, recorder)
}
