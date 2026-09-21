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

func TestEnsureObjectRefusesBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a")
	digest := testsupport.Digest("ab")
	resolved := func(t *testing.T, in output.OutputNamespace) output.ResolvedReservation {
		return testsupport.Reservation(t, in, reservation, digest)
	}

	t.Run("no canonical bytes", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsureObject(ctx, resolved(t, namespace), nil, 3)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a negative canonical size", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsureObject(ctx, resolved(t, namespace), bytes.NewReader([]byte("abc")), -1)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a reservation that does not validate", func(t *testing.T) {
		built, recorder := role(t, namespace)
		unresolved := resolved(t, namespace)
		unresolved.CaptureFence = 0
		_, err := built.EnsureObject(ctx, unresolved, bytes.NewReader([]byte("abc")), 3)
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	// A capture publishes into the namespace its epoch derived and no other.
	// The reservation is resolved by the control plane, so a scope that is
	// not this publisher's is not a caller's mistake to be corrected: it is a
	// publisher being asked to write into somebody else's namespace.
	t.Run("a reservation resolved to another tenant's scope", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.EnsureObject(ctx, resolved(t, namespaceFor(t, "tenant-b")),
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
		stale.Marker.ActivationEpoch = epoch + 1
		if err := stale.Validate(); err != nil {
			t.Fatalf("the fixture must be a valid reservation for the case to be about the epoch: %v", err)
		}
		_, err := built.EnsureObject(ctx, stale, bytes.NewReader([]byte("abc")), 3)
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

func TestOpenExactObjectRefusesALeaseForAnotherRefBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t, "tenant-a")
	built, recorder := role(t, namespace)

	ref := namespace.Ref(testsupport.Digest("ef"), 1)
	lease := testsupport.Lease(t, ref, epoch)

	another := ref
	another.Generation++
	if _, _, err := built.OpenExactObject(ctx, another, lease); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a read outside the lease was answered with %v, expected ErrUnauthorized", err)
	}
	testsupport.ExpectNoRPC(t, recorder)

	unleased := lease
	unleased.LeaseFence = 0
	if _, _, err := built.OpenExactObject(ctx, ref, unleased); err == nil {
		t.Error("a lease that does not validate authorized a read")
	}
	testsupport.ExpectNoRPC(t, recorder)
}
