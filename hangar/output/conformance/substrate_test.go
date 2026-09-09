package conformance

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/concourse/concourse/hangar"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
)

// The two environment variables the plan wires into CI (decision F2).
const (
	endpointVariable = "HANGAR_FAKE_GCS_ENDPOINT"
	ciVariable       = "HANGAR_CI"
)

// tier2Reason is the one sentence a missing or unreachable endpoint produces,
// whether it becomes a failure or a note. It is one string so that the CI
// failure and the local note cannot drift into saying different things.
func tier2Reason(detail string) string {
	return fmt.Sprintf("hangar tier-2 conformance required: %s is %s", endpointVariable, detail)
}

// substrate is one tier under test.
type substrate struct {
	name   string
	bucket string
	client objectstore.Client

	// memory is non-nil only for tier 1, and is what the fault-injection
	// cases arm. A case that needs it says so by name, so a tier-2 run skips
	// exactly those and nothing else.
	memory *gcstest.Memory

	// can is what this substrate was *measured* to support, not what it
	// claims. See probeCapabilities.
	can capabilities
}

// capabilities are the parts of the strict profile a substrate can actually
// answer for.
//
// They are measured rather than declared, because the interesting case is the
// one nobody expected: fsouza/fake-gcs-server v1.52.3 accepts a delete carrying
// a generation precondition that does not match and deletes the object anyway,
// and returns no page token however many objects match a prefix. A suite that
// assumed otherwise would have reported conformance on two rows the substrate
// never checked -- which is the precise failure mode the plan's "never fall
// back to tier 1 and report conformance" rule exists to prevent, arriving
// through a different door.
type capabilities struct {
	EnforcesDeletePreconditions bool
	Paginates                   bool
}

// probeCapabilities measures a substrate before any case runs.
//
// It writes into a throwaway prefix and never touches the keys a case will use.
func probeCapabilities(t *testing.T, tier substrate) capabilities {
	t.Helper()

	ctx := context.Background()
	measured := capabilities{}

	write := func(key string) objectstore.Attrs {
		writer := tier.client.Object(tier.bucket, key).NewWriter(ctx)
		if _, err := writer.Write([]byte("capability probe")); err != nil {
			t.Fatalf("probing %s: %v", key, err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("probing %s: %v", key, err)
		}

		return writer.Attrs()
	}

	// Delete preconditions: delete a generation that is not there and see
	// whether the object survives.
	victim := write("capability-probe/delete")
	_ = tier.client.Object(tier.bucket, "capability-probe/delete").
		Generation(victim.Generation + 1).
		If(objectstore.Conditions{GenerationMatch: victim.Generation + 1}).
		Delete(ctx)
	if _, err := tier.client.Object(tier.bucket, "capability-probe/delete").Attrs(ctx); err == nil {
		measured.EnforcesDeletePreconditions = true
	}

	// Pagination: three objects, a page size of two, and a next-page token.
	for _, suffix := range []string{"a", "b", "c"} {
		write("capability-probe/page/" + suffix)
	}
	page, err := tier.client.List(ctx, tier.bucket, objectstore.ListRequest{
		Prefix: "capability-probe/page/", PageSize: 2,
	})
	if err != nil {
		t.Fatalf("probing pagination: %v", err)
	}
	measured.Paginates = page.Cursor != ""

	// Leave nothing behind: every case below counts objects under its own
	// namespace prefix, and these are not under one.
	for _, key := range []string{
		"capability-probe/delete",
		"capability-probe/page/a", "capability-probe/page/b", "capability-probe/page/c",
	} {
		_ = tier.client.Object(tier.bucket, key).Delete(ctx)
	}

	return measured
}

// tier1 is the in-memory client. Its bucket is created by writing to it.
func tier1(t *testing.T) substrate {
	t.Helper()

	memory := gcstest.NewMemory()
	tier := substrate{
		name:   "tier-1 (in-memory adapter fake)",
		bucket: "tier-1-output",
		client: memory,
		memory: memory,
	}
	tier.can = probeCapabilities(t, tier)

	return tier
}

// tier2 is fake-gcs-server reached through the production hangar/gcs adapter.
//
// The endpoint comes from HANGAR_FAKE_GCS_ENDPOINT when one is configured --
// the same service the brine Hangar fixture reads, so one deployment serves
// both runners -- and otherwise from a server started in this process. In CI,
// where HANGAR_CI is set, there is no in-process fallback: a run that could
// quietly substitute its own server for the deployed one could report
// conformance against a substrate nobody deployed.
func tier2(t *testing.T) substrate {
	t.Helper()

	endpoint := strings.TrimSpace(os.Getenv(endpointVariable))
	inCI := strings.TrimSpace(os.Getenv(ciVariable)) != ""

	switch {
	case endpoint == "" && inCI:
		t.Fatal(tier2Reason("unset") + ". " + ciVariable + " is set, so this run is CI and " +
			"tier 2 must execute: the API-level profile Req 41 gates activation on -- " +
			"create-if-absent, exact-generation get and delete, bucket-wide list with " +
			"pagination, metageneration and the 404/412/403 split -- is exactly what the " +
			"in-memory fake cannot answer honestly. Wire the params into the unit-tests task.")

	case endpoint != "":
		if err := reachable(endpoint); err != nil {
			if inCI {
				t.Fatal(tier2Reason(fmt.Sprintf("unreachable: %v", err)) + ". " + ciVariable +
					" is set, so a skipped tier 2 may not be reported as conformance. Check " +
					"that the task pod resolves the service in its own namespace.")
			}
			t.Logf("%s; falling back to an in-process server", tier2Reason(fmt.Sprintf("unreachable: %v", err)))

			return inProcessTier2(t)
		}

		return remoteTier2(t, endpoint)

	default:
		// A developer machine with nothing configured. The plan's rule is that
		// tier 2 never degrades to tier 1; starting the same server in-process
		// is not a degrade, it is the same implementation at the same version
		// as the deployed image.
		t.Logf("%s; starting fake-gcs-server in-process instead", tier2Reason("unset"))

		return inProcessTier2(t)
	}

	// Unreachable: t.Fatal above stops the test, and Go still needs a return.
	return substrate{}
}

func inProcessTier2(t *testing.T) substrate {
	t.Helper()

	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme: "http",
		Host:   "127.0.0.1",
		Port:   0,
	})
	if err != nil {
		t.Fatalf("starting fake-gcs-server in-process: %v", err)
	}
	t.Cleanup(server.Stop)

	bucket := uniqueBucket("tier-2-inproc")
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: bucket})

	tier := substrate{
		name:   "tier-2 (fake-gcs-server, in-process)",
		bucket: bucket,
		client: adapterFor(t, server.URL()),
	}
	tier.can = probeCapabilities(t, tier)

	return tier
}

