package hangaroutput_test

// Lease control over a real HTTP round trip, with a real signer on the daemon's
// side and the real repository on the control plane's.
//
// The whole point of these endpoints is that a grant is not authority: the
// daemon holds a token the control plane really minted, and the control plane
// still answers `no` when the lease behind it is gone. So every refusal row
// below presents a VALID, unexpired, correctly signed grant and changes only
// what the database says. A row that tampered with the token would be testing
// the HMAC again, which readgrant_test.go already does.
//
// The control row is first: the same grant, the same client, the same route,
// admitted -- because a control plane that refused everything would pass a
// table made only of refusals.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type leaseFixture struct {
	Control *hangaroutput.LeaseControl
	Server  *httptest.Server
	Client  *output.LeaseControlClient
	Grant   string
	Lease   output.ReadLease
}

func newLeaseFixture(t *testing.T, h *harness, grant hangaroutput.ReadGrant) *leaseFixture {
	t.Helper()

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("node control key: %v", err)
	}
	signer, err := output.NewCaptureStatementSigner(private)
	if err != nil {
		t.Fatalf("statement signer: %v", err)
	}
	verifier, err := output.NewReadGrantVerifier(readGrantKey,
		output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		t.Fatalf("grant verifier: %v", err)
	}

	control := &hangaroutput.LeaseControl{
		Transactor: h.Coordinator.Transactor,
		Leases:     h.Repository,
		Grants:     verifier,
		Keys: hangaroutput.NodeKeysFunc(
			func(node executioncontrol.NodeUID, keyID string) (ed25519.PublicKey, error) {
				if node != harnessNode || keyID != "node-control-1" {
					return nil, errors.New("no key pinned for that node")
				}

				return public, nil
			}),
		Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() }),
	}

	server := httptest.NewServer(control.Handler())
	t.Cleanup(server.Close)

	return &leaseFixture{
		Control: control,
		Server:  server,
		Client: &output.LeaseControlClient{
			BaseURL: server.URL,
			HTTP:    server.Client(),
			NodeUID: harnessNode,
			KeyID:   "node-control-1",
			Signer:  signer,
			Clock:   output.ClockFunc(func() time.Time { return time.Now().UTC() }),
		},
		Grant: grant.Token,
		Lease: grant.Lease,
	}
}

func admittedGrant(t *testing.T, h *harness) hangaroutput.ReadGrant {
	t.Helper()

	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)
	admission, _ := readAdmission(t, h)

	grant, err := admission.Admit(context.Background(), readRequest(t, claimID, ref))
	if err != nil {
		t.Fatalf("admitting the read this lease-control fixture is about: %v", err)
	}

	return grant
}

// The control: a real grant over a live lease is admitted, and the answer
// carries the lease and the destination the control plane has rather than the
// ones the caller sent.
func TestLeaseControlAdmitsALiveLease(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))

	answer, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant, 10*time.Minute)
	if err != nil {
		t.Fatalf("validating a live lease: %v", err)
	}
	if !answer.Admitted {
		t.Fatalf("a live lease was refused as %q", answer.Refusal)
	}
	if answer.Lease.ReadLeaseID != fixture.Lease.ReadLeaseID {
		t.Errorf("the answer names lease %q, the grant named %q",
			answer.Lease.ReadLeaseID, fixture.Lease.ReadLeaseID)
	}
	if answer.Destination.Handle != "consumer-handle" || answer.Destination.Volume != "input-0" {
		t.Errorf("the answer's destination is %+v", answer.Destination)
	}
}

// A released lease, with the same valid grant.
func TestLeaseControlRefusesAGrantWhoseLeaseIsGone(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))

	// The control first, so "refused" below is about the release.
	if answer, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant,
		time.Minute); err != nil || !answer.Admitted {
		t.Fatalf("the lease was not live before it was released: %v %+v", err, answer)
	}

	answer, err := fixture.Client.ReleaseLease(context.Background(), fixture.Grant)
	if err != nil || !answer.Admitted {
		t.Fatalf("releasing a live lease: %v %+v", err, answer)
	}

	answer, err = fixture.Client.ValidateLease(context.Background(), fixture.Grant, time.Minute)
	if err != nil {
		t.Fatalf("validating a released lease was a transport error: %v", err)
	}
	if answer.Admitted {
		t.Fatal("a grant whose lease was released still authorized a read; its HMAC is still " +
			"perfectly valid, which is the whole reason the daemon asks")
	}
	if answer.Refusal != output.LeaseRefusedConflict {
		t.Errorf("a released lease was refused as %q, want %q",
			answer.Refusal, output.LeaseRefusedConflict)
	}
}

// Work that would outrun the lease.
func TestLeaseControlRefusesWorkThatWouldOutliveTheLease(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))

	answer, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant, 24*time.Hour)
	if err != nil {
		t.Fatalf("validating: %v", err)
	}
	if answer.Admitted {
		t.Fatal("a day of work was admitted under a twenty-minute lease")
	}
	if answer.Refusal != output.LeaseRefusedExpired {
		t.Errorf("refused as %q, want %q", answer.Refusal, output.LeaseRefusedExpired)
	}
}

