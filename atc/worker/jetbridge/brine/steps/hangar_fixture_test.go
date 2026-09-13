package steps

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	hangargcs "github.com/concourse/concourse/hangar/gcs"
)

// The fixture's bucket create, against an endpoint nothing answers.
//
// This is a regression test for a hang rather than for a wrong answer. The GCS
// client retries a refused connection with backoff, and on the background
// context the fixture used to hand it that retry had no end: with
// HANGAR_FAKE_GCS_ENDPOINT pointed at a closed port, `brine run
// features/hangar-fixture.feature` did not finish in 300 seconds and had to be
// killed. A fixture that waits is a job timeout with no reason in it, which
// tells an operator nothing about the service name they mistyped.
//
// So: a real hangar/gcs client -- the production seam, not a stub -- against a
// port this test closes itself, and the assertions are that the call returns,
// returns inside its own bound, and names the endpoint and the variable.
func TestTheHangarFixtureFailsOnAnUnreachableEndpointInsteadOfWaiting(t *testing.T) {
	endpoint := closedEndpoint(t)

	client, err := hangargcs.NewStorageClient(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("building a storage client for %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const (
		attempts   = 2
		perAttempt = time.Second
	)

	started := time.Now()
	err = createOutputBucket(context.Background(), endpoint, "brine-hangar-unreachable",
		attempts, perAttempt, func(attemptCtx context.Context) error {
			return client.Bucket("brine-hangar-unreachable").Create(attemptCtx, "brine-hangar-output", nil)
		})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("creating a bucket on a closed port succeeded")
	}
	if ceiling := time.Duration(attempts)*perAttempt + 5*time.Second; elapsed > ceiling {
		t.Errorf("the create took %s, and %d attempts of at most %s bound it to %s. An "+
			"unbounded retry here is the hang this test exists for", elapsed, attempts,
			perAttempt, ceiling)
	}
	for _, want := range []string{endpoint, FakeGCSEndpointEnv, "gave up after 2 attempts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q, so an operator cannot tell which endpoint "+
				"was wrong: %v", want, err)
		}
	}

	// And the production constants are finite, because the parameters above
	// are a test convenience and the call site passes these.
	if hangarBucketCreateAttempts <= 0 || hangarBucketCreateTimeout <= 0 {
		t.Errorf("the fixture's own bound is %d attempts of %s; a non-positive bound is the "+
			"unbounded retry back again", hangarBucketCreateAttempts, hangarBucketCreateTimeout)
	}
}

// closedEndpoint reserves a port and closes it, so "nothing is listening there"
// is a fact rather than a guess about a low port number.
func closedEndpoint(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("closing the reserved port: %v", err)
	}

	return "http://" + address
}
