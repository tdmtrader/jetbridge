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
	"net/http"
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

	minter, err := output.NewReadGrantSigner(readGrantKey)
	if err != nil {
		t.Fatalf("grant signer: %v", err)
	}

	control := &hangaroutput.LeaseControl{
		Transactor: h.Coordinator.Transactor,
		Leases:     h.Repository,
		Grants:     verifier,
		Minter:     minter,
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

// A RENEWED READER OUTLIVES ITS ORIGINAL GRANT WINDOW.
//
// The grant is dated with the lease's granted-at and expires-at, and requirement
// 37's byte-identical re-mint is why: nothing in the token may come from the
// instant it was minted. But a RENEWAL moves the row's expiry and cannot move a
// token that has already been handed out, so if the control plane decided the
// window from the token, everything a renewed reader did after its original
// expiry -- validating, renewing again, and above all RELEASING -- would be
// answered `unauthorized`. The lease would then close only by expiry plus
// recovery, and the renewal mechanism Green box 3 describes would have no
// reachable effect at all.
//
// Two halves, and this spec asserts both against a row that is live on the
// DATABASE clock while the verifier's clock sits past the original window:
//
//   - the control plane checks what the token BINDS and takes the window from
//     the row it names, because the row is the only thing that knows about a
//     renewal;
//   - a renewal answers with a grant RE-MINTED over the renewed row, so the
//     reader's token catches up and the daemon's own window check -- which
//     stays, and is what stops a stale token from ever opening anything --
//     keeps passing.
func TestARenewedReaderKeepsWorkingPastItsOriginalGrantWindow(t *testing.T) {
	h := newHarness(t)
	grant := admittedGrant(t, h)
	fixture := newLeaseFixture(t, h, grant)

	renewed, err := fixture.Client.RenewLease(context.Background(), fixture.Grant, time.Minute)
	if err != nil || !renewed.Admitted {
		t.Fatalf("renewing a live lease: %v %+v", err, renewed)
	}
	if renewed.Grant == "" {
		t.Fatal("a renewal answered with no grant; the reader's token still names the window " +
			"the renewal just moved, and nothing else will ever hand it a current one")
	}
	if renewed.Grant == fixture.Grant {
		t.Error("the renewal handed back the same token it was given, so the renewed window is " +
			"in no token the reader holds")
	}

	// The verifier's clock, one second past the ORIGINAL window. The row is
	// untouched and live on the database clock, which is the clock that decides.
	past, err := output.NewReadGrantVerifier(readGrantKey,
		output.ClockFunc(func() time.Time { return grant.Lease.ExpiresAt.Add(time.Second).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	fixture.Control.Grants = past

	// The ORIGINAL token, past its own expiry, over a row that is still live.
	answer, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant, time.Minute)
	if err != nil {
		t.Fatalf("validating with the original grant: %v", err)
	}
	if !answer.Admitted {
		t.Errorf("a live lease was refused as %q because the TOKEN's window had passed; the "+
			"window is the row's", answer.Refusal)
	}

	// And the re-minted token, which is what the reader actually carries on.
	for _, step := range []struct {
		name string
		ask  func(string) (output.LeaseAnswer, error)
	}{
		{"validate", func(token string) (output.LeaseAnswer, error) {
			return fixture.Client.ValidateLease(context.Background(), token, time.Minute)
		}},
		{"renew", func(token string) (output.LeaseAnswer, error) {
			return fixture.Client.RenewLease(context.Background(), token, time.Minute)
		}},
		{"release", func(token string) (output.LeaseAnswer, error) {
			return fixture.Client.ReleaseLease(context.Background(), token)
		}},
	} {
		answer, err := step.ask(renewed.Grant)
		if err != nil {
			t.Fatalf("%s with the re-minted grant: %v", step.name, err)
		}
		if !answer.Admitted {
			t.Fatalf("%s with the re-minted grant was refused as %q", step.name, answer.Refusal)
		}
	}

	var released bool
	if err := h.Conn.QueryRow(
		`SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id = $1`,
		string(grant.Lease.ReadLeaseID)).Scan(&released); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if !released {
		t.Error("a renewed reader could not release its own lease, so the protection it no " +
			"longer needs is held until recovery closes it")
	}
}

// AND THE RE-MINTED TOKEN IS THE ONE THE DAEMON WILL ACCEPT.
//
// The spec above renews the instant the lease is admitted, so the window it
// moves by is the microseconds of wall clock between the two calls, and every
// question it then asks goes through the CONTROL PLANE -- which takes the window
// from the row and does not look at the token's. What holds its line is
// `renewed.Grant != fixture.Grant`, and that catches only a byte-identical
// re-mint: a token carrying the PRE-RENEWAL expiry with any other field
// differing would pass it. The distinguishing check is the daemon's own Verify,
// which is where a stale window actually stops a read, and nothing ran it on a
// re-minted token.
//
// So this one moves the window by a REAL ten minutes and asks the daemon's
// question. The row is aged by moving all three instants back together, which
// preserves its fifteen-minute term and changes only how much is left; the
// reader's token is then minted by the production signer over the committed row
// read back through LoadReadLease, which is the method the minter exists for --
// a token minted before the ageing would name a window the row never had.
//
// The three answers, at one clock five seconds past the reader's own window:
//
//   - the daemon REFUSES the stale pre-renewal token -- a token whose window has
//     passed opens nothing, which is what the window is for;
//   - the daemon ADMITS the re-minted one, and it names the renewed row's
//     expiry, so the reader carries authority it can actually use;
//   - the control plane still admits the stale token's BINDING, because what a
//     grant binds is the MAC's to settle and whether the lease is live is the
//     row's. A renewed reader must still be able to release.
func TestARenewalRemintsTheWindowTheDaemonChecks(t *testing.T) {
	h := newHarness(t)
	admitted := admittedGrant(t, h)

	if _, err := h.Conn.Exec(`
		UPDATE hangar_read_leases
		SET granted_at = granted_at - interval '10 minutes',
		    renewed_at = renewed_at - interval '10 minutes',
		    expires_at = expires_at - interval '10 minutes'
		WHERE read_lease_id = $1`, string(admitted.Lease.ReadLeaseID)); err != nil {
		t.Fatalf("ageing the lease: %v", err)
	}

	loading, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(loading)
	record, err := h.Repository.LoadReadLease(context.Background(), loading,
		admitted.Lease.ReadLeaseID)
	if err != nil {
		t.Fatalf("reading the aged lease back: %v", err)
	}
	if err := loading.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	minter, err := output.NewReadGrantSigner(readGrantKey)
	if err != nil {
		t.Fatalf("grant signer: %v", err)
	}
	stale, err := minter.Sign(record.Lease, record.Destination, record.GrantNonce)
	if err != nil {
		t.Fatalf("minting the reader's token over the aged row: %v", err)
	}

	fixture := newLeaseFixture(t, h, hangaroutput.ReadGrant{
		Token: stale, Lease: record.Lease, Record: record})

	renewed, err := fixture.Client.RenewLease(context.Background(), stale, time.Minute)
	if err != nil || !renewed.Admitted {
		t.Fatalf("renewing a lease with five minutes left: %v %+v", err, renewed)
	}
	if renewed.Grant == "" || renewed.Grant == stale {
		t.Fatal("the renewal handed back no new token, so the reader still carries the window " +
			"this renewal just moved")
	}

	// The daemon's verifier, past the reader's own window and well inside the
	// renewed one. Its clock is the NODE's; the row is live on the database's.
	daemon, err := output.NewReadGrantVerifier(readGrantKey, output.ClockFunc(func() time.Time {
		return record.Lease.ExpiresAt.Add(5 * time.Second).UTC()
	}))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := daemon.Verify(stale, record.Lease.Ref, record.Destination); !errors.Is(err,
		output.ErrUnauthorized) {
		t.Errorf("the daemon answered %v to a token whose window has passed; a stale grant "+
			"opening an object is what the window is for", err)
	}

	claims, err := daemon.Verify(renewed.Grant, record.Lease.Ref, record.Destination)
	if err != nil {
		t.Fatalf("the daemon refused the RE-MINTED token as %v; the reader's lease is live, it "+
			"has just been renewed, and the one token it carries opens nothing", err)
	}
	if !claims.ExpiresAt.UTC().Equal(renewed.Lease.ExpiresAt.UTC()) {
		t.Errorf("the re-minted token names %s and the renewed row expires at %s; a token dated "+
			"from anything but the committed row is a token for a lease that may never have "+
			"existed", claims.ExpiresAt.UTC(), renewed.Lease.ExpiresAt.UTC())
	}

	// And the interval is real, which is what makes the clock above a question
	// about a renewal rather than about a microsecond.
	if moved := renewed.Lease.ExpiresAt.Sub(record.Lease.ExpiresAt.Time); moved < 9*time.Minute {
		t.Errorf("the renewal moved the window by %s; this spec ages the lease by ten minutes "+
			"so that the two windows are distinguishable at all", moved)
	}

	// The control plane, asked with the stale token: admitted. A renewed reader
	// that could not validate, renew again or RELEASE with the token it was
	// handed would hold its protection until recovery closed it.
	answer, err := fixture.Client.ValidateLease(context.Background(), stale, time.Minute)
	if err != nil {
		t.Fatalf("validating with the stale token: %v", err)
	}
	if !answer.Admitted {
		t.Errorf("the control plane refused a live lease as %q because the TOKEN's window had "+
			"passed; the window is the row's", answer.Refusal)
	}
}

// An OUTAGE between the daemon and the control plane is not a corrupt answer.
//
// The client decoded whatever came back without looking at the status, so a
// proxy's HTML 502, an ingress's 503 or a 401 from something that is not the
// control plane at all came out as ErrCorrupt -- "the answer does not decode",
// which reads as "the control plane is broken". The daemon has to tell "the
// lease is gone" from "the control plane was not reached" (the spec above says
// so for a closed server), and a status this protocol never uses belongs on the
// second side of that line.
//
// The control is first and it is the real handler: the statuses the control
// plane itself produces still decode as answers.
func TestAStatusTheControlPlaneNeverSendsIsAnOutageAndNotACorruptAnswer(t *testing.T) {
	h := newHarness(t)
	fixture := newLeaseFixture(t, h, admittedGrant(t, h))

	if answer, err := fixture.Client.ValidateLease(context.Background(), fixture.Grant,
		time.Minute); err != nil || !answer.Admitted {
		t.Fatalf("the real handler did not answer before this spec replaced it: %v %+v",
			err, answer)
	}

	for _, gateway := range []struct {
		name   string
		status int
		body   string
	}{
		{"a proxy's HTML 502", http.StatusBadGateway, "<html><body>502 Bad Gateway</body></html>"},
		{"an ingress 504", http.StatusGatewayTimeout, "upstream timed out"},
		{"a 401 from something that is not the control plane", http.StatusUnauthorized, "{}"},
	} {
		t.Run(gateway.name, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(
				func(writer http.ResponseWriter, _ *http.Request) {
					writer.WriteHeader(gateway.status)
					_, _ = writer.Write([]byte(gateway.body))
				}))
			defer proxy.Close()

			client := *fixture.Client
			client.BaseURL = proxy.URL
			client.HTTP = proxy.Client()

			_, err := client.ValidateLease(context.Background(), fixture.Grant, time.Minute)
			if !errors.Is(err, output.ErrInfrastructure) {
				t.Fatalf("%s was answered %v; a daemon that read that as a revocation would "+
					"fail a task for an outage", gateway.name, err)
			}
			if errors.Is(err, output.ErrCorrupt) {
				t.Error("an unreached control plane was reported as a corrupt answer")
			}
		})
	}
}