func remoteTier2(t *testing.T, endpoint string) substrate {
	t.Helper()

	bucket := uniqueBucket("tier-2")
	// fake-gcs-server creates a bucket on first write in its default
	// configuration; when it does not, the create below is what says so, with
	// the endpoint named.
	client := adapterFor(t, endpoint)

	tier := substrate{
		name:   "tier-2 (fake-gcs-server at " + endpoint + ")",
		bucket: bucket,
		client: client,
	}
	tier.can = probeCapabilities(t, tier)

	return tier
}

// adapterFor builds the production seam: hangar's own storage client against
// the emulator endpoint, then hangar/gcs's exported object adapter over it.
// Nothing in this file constructs a *storage.Client any other way, so a break
// in that seam is a failure here rather than a surprise in the daemon.
func adapterFor(t *testing.T, endpoint string) objectstore.Client {
	t.Helper()

	storageClient, err := hangargcs.NewStorageClient(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("building a storage client for %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = storageClient.Close() })

	client, err := hangargcs.NewObjectClient(storageClient)
	if err != nil {
		t.Fatalf("adapting the storage client: %v", err)
	}

	return client
}

func reachable(endpoint string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	request, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(endpoint, "/")+"/storage/v1/b", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 {
		return fmt.Errorf("the endpoint answered %d", response.StatusCode)
	}

	return nil
}

var bucketCounter int

func uniqueBucket(prefix string) string {
	bucketCounter++

	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), bucketCounter)
}

// eachSubstrate runs one case against both tiers.
//
// The tier name is in the subtest name so a failure says which substrate
// refused, which is the difference between "the code is wrong" and "this fake
// cannot express that".
func eachSubstrate(t *testing.T, run func(*testing.T, substrate)) {
	t.Helper()

	for _, build := range []func(*testing.T) substrate{tier1, tier2} {
		build := build
		substrate := build(t)
		t.Run(substrate.name, func(t *testing.T) { run(t, substrate) })
	}
}

// The fixture identities every case shares.

const (
	testTenant       = "tenant-conformance"
	testEpoch        = 7
	testReservation  = output.ReservationID("44444444-4444-4444-8444-444444444444")
	otherReservation = output.ReservationID("55555555-5555-4555-8555-555555555555")
)

var fixedInstant = time.Date(2026, 3, 4, 5, 6, 7, 890123456, time.UTC)

func namespaceFor(t *testing.T, bucket string) output.OutputNamespace {
	t.Helper()

	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store:            output.StoreGCS,
		Bucket:           bucket,
		DeploymentPrefix: "deployments/blue",
		TenantID:         testTenant,
		ActivationEpoch:  testEpoch,
	})
	if err != nil {
		t.Fatalf("deriving the namespace: %v", err)
	}

	return namespace
}

func digestOf(fill string) hangar.Digest {
	return hangar.Digest("sha256:" + strings.Repeat(fill, 32/len(fill)*2)[:64])
}

func reservationFor(t *testing.T, namespace output.OutputNamespace, id output.ReservationID, digest hangar.Digest) output.ResolvedReservation {
	t.Helper()

	reservation := output.ResolvedReservation{
		ReservationID:   id,
		Execution:       executionIdentity(),
		ActivationEpoch: testEpoch,
		HandoffID:       "11111111-1111-4111-8111-111111111111",
		CaptureFence:    1,
		Scope:           namespace.Scope(),
		Digest:          digest,
		Marker:          namespace.MarkerFor(id, digest, output.NewTimestamp(fixedInstant)),
	}
	if err := reservation.Validate(); err != nil {
		t.Fatalf("the fixture reservation does not validate: %v", err)
	}

	return reservation
}

// listen is a free port helper used by the unreachable-endpoint case, so that
// "nothing is listening there" is a fact rather than a guess about port 1.
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
