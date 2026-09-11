package hangaroutput_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The managed read's daemon half, composed against the real control plane.
//
// Real, because what is under test is the ORDER: the lease is validated before
// anything is opened, renewed while bytes move, and released on both paths. A
// double control plane would answer whatever the test wanted in whatever order
// it was asked, which is the one property this composition exists to have.

func TestAManagedReadIsAdmittedByTheLeaseAndNotByTheGrant(t *testing.T) {
	h := newHarness(t)
	grant := admittedGrant(t, h)
	fixture := newLeaseFixture(t, h, grant)

	profile, err := output.NewLeaseReadProfile(fixture.Client, fixture.Grant)
	if err != nil {
		t.Fatalf("building the profile: %v", err)
	}

	// The control: the lease this grant names, its own destination.
	work, err := profile.Admit(context.Background(), grant.Lease.Ref,
		grant.Record.Destination.Handle, grant.Record.Destination.Volume)
	if err != nil {
		t.Fatalf("a live lease was not admitted: %v", err)
	}
	if work <= 0 {
		t.Errorf("the profile admitted %s of working time", work)
	}

	// A DIFFERENT object. The token is valid, unexpired and correctly signed;
	// what is wrong is that the caller is reading something else.
	other := grant.Lease.Ref
	other.Generation++
	if _, err := profile.Admit(context.Background(), other,
		grant.Record.Destination.Handle, grant.Record.Destination.Volume); err == nil {
		t.Error("a grant for one object authorized a read of another")
	} else if !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("the refusal is not typed unauthorized: %v", err)
	}

	// A DIFFERENT destination. A managed output landing somewhere the control
	// plane never agreed to is the same defect wearing a different hat.
	if _, err := profile.Admit(context.Background(), grant.Lease.Ref,
		grant.Record.Destination.Handle, grant.Record.Destination.Volume+"-elsewhere"); err == nil {
		t.Error("a grant for one destination authorized staging into another")
	}
}

// The lease is released on the FAILURE path too.
//
// Protection nobody is using is protection that has to be given back: an
// abandoned lease keeps reclaim admission refusing for its whole term, and the
// generation it names is pinned for exactly that long. A release that only
// happened on success would be one that never happened on the path that needs
// it most.
func TestAFailedManagedReadStillReleasesItsLease(t *testing.T) {
	h := newHarness(t)
	grant := admittedGrant(t, h)
	fixture := newLeaseFixture(t, h, grant)

	profile, err := output.NewLeaseReadProfile(fixture.Client, fixture.Grant)
	if err != nil {
		t.Fatalf("building the profile: %v", err)
	}

	// MaterializeManaged releases whatever the staging did. The staging here
	// fails -- there is no object under this materializer -- and the lease must
	// still be gone afterwards.
	materializer := &hangar.Materializer{}
	staged := materializer.MaterializeManaged(context.Background(), grant.Lease.Ref,
		grant.Record.Destination.Handle, grant.Record.Destination.Volume, profile)
	if staged == nil {
		t.Fatal("the staging succeeded against a materializer with no store; this row cannot " +
			"say anything about the failure path")
	}

	answer, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant, time.Minute)
	if err != nil {
		t.Fatalf("asking about the lease afterwards: %v", err)
	}
	if answer.Admitted {
		t.Error("the lease is still live after a FAILED managed read. An abandoned lease keeps " +
			"reclaim admission refusing for its whole term")
	}
}

// Release is idempotent in this process as well as on the control plane.
//
// MaterializeManaged releases, and a caller that also releases explicitly is the
// normal shape rather than a mistake -- so a second release must not turn a
// successful read into an error.
func TestReleasingTwiceIsNotAnError(t *testing.T) {
	h := newHarness(t)
	grant := admittedGrant(t, h)
	fixture := newLeaseFixture(t, h, grant)

	profile, err := output.NewLeaseReadProfile(fixture.Client, fixture.Grant)
	if err != nil {
		t.Fatalf("building the profile: %v", err)
	}
	if err := profile.Release(context.Background(), nil); err != nil {
		t.Fatalf("the first release: %v", err)
	}
	if err := profile.Release(context.Background(), errors.New("staging failed")); err != nil {
		t.Errorf("the second release: %v", err)
	}
}

// A renewal moves the ROW and the TOKEN together.
//
// A grant is dated with its lease's own instants, so a renewal that did not
// answer with a re-minted token would leave the reader holding one describing a
// window that has passed -- and the daemon's own pre-open window check would
// refuse a lease that is perfectly live.
func TestARenewalCarriesTheReMintedGrantForward(t *testing.T) {
	h := newHarness(t)
	grant := admittedGrant(t, h)
	fixture := newLeaseFixture(t, h, grant)

	profile, err := output.NewLeaseReadProfile(fixture.Client, fixture.Grant)
	if err != nil {
		t.Fatalf("building the profile: %v", err)
	}
	if err := profile.Renew(context.Background()); err != nil {
		t.Fatalf("renewing: %v", err)
	}

	// The re-minted token is what the profile now spends, and it still works.
	if _, err := profile.Admit(context.Background(), grant.Lease.Ref,
		grant.Record.Destination.Handle, grant.Record.Destination.Volume); err != nil {
		t.Errorf("the profile could not use the token its renewal returned: %v", err)
	}
}

// A profile with no client or no grant is refused at construction, rather than
// producing a materialization that reads without authority.
func TestAProfileWithoutAuthorityIsRefusedAtConstruction(t *testing.T) {
	if _, err := output.NewLeaseReadProfile(nil, "token"); err == nil {
		t.Error("a profile was built with no lease-control client")
	} else if !strings.Contains(err.Error(), "never by a grant alone") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	if _, err := output.NewLeaseReadProfile(&output.LeaseControlClient{}, ""); err == nil {
		t.Error("a profile was built with no grant")
	}
}
