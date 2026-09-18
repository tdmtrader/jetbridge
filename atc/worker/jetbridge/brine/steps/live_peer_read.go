package steps

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const livePeerService = "artifact-peer"

type PeerReadOutcome struct {
	mode               string
	producer, peer     string
	expected, received []byte
	trace              *daemonWireObservation
	err                error
}

func LivePeerReadDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, PeerReadOutcome]("the artifact is read with producer {string} and peer {string}", func(_ LiveTaskPlan, p brine.Params, rec *brine.Recorder) (PeerReadOutcome, error) {
			state, ok := p.GetString(0)
			if !ok {
				return PeerReadOutcome{}, fmt.Errorf("expected producer state")
			}
			copy, ok := p.GetString(1)
			if !ok {
				return PeerReadOutcome{}, fmt.Errorf("expected peer copy state")
			}
			return readLivePeerArtifact(state, copy, rec)
		}),
		CheckThat[PeerReadOutcome]("artifact delivery preserves the exact bytes and peer request contract", checkLivePeerArtifact),
	}
}

// The producer uses the approved node-IP host port. The peer has a different
// pod IP and independent emptyDir, listening on that same port without a host
// port. Only the peer is selected by a real EndpointSlice controller.
func startLiveArtifactPeer(ctx context.Context, rec *brine.Recorder, d *liveArtifactDaemon) (string, error) {
	s := d.store
	size := *resource.NewQuantity(liveDaemonEphemeralBudget, resource.BinarySI)
	pod := s.pod(livePeerService, s.anchor.Spec.NodeName,
		[]corev1.Volume{{Name: "peer-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &size}}}},
		[]corev1.VolumeMount{{Name: "peer-data", MountPath: "/opt/brine"}},
	)
	pod.Labels["brine.dev/artifact-peer"] = s.cluster.Marker
	main := &pod.Spec.Containers[0]
	main.Command = []string{"sh", "-ec", "while [ ! -f /opt/brine/ready ]; do sleep 0.1; done; exec /opt/brine/artifact-daemon --port \"$1\" --storage-path /opt/brine/store --mirror-replicas=0", "start-owned-peer", fmt.Sprint(d.port)}
	main.Resources = *d.pod.Spec.Containers[0].Resources.DeepCopy()
	main.Env = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "96MiB"}}
	main.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: int32(d.port)}}
	pod, err := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	created := pod.DeepCopy()
	rec.RegisterDisposer(func() {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := deleteLiveStoragePod(clean, s, created); err != nil {
			panic(err)
		}
	})
	pod, err = awaitLiveStoragePod(ctx, s, pod)
	if err != nil {
		return "", err
	}
	if err := validatePodMounts(pod); err != nil {
		return "", err
	}
	source := jetbridge.NewDeferredVolume("daemon-source", "brine", s.executor, s.cluster.Namespace, "main", "/opt/brine")
	source.SetPodName(d.pod.Name)
	target := jetbridge.NewDeferredVolume("daemon-peer", "brine", s.executor, s.cluster.Namespace, "main", "/opt/brine")
	target.SetPodName(pod.Name)
	binary, err := source.StreamOut(ctx, "artifact-daemon", nil)
	if err != nil {
		return "", err
	}
	transferErr := target.StreamIn(ctx, ".", nil, 0, binary)
	if err := errors.Join(transferErr, binary.Close()); err != nil {
		return "", err
	}
	sum, err := s.exec(ctx, pod.Name, []string{"sha256sum", liveDaemonExecutable}, nil)
	if err != nil || strings.TrimSpace(sum) != d.binarySHA+"  "+liveDaemonExecutable {
		return "", fmt.Errorf("peer executable checksum %q: %v", sum, err)
	}
	if _, err := s.exec(ctx, pod.Name, []string{"touch", "/opt/brine/ready"}, nil); err != nil {
		return "", err
	}
	endpoint := "http://" + net.JoinHostPort(pod.Status.PodIP, fmt.Sprint(d.port))
	if pod.Status.PodIP == "" || pod.Status.PodIP == d.nodeIP {
		return "", fmt.Errorf("peer has no distinct pod address")
	}
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for {
		_, err = readDaemonHTTP(ctx, client, http.MethodGet, endpoint+"/healthz", nil, http.StatusOK)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("peer pod route unavailable: %w; %v", ctx.Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	service, err := s.cluster.Clientset.CoreV1().Services(s.cluster.Namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: livePeerService},
		Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Selector: map[string]string{"brine.dev/artifact-peer": s.cluster.Marker},
			Ports: []corev1.ServicePort{{Name: "http", Port: int32(d.port)}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	for {
		slices, err := s.cluster.Clientset.DiscoveryV1().EndpointSlices(s.cluster.Namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + service.Name})
		if err != nil {
			return "", err
		}
		count := 0
		for _, slice := range slices.Items {
			if slice.Labels[discoveryv1.LabelManagedBy] != "endpointslice-controller.k8s.io" {
				return "", fmt.Errorf("peer slice not controller-generated")
			}
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
					continue
				}
				if ep.TargetRef == nil || ep.TargetRef.UID != pod.UID || ep.NodeName == nil || *ep.NodeName != pod.Spec.NodeName || !reflect.DeepEqual(ep.Addresses, []string{pod.Status.PodIP}) {
					return "", fmt.Errorf("peer discovery does not match real pod UID/address/node")
				}
				count++
			}
		}
		if count == 1 {
			break
		}
		if count > 1 {
			return "", fmt.Errorf("unexpected extra peer endpoint")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Printf("verified independent peer pod %s/%s UID %s endpoint %s and controller-generated discovery\n", s.cluster.Namespace, pod.Name, pod.UID, endpoint)
	return endpoint, nil
}

func readLivePeerArtifact(state, copy string, rec *brine.Recorder) (PeerReadOutcome, error) {
	out := PeerReadOutcome{mode: state + "/" + copy}
	if out.mode != "running/present" && out.mode != "stopped/present" && out.mode != "stopped/absent" {
		return out, fmt.Errorf("unsupported peer read %q", out.mode)
	}
	ctx, cancel := context.WithTimeout(execLogger("live-peer-read"), 3*time.Minute)
	rec.RegisterDisposer(cancel)
	d, err := newLiveArtifactDaemon(ctx, rec)
	if err != nil {
		return out, err
	}
	peerURL, err := startLiveArtifactPeer(ctx, rec, d)
	if err != nil {
		return out, err
	}
	out.producer = strings.TrimPrefix(d.url(), "http://")
	out.peer = strings.TrimPrefix(peerURL, "http://")
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	// Seed through the daemon's actual extractor, with distinct producer/peer
	// bytes. There is no canned HTTP response or precomputed expected wire tar.
	for _, seed := range []struct{ url, text string }{{d.url(), "producer-content"}, {peerURL, "peer-served-content"}} {
		if seed.url == peerURL && copy == "absent" {
			continue
		}
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		content := seed.text + " " + d.store.cluster.Marker + "\n"
		if err := tw.WriteHeader(&tar.Header{Name: "output.txt", Mode: 0644, Size: int64(len(content))}); err != nil {
			return out, err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return out, err
		}
		if err := tw.Close(); err != nil {
			return out, err
		}
		if _, err := readDaemonHTTP(ctx, client, http.MethodPut, seed.url+"/stream-in/h/o", &archive, http.StatusCreated); err != nil {
			return out, err
		}
	}
	expectedURL := peerURL + "/artifacts/steps/h/o"
	if state == "running" {
		expectedURL = d.url() + "/artifacts/h/o"
	}
	if copy == "present" {
		out.expected, err = readDaemonHTTP(ctx, client, http.MethodGet, expectedURL, nil, http.StatusOK)
		if err != nil {
			return out, err
		}
		// Prove the real archive's member content independently of wire equality.
		tr := tar.NewReader(bytes.NewReader(out.expected))
		header, err := tr.Next()
		if err != nil {
			return out, err
		}
		payload, err := io.ReadAll(tr)
		if err != nil {
			return out, err
		}
		text := "peer-served-content"
		if state == "running" {
			text = "producer-content"
		}
		if header.Name != "output.txt" || string(payload) != text+" "+d.store.cluster.Marker+"\n" {
			return out, fmt.Errorf("daemon served wrong archive content")
		}
	} else {
		if _, err := readDaemonHTTP(ctx, client, http.MethodHead, expectedURL, nil, http.StatusNotFound); err != nil {
			return out, err
		}
	}
	if state == "stopped" {
		if err := d.close(ctx); err != nil {
			return out, err
		}
	}
	cfg := jetbridge.NewConfig(d.store.cluster.Namespace, "")
	cfg.ArtifactDaemonPort = int(d.port)
	var volume *jetbridge.DaemonSetVolume
	out.trace, err = observeDaemonConstruction(map[string]bool{out.producer: true, out.peer: true}, func() {
		volume = jetbridge.NewDaemonSetVolume("h/o", "h", "brine", nil, d.pod.Spec.NodeName, cfg, jetbridge.NewNodeIPResolver(d.store.cluster.Clientset))
		volume.SetDaemonClient(jetbridge.NewDaemonClient(lagerctx.FromContext(ctx), d.store.cluster.Clientset, d.store.cluster.Namespace, livePeerService, int(d.port), nil))
	})
	if err != nil {
		return out, err
	}
	reader, readErr := volume.StreamOut(ctx, ".", nil)
	out.err = readErr
	if reader != nil {
		out.received, err = io.ReadAll(reader)
		out.err = errors.Join(out.err, err, reader.Close())
	}
	return out, nil
}

func checkLivePeerArtifact(out PeerReadOutcome) error {
	requests, err := out.trace.requests()
	if err != nil {
		return err
	}
	fmt.Printf("observed actual daemon requests: %v\n", requests)
	if out.mode == "stopped/absent" {
		if out.err == nil || strings.Contains(out.err.Error(), "connection refused") {
			return fmt.Errorf("missing peer must report not-found, got %v", out.err)
		}
		if !reflect.DeepEqual(requests[out.peer], []string{"HEAD /artifacts/steps/h/o"}) {
			return fmt.Errorf("missing peer probe: got %v", requests[out.peer])
		}
	} else {
		if out.err != nil {
			return out.err
		}
		if len(out.expected) == 0 || !bytes.Equal(out.received, out.expected) {
			return fmt.Errorf("artifact raw bytes differ: received %d, expected %d", len(out.received), len(out.expected))
		}
		if out.mode == "running/present" {
			if !reflect.DeepEqual(requests[out.producer], []string{"GET /artifacts/h/o"}) || len(requests[out.peer]) != 0 {
				return fmt.Errorf("producer success must make one producer request and zero peer requests: %v", requests)
			}
		} else if !reflect.DeepEqual(requests[out.peer], []string{"HEAD /artifacts/steps/h/o", "GET /artifacts/steps/h/o"}) {
			return fmt.Errorf("peer delivery requests: %v", requests[out.peer])
		}
	}
	return nil
}
