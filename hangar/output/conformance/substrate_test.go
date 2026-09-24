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

	"cloud.google.com/go/storage"
	"github.com/fsouza/fake-gcs-server/fakestorage"
	"google.golang.org/api/option"

	"github.com/concourse/concourse/hangar/disk"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/gcsdelete"
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

// tier2Action is what the gate decides to do about tier 2.
//
// Three members, because the gate really has three answers and a boolean had
// only ever modelled one of them: run against the configured endpoint, start
// the same server in this process, or fail the suite with a named reason.
type tier2Action int

const (
	tier2Remote tier2Action = iota
	tier2InProcess
	tier2Fail
)

func (action tier2Action) String() string {
	switch action {
	case tier2Remote:
		return "remote"
	case tier2InProcess:
		return "in-process"
	default:
		return "fail"
	}
}

// tier2Decision is the whole gate, extracted so it can be driven as a table.
// tier2 itself calls t.Fatal, which a table cannot observe.
func tier2Decision(endpoint string, inCI bool, probe func(string) error) (tier2Action, string) {
	if endpoint == "" {
		if inCI {
			return tier2Fail, tier2Reason("unset") + ". " + ciVariable + " is set, so this run " +
				"is CI and tier 2 must execute: the API-level profile Req 41 gates activation " +
				"on -- create-if-absent, exact-generation get, bucket-wide list with " +
				"pagination, metageneration and the 404/412/403 split -- is exactly what the " +
				"in-memory fake cannot answer honestly. Wire the params into the unit-tests task."
		}

		// A developer machine with nothing configured. The plan's rule is that
		// tier 2 never degrades to tier 1; starting the same server in-process
		// is not a degrade, it is the same implementation at the same version
		// as the deployed image.
		return tier2InProcess, tier2Reason("unset") + "; starting fake-gcs-server in-process instead"
	}

	if err := probe(endpoint); err != nil {
		detail := fmt.Sprintf("unreachable: %v", err)
		if inCI {
			return tier2Fail, tier2Reason(detail) + ". " + ciVariable + " is set, so a skipped " +
				"tier 2 may not be reported as conformance. Check that the task pod resolves " +
				"the service in its own namespace."
		}

		return tier2InProcess, tier2Reason(detail) + "; falling back to an in-process server"
	}

	return tier2Remote, ""
}

// substrate is one tier under test.
type substrate struct {
	name   string
	bucket string
	client objectstore.Client

	// deleter is the capability, and it arrives separately because it IS
	// separate: DeleteExact is not a method on objectstore.Client, and the only
	// constructor of one over a real cloud client lives in hangar/gcsdelete,
	// which exactly one binary links. A suite that drives the reclaimer has to
	// be handed one, which is the whole point.
	deleter objectstore.DeleteClient

	// memory is non-nil only for tier 1, and is what the fault-injection
	// cases arm. A case that needs it says so by name, so a tier-2 run skips
	// exactly those and nothing else.
	memory *gcstest.Memory

	// can is what this substrate was *measured* to support, not what it
	// claims. See probeCapabilities.
	can capabilities

	// endpoint is the HTTP address of the GCS API substrate, and empty for
	// substrates with no API server.
	endpoint string
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
		attrs, err := tier.client.CreateAbsent(ctx, tier.bucket, key, nil, strings.NewReader("capability probe"))
		if err != nil {
			t.Fatalf("probing %s: %v", key, err)
		}
		return attrs
	}

	// Delete preconditions: delete a generation that is not there and see
	// whether the object survives.
	victim := write("capability-probe/delete")
	_ = tier.deleter.DeleteExact(ctx, tier.bucket, "capability-probe/delete", victim.Generation+1)
	if _, err := tier.client.StatCurrent(ctx, tier.bucket, "capability-probe/delete"); err == nil {
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
	measured.Paginates = len(page.Objects) == 2 && !page.Done

	// Leave nothing behind: every case below counts objects under its own
	// namespace prefix, and these are not under one.
	for _, key := range []string{
		"capability-probe/delete",
		"capability-probe/page/a", "capability-probe/page/b", "capability-probe/page/c",
	} {
		if attrs, err := tier.client.StatCurrent(ctx, tier.bucket, key); err == nil {
			_ = tier.deleter.DeleteExact(ctx, tier.bucket, key, attrs.Generation)
		}
	}

	return measured
}

