package jetbridge

// What the Reaper finds when a step is destroyed.
//
// The locator is keyed by VOLUME handle -- RecordOutputs files a step's outputs
// under "<h>-dir", "<h>-input-N" and "<h>-output-<name>", and a resource cache
// under its own key -- but the Reaper is handed CONTAINER handles. It used to
// look the bare container handle up, a key no production writer ever records,
// so it never located anything: no DELETE reached a daemon and no entry was
// ever retired. These specs record through the production writers and reap
// through the production sweep, so a key-shape mismatch between the two
// cannot hide behind a hand-written key again.
//
// The daemon is an httptest server answering status codes, as in
// reaper_capture_test.go: what the daemon does with a DELETE is pinned by
// cmd/artifact-daemon's own suite; what is pinned here is what the ATC sends.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// reapFixture is one node, one daemon on it, and the production backend and
// Reaper wired to one shared locator, the way atccmd wires them.
type reapFixture struct {
	backend *DaemonSetBackend
	reaper  *Reaper
	locator *ArtifactLocator

	mu      sync.Mutex
	deleted []string
}

func newReapFixture(t *testing.T) *reapFixture {
	t.Helper()
	f := &reapFixture{}

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			f.mu.Lock()
			f.deleted = append(f.deleted, r.URL.Path)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/register":
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/mirror":
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(daemon.Close)

	parsed, err := url.Parse(daemon.URL)
	if err != nil {
		t.Fatalf("parsing the daemon URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("the daemon has no port: %v", err)
	}
	host := parsed.Hostname()

	clientset := fake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: host},
			}},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "artifact-daemon-fixture",
				Namespace: "test-ns",
				Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
			},
			Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
		},
	)

	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = port

	f.locator = NewArtifactLocator()
	f.backend = NewDaemonSetBackend(cfg, f.locator, NewNodeIPResolver(clientset),
		NewDaemonClient(lagertest.NewTestLogger("daemon-client"), clientset, "test-ns", "artifact-daemon", port, nil))
	f.reaper = NewReaper(lagertest.NewTestLogger("reaper"), clientset, cfg, nil, nil)
	f.reaper.SetArtifactLocator(f.locator)

	return f
}

// run records a step's outputs the way Process does once its pod finished on
// node-1, and returns the locator keys the step's volumes were given.
func (f *reapFixture) run(t *testing.T, handle string, spec runtime.ContainerSpec) []string {
	t.Helper()
	_, volumes := buildVolumeMounts("k8s-test-ns", "test-ns", nil, handle, spec)
	f.backend.RecordOutputs(context.Background(), handle, "node-1", volumes, spec)

	var keys []string
	for _, vol := range volumes {
		if _, found := f.locator.LocateNode(ArtifactKey(vol.Handle())); found {
			keys = append(keys, vol.Handle())
		}
	}
	if len(keys) == 0 {
		t.Fatalf("recording %s located none of its volumes; the fixture records nothing", handle)
	}
	return keys
}

func (f *reapFixture) reap(handles ...string) {
	f.reaper.cleanupDaemonSetArtifacts(context.Background(), lagertest.NewTestLogger("cleanup"), handles)
}

func (f *reapFixture) deletes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func taskSpec() runtime.ContainerSpec {
	return runtime.ContainerSpec{
		Type: db.ContainerTypeTask,
		Dir:  "/tmp/build/abc",
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/build/abc/repo"},
		},
		// "repo" is also an input, so it is recorded under the input volume's
		// handle; "result" gets an output volume of its own.
		Outputs: runtime.OutputPaths{
			"repo":   "/tmp/build/abc/repo",
			"result": "/tmp/build/abc/result",
		},
	}
}

func getSpec() runtime.ContainerSpec {
	return runtime.ContainerSpec{Type: db.ContainerTypeGet, Dir: "/tmp/build/get"}
}

func TestTheReaperDeletesAndForgetsEveryArtifactADestroyedStepRecorded(t *testing.T) {
	f := newReapFixture(t)

	const destroyed = "build-7-task-3f2a"
	// A live container whose handle EXTENDS the destroyed one: its keys
	// ("build-7-task-3f2a-retry-dir") start with "build-7-task-3f2a-", so a
	// prefix match on the destroyed handle would reap a step still running.
	const neighbour = "build-7-task-3f2a-retry"

	destroyedKeys := f.run(t, destroyed, taskSpec())
	neighbourKeys := f.run(t, neighbour, getSpec())
	if len(destroyedKeys) != 2 {
		t.Fatalf("the task recorded %v; expected its input-backed and output volumes", destroyedKeys)
	}

	f.reap(destroyed)

	want := "/artifacts/steps/" + destroyed
	if got := f.deletes(); len(got) != 1 || got[0] != want {
		t.Fatalf("the daemon was asked to delete %v, want exactly [%s]", got, want)
	}
	for _, key := range destroyedKeys {
		if _, found := f.locator.LocateNode(key); found {
			t.Errorf("the locator still names %s after its step's directory was deleted; "+
				"the map grows until the web restarts", key)
		}
	}
	for _, key := range neighbourKeys {
		if _, found := f.locator.LocateNode(key); !found {
			t.Errorf("reaping %s forgot %s, which belongs to %s", destroyed, key, neighbour)
		}
	}
}

// A resource cache is a daemon alias onto its get step's directory
// ("rc-<id>" -> steps/<h>/dir), and it must outlive that get's container: the
// next build's get is a cache hit precisely because the container is gone and
// the bytes are not. Deleting steps/<h> when the get is reaped would leave the
// alias resolving into nothing.
func TestTheReaperNeverDeletesAStepDirectoryAResourceCacheResolvesInto(t *testing.T) {
	f := newReapFixture(t)

	const cachedGet = "get-cached-9c1d"
	const uncachedGet = "get-uncached-51ab"

	cachedKeys := f.run(t, cachedGet, getSpec())
	f.run(t, uncachedGet, getSpec())
	if err := f.backend.RegisterResourceCache(context.Background(), "rc-7", "", cachedGet+"-dir", "node-1"); err != nil {
		t.Fatalf("RegisterResourceCache: %v", err)
	}

	f.reap(cachedGet, uncachedGet)

	got := f.deletes()
	for _, path := range got {
		if path == "/artifacts/steps/"+cachedGet {
			t.Fatalf("the Reaper deleted steps/%s, which resource cache rc-7 resolves into", cachedGet)
		}
	}
	if len(got) != 1 || got[0] != "/artifacts/steps/"+uncachedGet {
		t.Errorf("the daemon was asked to delete %v, want exactly the uncached get's directory", got)
	}

	if node, found := f.locator.LocateNode("rc-7"); !found || node != "node-1" {
		t.Errorf("the resource cache's locator entry did not survive its get being reaped: %q, %v", node, found)
	}
	// The get's own volume keys name a container that is gone; the cache is
	// reached by its own key, so keeping them only grows the map.
	for _, key := range cachedKeys {
		if _, found := f.locator.LocateNode(key); found {
			t.Errorf("the locator still names %s after its container was reaped", key)
		}
	}
}
