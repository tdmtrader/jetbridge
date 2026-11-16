package jetbridge

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

const (
	addressNode   = "producer-node"
	addressNodeIP = "192.0.2.10"
	addressPodIP  = "192.0.2.20"
)

// Only the pod address is reachable. Use the wire client's transport seam to
// reach an in-memory httptest daemon on its fixed port, without a real dial.
// HEAD deliberately fails so a peer probe cannot hide a broken primary read.
type daemonAddressFixture struct {
	backend    *DaemonSetBackend
	clientset  *fake.Clientset
	slice      *discoveryv1.EndpointSlice
	nodeCalls  atomic.Int32
	registered atomic.Int32
	mirrored   atomic.Int32
	deleted    atomic.Int32
	allowNode  bool
}

func newDaemonAddressFixture(t *testing.T) *daemonAddressFixture {
	t.Helper()
	f := &daemonAddressFixture{}
	var answer bytes.Buffer
	tw := tar.NewWriter(&answer)
	if err := tw.WriteHeader(&tar.Header{Name: answerFile, Mode: 0600, Size: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	daemon := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/register":
			f.registered.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/mirror":
			f.mirrored.Add(1)
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodDelete && r.URL.Path == "/artifacts/steps/step":
			f.deleted.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && (r.URL.Path == "/artifacts/output-volume" || r.URL.Path == "/artifacts/steps/step/answer"):
			w.Write(answer.Bytes())
		case r.Method == http.MethodGet && r.URL.Path == "/capture-held/steps/step":
			io.WriteString(w, `{"class":"unmanaged","handle":"step"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonNamespace = "daemon-ns"
	cfg.OutputPlaneEnabled = true
	ready := true
	node := addressNode
	f.slice = &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "daemon-addresses", Namespace: cfg.DaemonNamespace(),
			Labels: map[string]string{discoveryv1.LabelServiceName: cfg.ArtifactDaemonService}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{NodeName: &node, Addresses: []string{addressPodIP},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
	f.clientset = fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: addressNode, UID: "node-uid"},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: addressNodeIP}}},
	}, f.slice)
	wire := plainWire(t, cfg.ArtifactDaemonPort, daemonAddressTransport{fixture: f, daemon: daemon})
	dc := NewDaemonClientFromConfig(lagertest.NewTestLogger("daemon-address"), f.clientset, cfg)
	dc.wire = wire
	f.backend = NewDaemonSetBackend(cfg, NewArtifactLocator(), NewNodeIPResolver(f.clientset), dc)
	f.backend.wire = wire
	return f
}

type daemonAddressTransport struct {
	fixture *daemonAddressFixture
	daemon  http.Handler
}

func (rt daemonAddressTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	switch r.URL.Host {
	case addressNodeIP + ":7780", addressPodIP + ":7780", "192.0.2.21:7780":
	default:
		return nil, errors.New("fixture: no daemon at this address")
	}
	if r.URL.Hostname() == addressNodeIP {
		rt.fixture.nodeCalls.Add(1)
		if !rt.fixture.allowNode {
			return nil, errors.New("fixture: node InternalIP is unreachable")
		}
	}
	response := httptest.NewRecorder()
	rt.daemon.ServeHTTP(response, r)
	result := response.Result()
	result.Request = r
	return result, nil
}

func (f *daemonAddressFixture) record() {
	f.backend.RecordOutputs(context.Background(), "step", addressNode,
		[]*Volume{NewStubVolume("output-volume", "worker", "/tmp/output")},
		runtime.ContainerSpec{Type: db.ContainerTypeTask, Outputs: runtime.OutputPaths{"result": "/tmp/output"}})
}

func (f *daemonAddressFixture) volume() *DaemonSetVolume {
	f.backend.artifactLocator.RecordStepOutput("step", "output-volume", addressNode, "step/result")
	vol := f.backend.WrapVolumeForLookup(context.Background(), "output-volume", "output-volume", "worker", nil).(*DaemonSetVolume)
	vol.wire = f.backend.wire
	return vol
}

func TestWebDaemonAddress_PrefersPodIP(t *testing.T) {
	t.Run("registration-and-mirror", func(t *testing.T) {
		f := newDaemonAddressFixture(t)
		f.record()
		if got := f.nodeCalls.Load(); got != 0 {
			t.Errorf("registration/mirror dialled the unreachable node InternalIP %d times", got)
		}
		if f.registered.Load() != 1 || f.mirrored.Load() != 1 {
			t.Errorf("pod daemon got registrations=%d mirrors=%d, want 1 each", f.registered.Load(), f.mirrored.Load())
		}
	})
	t.Run("stream-out", func(t *testing.T) {
		f := newDaemonAddressFixture(t)
		body, err := f.volume().StreamOut(context.Background(), ".", nil)
		if err != nil {
			t.Fatalf("StreamOut: %v (node InternalIP attempts=%d)", err, f.nodeCalls.Load())
		}
		defer body.Close()
		data, err := io.ReadAll(body)
		if err != nil || !bytes.Contains(data, []byte("payload")) {
			t.Fatalf("pod daemon did not serve the output: %v", err)
		}
		if f.nodeCalls.Load() != 0 {
			t.Fatal("StreamOut dialled the node InternalIP")
		}
	})
}

func TestWebDaemonAddress_FallsBackToNodeIP(t *testing.T) {
	for _, mode := range []string{"no-endpoints", "other-node", "not-ready", "list-error", "no-service", "no-client"} {
		t.Run(mode, func(t *testing.T) {
			f := newDaemonAddressFixture(t)
			f.allowNode = true
			switch mode {
			case "no-endpoints":
				f.slice.Endpoints = nil
			case "other-node":
				node := "other-node"
				f.slice.Endpoints[0].NodeName = &node
			case "not-ready":
				ready := false
				f.slice.Endpoints[0].Conditions.Ready = &ready
			case "list-error":
				f.clientset.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, errors.New("fixture: discovery unavailable")
				})
			case "no-service":
				f.backend.daemonClient.service = ""
			case "no-client":
				f.backend.daemonClient = nil
			}
			if _, err := f.clientset.DiscoveryV1().EndpointSlices(f.slice.Namespace).Update(context.Background(), f.slice, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			f.record()
			body, err := f.volume().StreamOut(context.Background(), ".", nil)
			if err != nil {
				t.Fatalf("fallback read: %v", err)
			}
			body.Close()
			if f.registered.Load() != 1 || f.nodeCalls.Load() < 2 {
				t.Fatalf("fallback did not register and read through the node InternalIP: registrations=%d node calls=%d", f.registered.Load(), f.nodeCalls.Load())
			}
		})
	}
}

// The generated fake drops List's context, so stall an in-memory API transport instead.
type stalledDaemonDiscovery struct {
	lists int
}

func (rt *stalledDaemonDiscovery) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.lists++
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func (f *daemonAddressFixture) stallDiscovery(t *testing.T) *stalledDaemonDiscovery {
	t.Helper()
	transport := &stalledDaemonDiscovery{}
	client, err := kubernetes.NewForConfigAndClient(&rest.Config{Host: "http://discovery.invalid"},
		&http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	f.backend.daemonClient.clientset = client
	return transport
}

func TestWebDaemonAddress_StalledDiscoveryStillRegistersOnNode(t *testing.T) {
	f := newDaemonAddressFixture(t)
	f.allowNode = true
	if got, err := f.backend.nodeIPResolver.Resolve(context.Background(), addressNode); err != nil || got != addressNodeIP {
		t.Fatalf("warming node IP cache: got=%q err=%v", got, err)
	}
	discovery := f.stallDiscovery(t)

	f.backend.registerAlias(context.Background(), addressNode, "output-volume", "steps/step/result", true)

	if discovery.lists > 1 {
		t.Fatalf("stalled EndpointSlice LIST calls=%d, want at most 1", discovery.lists)
	}
	if f.registered.Load() != 1 || f.nodeCalls.Load() != 1 {
		t.Fatalf("stalled discovery prevented node-IP registration: registrations=%d node calls=%d, want 1 each",
			f.registered.Load(), f.nodeCalls.Load())
	}
}

func TestWebDaemonAddress_StalledDiscoveryReservesCallerDeadline(t *testing.T) {
	f := newDaemonAddressFixture(t)
	f.allowNode = true
	if _, err := f.backend.nodeIPResolver.Resolve(context.Background(), addressNode); err != nil {
		t.Fatal(err)
	}
	f.stallDiscovery(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	host, err := f.volume().daemonHost(ctx)
	if err != nil || host != addressNodeIP {
		t.Fatalf("fallback address: got=%q err=%v", host, err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("discovery consumed the caller's deadline: %v", err)
	}
	if err := f.backend.wire.Register(ctx, host, artifactwire.RegisterRequest{
		Key: "output-volume", LocalPath: "steps/step/result", ReadOnly: true,
	}); err != nil {
		t.Fatalf("fallback registration: %v", err)
	}
}

func TestWebDaemonAddress_OtherWebCallsUsePodIP(t *testing.T) {
	t.Run("capture-class", func(t *testing.T) {
		f := newDaemonAddressFixture(t)
		if class, err := f.backend.CaptureClass(context.Background(), "step", addressNode); err != nil || class != "unmanaged" {
			t.Fatalf("CaptureClass: class=%q err=%v", class, err)
		}
	})
	t.Run("job-answer", func(t *testing.T) {
		f := newDaemonAddressFixture(t)
		var stdout bytes.Buffer
		p := &jobProcess{storageBackend: f.backend, container: &Container{handle: "step"}, processIO: runtime.ProcessIO{Stdout: &stdout}}
		if err := p.readAnswer(context.Background(), addressNode); err != nil || stdout.String() != "payload" {
			t.Fatalf("readAnswer: stdout=%q err=%v", stdout.String(), err)
		}
	})
	t.Run("reaper", func(t *testing.T) {
		f := newDaemonAddressFixture(t)
		logger := lagertest.NewTestLogger("reaper")
		r := NewReaper(logger, f.clientset, f.backend.config, nil, nil)
		r.SetArtifactLocator(f.backend.artifactLocator)
		r.wire = f.backend.wire
		if !r.deleteStepDir(context.Background(), logger, "step", addressNode) || f.deleted.Load() != 1 {
			t.Fatal("reaper did not delete through the pod daemon")
		}
	})
	t.Run("output-controls-and-source", func(t *testing.T) {
		f := newDaemonAddressFixture(t)
		controls := NewOutputControls(f.backend.config, f.backend.nodeIPResolver, nil).(*nodeOutputControls)
		source := NewOutputSource(f.clientset, f.backend.config, nil)
		for _, get := range []func() (*OutputControlClient, error){
			func() (*OutputControlClient, error) { return controls.clientForNode(context.Background(), addressNode) },
			func() (*OutputControlClient, error) {
				return source.exactClient(context.Background(), addressNode, "node-uid")
			},
		} {
			client, err := get()
			if err != nil {
				t.Fatal(err)
			}
			// Execution controls retain this endpoint, so it must survive daemon pod replacement.
			if want := fmt.Sprintf("%s://%s:7780", outputPlaneURLScheme(), addressNodeIP); client.endpoint != want {
				t.Errorf("output control client selected %q, want stable node address %q", client.endpoint, want)
			}
		}
	})
}

func TestWebDaemonAddress_RefreshesPodIP(t *testing.T) {
	f := newDaemonAddressFixture(t)
	v := f.volume()
	for _, want := range []string{addressPodIP, "192.0.2.21"} {
		f.slice.Endpoints[0].Addresses = []string{want}
		if _, err := f.clientset.DiscoveryV1().EndpointSlices(f.slice.Namespace).Update(context.Background(), f.slice, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if got, err := v.daemonHost(context.Background()); err != nil || got != want {
			t.Fatalf("daemonHost did not refresh the pod address: got=%q want=%q err=%v", got, want, err)
		}
	}
}

func TestWebDaemonAddress_RejectsIPAsNodeName(t *testing.T) {
	f := newDaemonAddressFixture(t)
	v := f.volume()
	v.sourceNode = addressPodIP
	f.slice.Endpoints[0].NodeName = &v.sourceNode
	if _, err := f.clientset.DiscoveryV1().EndpointSlices(f.slice.Namespace).Update(context.Background(), f.slice, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.daemonHost(context.Background()); !errors.Is(err, ErrNodeNameIsIP) {
		t.Fatalf("expected ErrNodeNameIsIP, got %v", err)
	}
}
