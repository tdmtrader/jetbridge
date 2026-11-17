package steps

import (
	"context"
	"fmt"
	"net"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// livePeerWebService is the daemon discovery the web is handed when a
// scenario's reader may fall back to the independent peer.
//
// The web takes a ready endpoint on node N for N's own daemon, and dials it in
// place of N's InternalIP. In production that is the DaemonSet pod on N. The
// live tier has one approved node, so its peer is scheduled on the producer's
// node, and the controller-generated slice of livePeerService names that node:
// handed that slice, the web would read the producer's artifact from the peer.
// So the web gets the peer's verified endpoint -- its real pod address and
// pod UID -- without a node name: an endpoint that speaks for no node, as a
// peer on another node speaks for none the read resolves. livePeerService
// itself is untouched; the daemons' own mirroring still discovers through it.
const livePeerWebService = "artifact-peer-web"

// publishPeerForWeb publishes peer, which startLiveArtifactPeer verified
// against its controller-generated endpoint, as livePeerWebService.
func publishPeerForWeb(ctx context.Context, cs kubernetes.Interface, namespace string, peer *corev1.Pod, port uint16) error {
	if net.ParseIP(peer.Status.PodIP) == nil {
		return fmt.Errorf("peer %s has no pod address to publish", peer.Name)
	}
	service, err := cs.CoreV1().Services(namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: livePeerWebService},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Ports: []corev1.ServicePort{{Name: "http", Port: int32(port)}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	addressType := discoveryv1.AddressTypeIPv4
	if net.ParseIP(peer.Status.PodIP).To4() == nil {
		addressType = discoveryv1.AddressTypeIPv6
	}
	ready, portName, endpointPort := true, "http", int32(port)
	_, err = cs.DiscoveryV1().EndpointSlices(namespace).Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: livePeerWebService,
			Labels:          map[string]string{discoveryv1.LabelServiceName: service.Name, discoveryv1.LabelManagedBy: "brine-runtime-tests"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID}}},
		AddressType: addressType,
		Ports:       []discoveryv1.EndpointPort{{Name: &portName, Port: &endpointPort}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{peer.Status.PodIP}, Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: namespace, Name: peer.Name, UID: peer.UID}}},
	}, metav1.CreateOptions{})
	return err
}

// requireDaemonResolution proves how the web found node's daemon, through the
// actual API requests t observed. With discovery (service) publishing a ready
// endpoint on node, the web dials that endpoint: it must have listed the
// service's slices and never read the node. Without one -- no discovery, or
// none published for node -- it falls back to one real read of the node.
// direct is a client outside the observation, so the check adds nothing to it.
func (t *execObservation) requireDaemonResolution(ctx context.Context, direct kubernetes.Interface, namespace, service, nodeName string) error {
	if t == nil {
		return fmt.Errorf("missing real daemon-resolution observation")
	}
	published := false
	if service != "" {
		var err error
		if published, err = daemonPublishedOn(ctx, direct, namespace, service, nodeName); err != nil {
			return err
		}
	}
	t.mu.Lock()
	requests := append([]volumeExecRequest(nil), t.requests...)
	t.mu.Unlock()
	return daemonResolutionVerdict(requests, namespace, service, nodeName, published)
}

// daemonPublishedOn applies the web's own rule: a ready (or unreported)
// endpoint with an address, on nodeName.
func daemonPublishedOn(ctx context.Context, cs kubernetes.Interface, namespace, service, nodeName string) (bool, error) {
	slices, err := cs.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + service})
	if err != nil {
		return false, fmt.Errorf("list %s's endpoints: %w", service, err)
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			if ep.NodeName != nil && *ep.NodeName == nodeName && len(ep.Addresses) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func daemonResolutionVerdict(requests []volumeExecRequest, namespace, service, nodeName string, published bool) error {
	if !published {
		return (&execObservation{requests: requests}).requireNodeRead(nodeName)
	}
	slices := "/apis/discovery.k8s.io/v1/namespaces/" + namespace + "/endpointslices"
	selector := discoveryv1.LabelServiceName + "=" + service
	lists, nodeReads := 0, 0
	for _, r := range requests {
		switch {
		case r.path == "/api/v1/nodes/"+nodeName:
			nodeReads++
		case r.method == http.MethodGet && r.path == slices && r.query.Get("labelSelector") == selector:
			if r.status != http.StatusOK || r.err != nil {
				return fmt.Errorf("endpoint list for %s failed: HTTP %d: %v", service, r.status, r.err)
			}
			lists++
		}
	}
	if nodeReads != 0 {
		return fmt.Errorf("the web read node %s's address although discovery published its daemon (%d node reads)", nodeName, nodeReads)
	}
	if lists == 0 {
		return fmt.Errorf("the web never listed %s's endpoints to find node %s's daemon", service, nodeName)
	}
	return nil
}