// tier1 is the in-memory client. Its bucket is created by writing to it.
func tier1(t *testing.T) substrate {
	t.Helper()

	memory := gcstest.NewMemory()
	memory.CreateBucket("tier-1-output")
	tier := substrate{
		name:    "tier-1 (in-memory adapter fake)",
		bucket:  "tier-1-output",
		client:  memory,
		deleter: memory,
		memory:  memory,
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

	switch action, reason := tier2Decision(endpoint, inCI, reachable); action {
	case tier2Fail:
		t.Fatal(reason)

		// Unreachable: t.Fatal stops the test, and Go still needs a return.
		return substrate{}

	case tier2InProcess:
		t.Log(reason)

		return inProcessTier2(t)

	default:
		return remoteTier2(t, endpoint)
	}
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

	client, deleter, _ := adapterAndClient(t, server.URL())
	tier := substrate{
		name:     "tier-2 (fake-gcs-server, in-process)",
		bucket:   bucket,
		client:   client,
		deleter:  deleter,
		endpoint: server.URL(),
	}
	tier.can = probeCapabilities(t, tier)

	return tier
}

func remoteTier2(t *testing.T, endpoint string) substrate {
	t.Helper()

	bucket := uniqueBucket("tier-2")

	// The bucket is created explicitly. fake-gcs-server does NOT create one on
	// first write -- an upload to an unknown bucket is a 404, measured -- and
	// the deployed service is shared, so each run takes its own bucket rather
	// than colliding with whatever the last one left behind.
	client, deleter, storageClient := adapterAndClient(t, endpoint)
	if err := storageClient.Bucket(bucket).Create(context.Background(), conformanceProject, nil); err != nil {
		t.Fatalf("creating the conformance bucket %s at %s: %v.\n\n"+
			"The endpoint answered the reachability probe, so this is the emulator refusing a "+
			"bucket insert rather than an unreachable service.", bucket, endpoint, err)
	}
	t.Cleanup(func() { deleteBucket(t, client, deleter, storageClient, bucket) })

	tier := substrate{
		name:     "tier-2 (fake-gcs-server at " + endpoint + ")",
		bucket:   bucket,
		client:   client,
		deleter:  deleter,
		endpoint: endpoint,
	}
	tier.can = probeCapabilities(t, tier)

	return tier
}

// adapterFor builds the production seam: hangar's own storage client against
// the emulator endpoint, then hangar/gcs's exported object adapter over it.
// Nothing in this file constructs a *storage.Client any other way, so a break
// in that seam is a failure here rather than a surprise in the daemon.
func adapterFor(t *testing.T, endpoint string) (objectstore.Client, objectstore.DeleteClient) {
	t.Helper()

	client, deleter, _ := adapterAndClient(t, endpoint)

	return client, deleter
}

// conformanceProject is the project id a bucket insert needs. The emulator does
// not check it and real GCS is never reached from here.
const conformanceProject = "hangar-conformance"

// adapterAndClient also hands back the storage client, which the remote tier
// needs for the one operation the object seam deliberately does not offer:
// creating a bucket. No role has bucket-policy permission, so bucket creation
// cannot be behind the seam -- it is test setup against an emulator, and saying
// so here is better than widening objectstore.Client to make a test convenient.
func adapterAndClient(t *testing.T, endpoint string) (objectstore.Client, objectstore.DeleteClient, *storage.Client) {
	t.Helper()

	client, closeClient, err := hangargcs.NewObjectClient(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("adapting the object seam to %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = closeClient() })

	deleter, closeDeleter, err := gcsdelete.NewDeleteClient(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("adapting the delete capability: %v", err)
	}
	t.Cleanup(func() { _ = closeDeleter() })

	return client, deleter, emulatorClient(t, endpoint)
}

// emulatorClient is the bucket-creating client, and it is built here with the
// SDK rather than through hangar/gcs on purpose.
//
// hangar/gcs hands out no *storage.Client at all any more -- that is the whole
// of R2-F1 -- so a harness that needs one for bucket setup builds its own. The
// endpoint convention is still shared, through NormalizeStorageEndpoint, which
// is a string in and a string out and confers nothing.
func emulatorClient(t *testing.T, endpoint string) *storage.Client {
	t.Helper()

	normalized, err := hangargcs.NormalizeStorageEndpoint(endpoint)
	if err != nil {
		t.Fatalf("normalising %s: %v", endpoint, err)
	}
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(normalized),
		option.WithoutAuthentication(), storage.WithJSONReads())
	if err != nil {
		t.Fatalf("building a bucket-creating client for %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// deleteBucket clears a run's bucket off the shared emulator.
//
// It is best-effort and never fails the test: the suite's assertions are about
// what the plane does, and a leftover bucket on a fake is an untidiness rather
// than a wrong answer. Each run takes a fresh name, so a failure here cannot
// affect the next one.
func deleteBucket(t *testing.T, client objectstore.Client, deleter objectstore.DeleteClient, storageClient *storage.Client, bucket string) {
	t.Helper()

	ctx := context.Background()
	for after := ""; ; {
		page, err := client.List(ctx, bucket, objectstore.ListRequest{PageSize: 200, After: after})
		if err != nil {
			return
		}
		for _, object := range page.Objects {
			_ = deleter.DeleteExact(ctx, bucket, object.Key, object.Generation)
		}
		if page.Done || page.LastKey == "" {
			break
		}
		after = page.LastKey
	}
	_ = storageClient.Bucket(bucket).Delete(ctx)
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

// eachSubstrate runs one case against memory, GCS emulator, and persistent disk.
//
// The tier name is in the subtest name so a failure says which substrate
// refused, which is the difference between "the code is wrong" and "this fake
// cannot express that".
func eachSubstrate(t *testing.T, run func(*testing.T, substrate)) {
	t.Helper()

	for _, build := range []func(*testing.T) substrate{tier1, tier2, diskSubstrate} {
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

// diskSubstrate runs the same publication, inventory and reclamation contract
// against a real filesystem and transactional index, without a storage fake.
func diskSubstrate(t *testing.T) substrate {
	t.Helper()
	root := t.TempDir()
	const id = "conformance-store"
	if err := disk.Initialize(root, id); err != nil {
		t.Fatal(err)
	}
	store, err := disk.Open(root, id, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	tier := substrate{name: "disk (persistent filesystem)", bucket: "output", client: store, deleter: store}
	tier.can = probeCapabilities(t, tier)
	if !tier.can.EnforcesDeletePreconditions || !tier.can.Paginates {
		t.Fatal("disk does not satisfy the immutable object contract")
	}
	return tier
}