// Who is asking.
func TestLeaseControlRefusesAQuestionItCannotAttribute(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))

	for _, test := range []struct {
		name  string
		spoil func(*output.LeaseControlClient)
	}{
		{"a node nothing pins a key for", func(client *output.LeaseControlClient) {
			client.NodeUID = executioncontrol.NodeUID("some-other-node")
		}},
		{"a key id nothing pins", func(client *output.LeaseControlClient) {
			client.KeyID = "node-control-9"
		}},
		{"another node's key", func(client *output.LeaseControlClient) {
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatalf("another key: %v", err)
			}
			signer, err := output.NewCaptureStatementSigner(other)
			if err != nil {
				t.Fatalf("another signer: %v", err)
			}
			client.Signer = signer
		}},
		{"a question dated an hour ago", func(client *output.LeaseControlClient) {
			client.Clock = output.ClockFunc(func() time.Time {
				return time.Now().UTC().Add(-time.Hour)
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := *fixture.Client
			test.spoil(&client)

			answer, err := client.ValidateLease(context.Background(), fixture.Grant, time.Minute)
			if err != nil {
				t.Fatalf("validating: %v", err)
			}
			if answer.Admitted {
				t.Fatalf("%s was admitted", test.name)
			}
			if answer.Refusal != output.LeaseRefusedUnauthorized {
				t.Errorf("refused as %q, want %q", answer.Refusal, output.LeaseRefusedUnauthorized)
			}
		})
	}
}

// A renewal keeps the reader going, and a release afterwards is idempotent.
func TestLeaseControlRenewsAndThenReleasesIdempotently(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))

	before := fixture.Lease.ExpiresAt.Time
	answer, err := fixture.Client.RenewLease(context.Background(), fixture.Grant, time.Minute)
	if err != nil || !answer.Admitted {
		t.Fatalf("renewing a live lease: %v %+v", err, answer)
	}
	if !answer.Lease.ExpiresAt.After(before) {
		t.Errorf("a renewal did not move the expiry: %s -> %s", before, answer.Lease.ExpiresAt)
	}

	for attempt := 0; attempt < 2; attempt++ {
		answer, err := fixture.Client.ReleaseLease(context.Background(), fixture.Grant)
		if err != nil {
			t.Fatalf("release attempt %d: %v", attempt, err)
		}
		if attempt == 0 && !answer.Admitted {
			t.Fatalf("the first release was refused as %q", answer.Refusal)
		}
		// The second release meets a lease that is already released, and the
		// control plane says so rather than pretending. Idempotence is the
		// STORE's -- the row does not move -- and the answer is honest about
		// the state it found.
		if attempt == 1 && answer.Admitted {
			t.Error("a second release was admitted; a released lease does not reactivate")
		}
	}

	var released int
	if err := h.Conn.QueryRow(`
		SELECT count(*) FROM hangar_read_leases
		WHERE read_lease_id = $1 AND released_at IS NOT NULL`,
		string(fixture.Lease.ReadLeaseID)).Scan(&released); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if released != 1 {
		t.Errorf("%d released rows for one lease released twice", released)
	}
}

// A renewal or a release presented for a lease the grant does not describe.
//
// This is the case that makes validation run for all three operations: a token
// for one read must not be able to close another read's protection.
func TestLeaseControlRefusesAReleaseForAnotherReadsLease(t *testing.T) {
	h := newHarness(t)

	first := admittedGrant(t, h)
	fixture := newLeaseFixture(t, h, first)

	// A second read of the same ref under its own claim and its own lease.
	second := admittedGrant(t, h)
	if second.Lease.ReadLeaseID == first.Lease.ReadLeaseID {
		t.Fatal("the two reads share a lease id, so this proves nothing")
	}

	// The first grant closes the FIRST lease and nothing else.
	if answer, err := fixture.Client.ReleaseLease(context.Background(),
		first.Token); err != nil || !answer.Admitted {
		t.Fatalf("releasing the first lease: %v %+v", err, answer)
	}

	var stillOpen int
	if err := h.Conn.QueryRow(`
		SELECT count(*) FROM hangar_read_leases
		WHERE read_lease_id = $1 AND released_at IS NULL`,
		string(second.Lease.ReadLeaseID)).Scan(&stillOpen); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if stillOpen != 1 {
		t.Error("one read's grant closed another read's lease")
	}

	// And the daemon's own database view agrees.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)
	if _, err := h.Repository.LoadReadLease(context.Background(), tx,
		second.Lease.ReadLeaseID); err != nil {
		t.Errorf("the second lease is not loadable after the first was released: %v", err)
	}
}

// A refusal is a value, and an outage is not a refusal.
func TestAnUnreachableControlPlaneIsNotARevocation(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))
	fixture.Server.Close()

	_, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant, time.Minute)
	if !errors.Is(err, output.ErrInfrastructure) {
		t.Fatalf("an unreachable control plane answered %v; a daemon that read that as a "+
			"revocation would fail a task for an outage", err)
	}
}
