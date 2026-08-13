package jetbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// warmSpy is a daemon that records whether it was asked to warm, and can be told
// whether it holds the cache locally and whether it advertises a durable tier.
type warmSpy struct {
	local          bool
	durableCapable bool
	restoreOK      bool

	heads    atomic.Int64
	restores atomic.Int64
}

func (d *warmSpy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/resource-caches/"):
			d.heads.Add(1)
			if d.durableCapable {
				w.Header().Set("X-Durable-Tier", "enabled")
			}
			if d.local {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodPost && r.URL.Path == "/durable/restore":
			d.restores.Add(1)
			if !d.restoreOK {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("X-Artifact-Tier", "durable")
			w.WriteHeader(http.StatusCreated)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func backendFor(t *testing.T, d *warmSpy) *DaemonSetBackend {
	t.Helper()

	ts := httptest.NewServer(d.handler())
	t.Cleanup(ts.Close)

	addr := ts.Listener.Addr().String()
	host := addr[:strings.LastIndex(addr, ":")]
	port, err := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	if err != nil {
		t.Fatalf("parse port from %q: %v", addr, err)
	}

	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-xyz",
			Namespace: "cicd",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses: []string{host},
			NodeName:  ptr("node-a"),
		}},
	})

	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = port

	b := NewDaemonSetBackend(cfg, NewArtifactLocator(), nil)
	b.SetDaemonClient(NewDaemonClient(lagertest.NewTestLogger("test"), clientset, "cicd", "artifact-daemon", port, nil))

	return b
}

func ptr[T any](v T) *T { return &v }

// A cache that is already on a node must never trigger a warm. The local copy is
// the fast path; going to object storage for something already on disk would be
// the whole cost of the tier with none of its benefit.
func TestFindResourceCacheDoesNotWarmOnALocalHit(t *testing.T) {
	d := &warmSpy{local: true, durableCapable: true, restoreOK: true}
	b := backendFor(t, d)

	vol, found := b.FindResourceCache(context.Background(), "rc-42", "rc-content", "worker-1")
	if !found {
		t.Fatal("expected a local hit")
	}
	if vol == nil {
		t.Fatal("hit returned a nil volume")
	}
	if got := d.restores.Load(); got != 0 {
		t.Errorf("a local hit issued %d restore calls, want 0", got)
	}
}

// Silence is the protocol: no content key means the ATC is not offering this
// cache to the durable tier, so no request may be made at all.
func TestFindResourceCacheDoesNotWarmWithoutAContentKey(t *testing.T) {
	d := &warmSpy{local: false, durableCapable: true, restoreOK: true}
	b := backendFor(t, d)

	if _, found := b.FindResourceCache(context.Background(), "rc-42", "", "worker-1"); found {
		t.Error("reported a hit for a cache with no content key")
	}
	if got := d.restores.Load(); got != 0 {
		t.Errorf("a cache with no content key issued %d restore calls, want 0", got)
	}
}

// A daemon that predates the tier never advertises it, and the ATC must then
// issue zero requests to a route that does not exist — otherwise every cache
// miss during a rolling upgrade costs an extra failed round trip.
func TestFindResourceCacheDoesNotWarmAnUnadvertisedDaemon(t *testing.T) {
	d := &warmSpy{local: false, durableCapable: false, restoreOK: true}
	b := backendFor(t, d)

	if _, found := b.FindResourceCache(context.Background(), "rc-42", "rc-content", "worker-1"); found {
		t.Error("reported a hit from a daemon with no durable tier")
	}
	if got := d.restores.Load(); got != 0 {
		t.Errorf("issued %d restore calls to a daemon that never advertised a tier, want 0", got)
	}
}

// The payoff path: nothing local, a content key, a capable daemon.
func TestFindResourceCacheWarmsOnAMiss(t *testing.T) {
	d := &warmSpy{local: false, durableCapable: true, restoreOK: true}
	b := backendFor(t, d)

	vol, found := b.FindResourceCache(context.Background(), "rc-42", "rc-content", "worker-1")
	if !found {
		t.Fatal("expected the warm to produce a hit")
	}
	if vol == nil {
		t.Fatal("warm returned a nil volume")
	}
	if got := d.restores.Load(); got != 1 {
		t.Errorf("issued %d restore calls, want 1", got)
	}
}

// A failed warm must not be retried on every scheduler tick. attemptGet
// re-enters every GetResourceLockInterval while waiting for the resource lock,
// and a get step's own timeout does not bound the warm, so an unreachable bucket
// would otherwise cost a full warm timeout every few seconds indefinitely.
func TestFindResourceCacheSuppressesAfterAFailedWarm(t *testing.T) {
	d := &warmSpy{local: false, durableCapable: true, restoreOK: false}
	b := backendFor(t, d)

	for range 5 {
		if _, found := b.FindResourceCache(context.Background(), "rc-42", "rc-content", "worker-1"); found {
			t.Fatal("a failing store reported a hit")
		}
	}

	if got := d.restores.Load(); got != 1 {
		t.Errorf("5 lookups after a failed warm issued %d restore calls, want 1", got)
	}
}

