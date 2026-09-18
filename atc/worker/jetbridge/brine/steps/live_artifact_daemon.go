package steps

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const liveDaemonExecutable = "/opt/brine/artifact-daemon"
const liveArtifactDaemonService = "brine-artifact-daemon"

// Both producer and peer share this fixed budget. Keep room for artifact bytes
// and container logs rather than accepting an ELF larger than its own pod limit.
const liveDaemonEphemeralBudget = 96 << 20
const maxLiveDaemonBinaryBytes = liveDaemonEphemeralBudget - (8 << 20)

// Current production binary in an owned real pod. Its explicitly approved
// host port exposes only the node InternalIP, without a tunnel or node changes.
type liveArtifactDaemon struct {
	store             *liveArtifactStore
	pod               *corev1.Pod
	port              uint16
	nodeIP, binarySHA string
}

func newLiveArtifactDaemon(ctx context.Context, rec *brine.Recorder) (*liveArtifactDaemon, error) {
	return startLiveArtifactDaemon(ctx, rec, false)
}

func startLiveArtifactDaemon(ctx context.Context, rec *brine.Recorder, mirror bool) (*liveArtifactDaemon, error) {
	started := time.Now()
	if os.Getenv("BRINE_ALLOW_HOSTPATH_TESTS") != "1" {
		return nil, fmt.Errorf("live artifact storage requires explicit BRINE_ALLOW_HOSTPATH_TESTS=1 approval")
	}
	if os.Getenv("BRINE_ALLOW_HOSTPORT_TESTS") != "1" {
		return nil, fmt.Errorf("live artifact daemon requires explicit BRINE_ALLOW_HOSTPORT_TESTS=1 approval")
	}
	port, err := strconv.Atoi(os.Getenv("BRINE_LIVE_ARTIFACT_DAEMON_PORT"))
	if err != nil || port < 49152 || port > 60999 {
		return nil, fmt.Errorf("BRINE_LIVE_ARTIFACT_DAEMON_PORT must select an approved unused TCP port in 49152..60999")
	}
	binary := os.Getenv("BRINE_LIVE_ARTIFACT_DAEMON_BINARY")
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("BRINE_LIVE_ARTIFACT_DAEMON_BINARY must name an absolute static Linux executable built from current source")
	}
	object, err := elf.Open(binary)
	if err != nil {
		return nil, fmt.Errorf("read daemon ELF: %w", err)
	}
	defer object.Close()
	for _, p := range object.Progs {
		if p.Type == elf.PT_INTERP {
			return nil, fmt.Errorf("live daemon must be static; rebuild with CGO_ENABLED=0")
		}
	}
	file, err := os.Open(binary)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxLiveDaemonBinaryBytes {
		return nil, fmt.Errorf("daemon must be a regular executable between 1 byte and %d MiB; rebuild with -ldflags='-s -w' to fit the %d MiB pod storage budget", maxLiveDaemonBinaryBytes>>20, liveDaemonEphemeralBudget>>20)
	}
	// Compress only the executable's transport, not any artifact result.
	// The pod verifies the original ELF checksum after real decompression.
	packed, err := os.CreateTemp("", "brine-live-daemon-*.gz")
	if err != nil {
		return nil, err
	}
	defer os.Remove(packed.Name())
	defer packed.Close()
	hash := sha256.New()
	zipper := gzip.NewWriter(packed)
	_, copyErr := io.Copy(io.MultiWriter(hash, zipper), file)
	closeErr := zipper.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}
	packedInfo, err := packed.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := packed.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	fmt.Printf("live daemon executable preparation: raw=%d gzip=%d elapsed=%s\n", info.Size(), packedInfo.Size(), time.Since(started))
	s, err := newLiveArtifactStore(ctx, rec)
	if err != nil {
		return nil, err
	}
	fmt.Printf("live daemon storage ready after %s\n", time.Since(started))
	d := &liveArtifactDaemon{store: s, port: uint16(port), binarySHA: fmt.Sprintf("%x", hash.Sum(nil))}
	rec.RegisterDisposer(func() {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.close(clean); err != nil {
			panic(err)
		}
	})
	node, err := s.cluster.Clientset.CoreV1().Nodes().Get(ctx, s.anchor.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	machine, known := machines[node.Status.NodeInfo.Architecture]
	if !known || node.Status.NodeInfo.OperatingSystem != "linux" || object.Machine != machine {
		return nil, fmt.Errorf("daemon ELF %s does not match actual node %s/%s", object.Machine, node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.Architecture)
	}
	d.nodeIP, err = jetbridge.NewNodeIPResolver(s.cluster.Clientset).Resolve(ctx, node.Name)
	if err != nil {
		return nil, err
	}
	// Require no Kubernetes allocation and a refused TCP connection before
	// creating anything that can bind the explicitly selected high port.
	pods, err := s.cluster.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return nil, err
	}
	for _, existing := range pods.Items {
		for _, container := range append(append([]corev1.Container{}, existing.Spec.Containers...), existing.Spec.InitContainers...) {
			for _, allocated := range container.Ports {
				if allocated.HostPort == int32(port) {
					return nil, fmt.Errorf("approved host port already allocated by %s/%s", existing.Namespace, existing.Name)
				}
			}
		}
	}
	if err := d.requireClosedPort(ctx); err != nil {
		return nil, err
	}
	directory := corev1.HostPathDirectory
	binaryLimit := *resource.NewQuantity(liveDaemonEphemeralBudget, resource.BinarySI)
	pod := s.pod("artifact-daemon", s.anchor.Spec.NodeName,
		[]corev1.Volume{
			{Name: "artifacts", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: s.root, Type: &directory}}},
			{Name: "binary", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &binaryLimit}}},
		},
		[]corev1.VolumeMount{{Name: "artifacts", MountPath: s.root}, {Name: "binary", MountPath: "/opt/brine"}},
	)
	internalPort := 7780
	args := []string{"--mirror-replicas=0"}
	if mirror {
		// Mirror destinations use the daemon's own port; the peer listens on
		// this port at its pod IP, without reserving another host port.
		internalPort = port
		if err := configureLivePeerDiscovery(ctx, s, pod); err != nil {
			return nil, err
		}
		args = []string{"--peer-discovery", "--namespace", s.cluster.Namespace,
			"--service-name", livePeerService, "--mirror-replicas=2", "--mirror-timeout=5s"}
	}
	main := &pod.Spec.Containers[0]
	main.Ports = []corev1.ContainerPort{{Name: "artifact-http", ContainerPort: int32(internalPort), HostPort: int32(port), HostIP: d.nodeIP, Protocol: corev1.ProtocolTCP}}
	main.Command = append([]string{"sh", "-ec", "while [ ! -f /opt/brine/ready ]; do sleep 0.1; done; exec /opt/brine/artifact-daemon \"$@\"", "start-owned-daemon",
		"--port", strconv.Itoa(internalPort), "--storage-path", s.root}, args...)
	main.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("128Mi")
	main.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("64Mi")
	main.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("250m")
	main.Resources.Limits[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(liveDaemonEphemeralBudget, resource.BinarySI)
	main.Resources.Requests[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(liveDaemonEphemeralBudget, resource.BinarySI)
	main.Env = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "96MiB"},
		{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}}}
	d.pod, err = s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	ready, err := awaitLiveStoragePod(ctx, s, d.pod)
	if err != nil {
		return nil, err
	}
	d.pod = ready
	if err := validatePodMounts(ready); err != nil {
		return nil, err
	}
	fmt.Printf("live daemon pod ready for upload after %s\n", time.Since(started))
	checksum, err := s.exec(ctx, ready.Name, []string{"sh", "-ec", "gzip -dc > /opt/brine/artifact-daemon; chmod 0755 /opt/brine/artifact-daemon; sha256sum /opt/brine/artifact-daemon"}, packed)
	if err != nil {
		return nil, err
	}
	fmt.Printf("live daemon executable uploaded after %s\n", time.Since(started))
	fields := strings.Fields(checksum)
	if len(fields) != 2 || fields[0] != d.binarySHA || fields[1] != liveDaemonExecutable {
		return nil, fmt.Errorf("daemon upload checksum mismatch: %q", checksum)
	}
	if _, err := s.exec(ctx, ready.Name, []string{"touch", "/opt/brine/ready"}, nil); err != nil {
		return nil, err
	}
	// Observe the actual server inside its pod before checking the node route.
	for {
		_, healthErr := s.exec(ctx, ready.Name, []string{"wget", "-T", "1", "-qO", "/dev/null", "http://127.0.0.1:" + strconv.Itoa(internalPort) + "/healthz"}, nil)
		if healthErr == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("daemon did not listen inside its pod: %w; last exec=%v", ctx.Err(), healthErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	executable, err := s.exec(ctx, ready.Name, []string{"readlink", "/proc/1/exe"}, nil)
	if err != nil || strings.TrimSpace(executable) != liveDaemonExecutable {
		return nil, fmt.Errorf("daemon PID 1 executable=%q err=%v", executable, err)
	}
	if ready.Status.HostIP != d.nodeIP {
		return nil, fmt.Errorf("daemon host IP %q differs from node InternalIP %q", ready.Status.HostIP, d.nodeIP)
	}
	if _, err := s.exec(ctx, s.observer.Name, []string{"wget", "-T", "5", "-qO", "/dev/null", d.url() + "/healthz"}, nil); err != nil {
		return nil, fmt.Errorf("independent pod cannot reach daemon through node IP: %w", err)
	}
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url()+"/healthz", nil)
		if err != nil {
			return nil, err
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && readErr == nil && closeErr == nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("real daemon health check: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Publish only the endpoint whose real listener and node identity were
	// verified above. No Kubernetes response or daemon result is synthesized.
	service, err := s.cluster.Clientset.CoreV1().Services(s.cluster.Namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: liveArtifactDaemonService},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Ports: []corev1.ServicePort{{Name: "http", Port: int32(d.port)}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	addressType := discoveryv1.AddressTypeIPv4
	if net.ParseIP(d.nodeIP).To4() == nil {
		addressType = discoveryv1.AddressTypeIPv6
	}
	readyEndpoint, nodeName, portName, endpointPort := true, d.pod.Spec.NodeName, "http", int32(d.port)
	slice, err := s.cluster.Clientset.DiscoveryV1().EndpointSlices(s.cluster.Namespace).Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: liveArtifactDaemonService, Labels: map[string]string{discoveryv1.LabelServiceName: service.Name, discoveryv1.LabelManagedBy: "brine-runtime-tests"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID}}},
		AddressType: addressType, Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &endpointPort}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{d.nodeIP}, NodeName: &nodeName, Conditions: discoveryv1.EndpointConditions{Ready: &readyEndpoint}, TargetRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: s.cluster.Namespace, Name: d.pod.Name, UID: d.pod.UID}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if slice.UID == "" || service.UID == "" {
		return nil, fmt.Errorf("owned daemon discovery lacks API identity")
	}
	fmt.Printf("published verified live daemon endpoint %s in owned EndpointSlice %s/%s UID %s for pod UID %s\n", d.url(), s.cluster.Namespace, slice.Name, slice.UID, d.pod.UID)
	fmt.Printf("real artifact daemon pod %s/%s UID %s node %s serves owned storage %s; binary SHA256 %s; node endpoint %s\n", s.cluster.Namespace, d.pod.Name, d.pod.UID, d.pod.Spec.NodeName, s.root, d.binarySHA, d.url())
	return d, nil
}

func (d *liveArtifactDaemon) url() string {
	return "http://" + net.JoinHostPort(d.nodeIP, fmt.Sprint(d.port))
}

func (d *liveArtifactDaemon) requireClosedPort(ctx context.Context) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(d.nodeIP, fmt.Sprint(d.port)))
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("node port %s must refuse connections: %v", d.url(), err)
	}
	return nil
}

func (d *liveArtifactDaemon) close(ctx context.Context) error {
	if d.pod == nil {
		return nil
	}
	if err := deleteLiveStoragePod(ctx, d.store, d.pod); err != nil {
		return err
	}
	for {
		if err := d.requireClosedPort(ctx); err == nil {
			fmt.Printf("removed real daemon UID %s and verified closed node port %s\n", d.pod.UID, d.url())
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("node port cleanup %s: %w", d.url(), ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}
