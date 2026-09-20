package steps

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

type ArtifactLookup struct {
	volume                            runtime.Volume
	association                       *db.UsedWorkerResourceCache
	key, location, discovery, address string
	recordedNode                      string
	expected, received                []byte
	lookupRequests                    []daemonWireRequest
	wire                              *daemonWireObservation
	err, readErr                      error
}

func artifactLookupContractDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[ArtifactCluster, ArtifactLookup](
			"the worker looks up {string} with {string} location and {string} discovery",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactLookup, error) {
				key, _ := p.GetString(0)
				location, _ := p.GetString(1)
				discovery, _ := p.GetString(2)
				return observeArtifactLookup(in, key, location, discovery)
			},
		),
		brine.DefineCheck[ArtifactLookup](
			"the lookup has database identity {string} and delivers {string}",
			func(in ArtifactLookup, p brine.Params, _ *brine.Recorder) error {
				identity, _ := p.GetString(0)
				content, _ := p.GetString(1)
				return checkArtifactLookup(in, identity, content)
			},
		),
	}
}

func observeArtifactLookup(in ArtifactCluster, key, location, discovery string) (ArtifactLookup, error) {
	out := ArtifactLookup{key: key, location: location, discovery: discovery, recordedNode: in.NodeName,
		address: net.JoinHostPort(in.Node.host, strconv.Itoa(in.Node.port))}
	switch location {
	case "recorded":
		in.Locator.Record(key, in.NodeName, key)
	case "unrecorded":
	default:
		return out, fmt.Errorf("unknown location state %q", location)
	}
	switch discovery {
	case "published":
	case "unpublished":
		if err := in.unpublishDaemons(); err != nil {
			return out, err
		}
	default:
		return out, fmt.Errorf("unknown discovery state %q", discovery)
	}
	// Independent real-daemon raw-byte oracle, before observing the SUT.
	var err error
	out.expected, err = in.Node.fetchArtifact(in.Ctx, "/artifacts/"+key)
	if err != nil {
		return out, err
	}
	cache, err := oneResourceCache(in)
	if err != nil {
		return out, err
	}
	out.wire, err = observeDaemonTraffic(map[string]bool{out.address: true}, func() {
		client := jetbridge.NewDaemonClient(lagertest.NewTestLogger("brine-lookup"),
			in.Clientset, in.Namespace, in.daemonService(), in.Node.port, nil)
		in.Worker.SetDaemonClient(client)
		out.volume, out.err = lookupVolume(in, key)
	})
	if err != nil {
		return out, err
	}
	out.lookupRequests, err = out.wire.capturedRequests()
	if err != nil {
		return out, err
	}
	if out.err != nil || out.volume == nil {
		return out, nil
	}
	out.association, out.err = out.volume.InitializeResourceCache(in.Ctx, cache)
	reader, readErr := out.volume.StreamOut(in.Ctx, ".", nil)
	out.readErr = readErr
	if reader != nil {
		out.received, err = io.ReadAll(reader)
		out.readErr = errors.Join(out.readErr, err, reader.Close())
	}
	if in.live != nil && location == "recorded" {
		out.readErr = errors.Join(out.readErr, in.nodeReads.requireNodeRead(in.NodeName))
	}
	return out, nil
}

func checkArtifactLookup(in ArtifactLookup, identity, content string) error {
	if identity != "yes" && identity != "no" {
		return fmt.Errorf("unknown identity expectation %q", identity)
	}
	if in.err != nil {
		return fmt.Errorf("lookup or cache initialization: %w", in.err)
	}
	volume, ok := in.volume.(*jetbridge.DaemonSetVolume)
	if !ok || volume == nil {
		return fmt.Errorf("lookup must return a daemon volume, got %T", in.volume)
	}
	if (in.association != nil) != (identity == "yes") {
		return fmt.Errorf("database identity: want %s, association=%+v", identity, in.association)
	}
	// Which node and address the volume bound to is a private detail of
	// DaemonSetVolume; the Go unit test in storage_daemonset_test.go pins
	// it. Here the binding is proved by what it does: the lookup requests
	// the daemon saw (below) and, on the live tier, the node that served
	// the read.
	if in.location == "recorded" && in.recordedNode == "" {
		return fmt.Errorf("recorded lookup has no expected node identity")
	}
	var wantLookup []string
	if identity == "no" {
		wantLookup = []string{"HEAD /resource-caches/" + in.key}
	}
	var lookup []string
	for _, r := range in.lookupRequests {
		lookup = append(lookup, r.Method+" "+r.Path)
	}
	if !reflect.DeepEqual(lookup, wantLookup) {
		return fmt.Errorf("lookup requests: got %v, want %v", lookup, wantLookup)
	}
	requests, err := in.wire.requests()
	if err != nil {
		return err
	}
	if content == "unavailable" {
		if in.readErr == nil || !strings.Contains(in.readErr.Error(), "not found") || len(in.received) != 0 || len(requests) != 0 {
			return fmt.Errorf("undiscovered lookup must stay unbound and unreadable: error=%v requests=%v bytes=%d", in.readErr, requests, len(in.received))
		}
		return nil
	}
	if in.readErr != nil {
		return in.readErr
	}
	if len(in.expected) == 0 || !bytes.Equal(in.received, in.expected) {
		return fmt.Errorf("lookup did not preserve daemon's exact raw bytes")
	}
	got, err := oneFileInTar(in.received)
	if err != nil {
		return err
	}
	if got != content {
		return fmt.Errorf("artifact content: got %q want %q", got, content)
	}
	wantRequests := append([]string(nil), wantLookup...)
	if in.location == "recorded" || identity == "no" {
		wantRequests = append(wantRequests, "GET /artifacts/"+in.key)
	} else {
		wantRequests = append(wantRequests, "HEAD /artifacts/steps/"+in.key, "GET /artifacts/steps/"+in.key)
	}
	if len(requests) != 1 || !reflect.DeepEqual(requests[in.address], wantRequests) {
		return fmt.Errorf("lookup delivery requests: got %v, want %v", requests, wantRequests)
	}
	return nil
}