// The durable flag must survive the trip to the daemon, both ways.
//
// This is the seam the whole content-key change turns on: a cache with no
// content key is addressed by its row id, a Postgres sequence, and filing that
// in permanent storage is the exact defect the key exists to prevent. An ATC
// that always said "durable" would reintroduce it while every daemon-side test
// still passed, because the daemon only ever sees the bool it is handed.
func TestRegisterAliasSendsTheDurableFlagAsGiven(t *testing.T) {
	for _, durable := range []bool{true, false} {
		var got struct {
			Key       string `json:"key"`
			LocalPath string `json:"local_path"`
			Durable   bool   `json:"durable"`
		}
		var seen atomic.Bool

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/register" {
				_ = json.NewDecoder(r.Body).Decode(&got)
				seen.Store(true)
				w.WriteHeader(http.StatusCreated)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		addr := ts.Listener.Addr().String()
		host := addr[:strings.LastIndex(addr, ":")]
		port, err := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
		if err != nil {
			t.Fatalf("parse port: %v", err)
		}

		clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "artifact-daemon-xyz",
				Namespace: "cicd",
				Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
			},
			Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}, NodeName: ptr("node-a")}},
		})

		client := NewDaemonClient(lagertest.NewTestLogger("test"), clientset, "cicd", "artifact-daemon", port, nil)
		if err := client.RegisterAlias(context.Background(), "rc-42", "/some/path", durable); err != nil {
			t.Fatalf("RegisterAlias(durable=%v): %v", durable, err)
		}

		if !seen.Load() {
			t.Fatal("the daemon never received a register call")
		}
		if got.Durable != durable {
			t.Errorf("sent durable=%v, daemon received %v", durable, got.Durable)
		}
	}
}

// Volumes bound from a probe or a warm must carry the daemonClient, exactly as
// lookup-wrapped ones do (TestDaemonSetBackend_WrapVolumeForLookup_SetsDaemonClient).
//
// NewDaemonSetVolumeFromIP leaves the field nil, and fetchArtifactWithPeerFallback
// returns the recorded source's error verbatim when it is nil — so an alias swept
// between the probe and the read becomes a bare 404 and a red build, while the
// bytes sit on another node that was never asked.
func TestFindResourceCacheBindsAVolumeThatCanPeerFallBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spy   *warmSpy
		phase string
	}{
		{"local hit", &warmSpy{local: true, durableCapable: true, restoreOK: true}, "probe"},
		{"after a warm", &warmSpy{local: false, durableCapable: true, restoreOK: true}, "warm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := backendFor(t, tc.spy)

			vol, found := b.FindResourceCache(context.Background(), "rc-42", "rc-content", "worker-1")
			if !found {
				t.Fatalf("expected a hit via %s", tc.phase)
			}

			dsv, ok := vol.(*DaemonSetVolume)
			if !ok {
				t.Fatalf("expected *DaemonSetVolume, got %T", vol)
			}
			if dsv.daemonClient == nil {
				t.Fatalf("volume bound via %s carries no daemonClient; peer fallback is dead", tc.phase)
			}
		})
	}
}

// An endpoint the API has marked not-ready is a pod that is terminating or
// failing its probe. Binding an artifact read to one is a read against a pod
// that is going away.
//
// EndpointSlice reports these alongside ready ones — that is what Conditions is
// for — and discovery previously flattened every address regardless, so a
// terminating pod could win the probe race.
func TestProbeSkipsEndpointsTheAPIMarkedNotReady(t *testing.T) {
	var hits atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	addr := ts.Listener.Addr().String()
	host := addr[:strings.LastIndex(addr, ":")]
	port, err := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	notReady := false
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-xyz",
			Namespace: "cicd",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{host},
			NodeName:   ptr("node-a"),
			Conditions: discoveryv1.EndpointConditions{Ready: &notReady},
		}},
	})

	client := NewDaemonClient(lagertest.NewTestLogger("test"), clientset, "cicd", "artifact-daemon", port, nil)

	if _, found := client.ProbeResourceCache(context.Background(), "rc-42"); found {
		t.Error("probe reported a hit from a not-ready pod")
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("probe sent %d requests to a not-ready pod, want 0", got)
	}
}

// ...and a ready endpoint in the same slice must still be used, so the filter
// cannot pass by excluding everything.
func TestProbeStillUsesReadyEndpoints(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	addr := ts.Listener.Addr().String()
	host := addr[:strings.LastIndex(addr, ":")]
	port, err := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-xyz",
			Namespace: "cicd",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{host},
			NodeName:   ptr("node-a"),
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	})

	client := NewDaemonClient(lagertest.NewTestLogger("test"), clientset, "cicd", "artifact-daemon", port, nil)

	if _, found := client.ProbeResourceCache(context.Background(), "rc-42"); !found {
		t.Error("probe ignored a ready pod that had the cache")
	}
}
