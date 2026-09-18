package steps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func liveNodeResolverDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, NodeIPOutcome]("a real node is resolved before and after its API route closes",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (NodeIPOutcome, error) {
				return resolveLiveNode(rec)
			}),
		CheckThat[NodeIPOutcome]("both answers retain its published internal address with only one API read",
			func(in NodeIPOutcome) error {
				if in.Err != nil {
					return fmt.Errorf("resolving failed: %w", in.Err)
				}
				if in.Expected == "" || len(in.IPs) != 2 {
					return fmt.Errorf("expected two answers for published address %q, got %v", in.Expected, in.IPs)
				}
				for _, ip := range in.IPs {
					if ip != in.Expected {
						return fmt.Errorf("answer %q differs from published address %q", ip, in.Expected)
					}
				}
				if in.trace == nil {
					return fmt.Errorf("resolver has no API observation")
				}
				in.trace.mu.Lock()
				defer in.trace.mu.Unlock()
				if len(in.trace.requests) != 1 {
					return fmt.Errorf("expected one real node read, observed %d", len(in.trace.requests))
				}
				r := in.trace.requests[0]
				if r.method != http.MethodGet || r.path != in.NodePath || r.status != http.StatusOK || r.err != nil {
					return fmt.Errorf("node read: %s %s HTTP %d error=%v", r.method, r.path, r.status, r.err)
				}
				return nil
			}),
	}
}

// Read only from the explicitly selected live cluster. The route carries
// unchanged API TLS; closing it destroys only this scenario's connections.
// No namespace, Node, status, RBAC object or response is created or modified.
func resolveLiveNode(rec *brine.Recorder) (NodeIPOutcome, error) {
	out := NodeIPOutcome{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := liveKubernetesConfig()
	if err != nil {
		return out, err
	}
	direct, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return out, err
	}
	nodes, err := direct.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, err
	}
	var node *corev1.Node
	for i := range nodes.Items {
		n := &nodes.Items[i]
		for _, address := range n.Status.Addresses {
			if address.Type == corev1.NodeInternalIP && net.ParseIP(address.Address) != nil {
				node = n
				out.Expected = address.Address
				break
			}
		}
		if node != nil {
			break
		}
	}
	if node == nil {
		return out, fmt.Errorf("selected cluster has no node with a published InternalIP")
	}
	endpoint, err := url.Parse(cfg.Host)
	if err != nil {
		return out, err
	}
	route, err := routeWithDrops("127.0.0.1:0", apiRouteAddress(endpoint), 0, false)
	if err != nil {
		return out, err
	}
	rec.RegisterDisposer(func() {
		if err := route.Close(); err != nil {
			panic(err)
		}
	})
	routed := rest.CopyConfig(cfg)
	if routed.ServerName == "" {
		routed.ServerName = endpoint.Hostname()
	}
	endpoint.Host = route.Addr().String()
	routed.Host = endpoint.String()
	trace := new(execObservation)
	client, err := kubernetes.NewForConfig(trace.config(routed))
	if err != nil {
		return out, err
	}
	in := NodeCluster{Ctx: ctx, Clientset: direct, Resolver: jetbridge.NewNodeIPResolver(client), trace: trace}
	out.NodePath = "/api/v1/nodes/" + node.Name
	if !out.resolve(in, node.Name) {
		return out, nil
	}
	if err := route.Close(); err != nil {
		return out, err
	}
	// Independently prove an uncached request can no longer reach that route.
	probe, err := kubernetes.NewForConfig(routed)
	if err != nil {
		return out, err
	}
	probeCtx, stop := context.WithTimeout(ctx, time.Second)
	_, probeErr := probe.CoreV1().Nodes().Get(probeCtx, node.Name, metav1.GetOptions{})
	stop()
	if probeErr == nil {
		return out, fmt.Errorf("closed API route still served a fresh node read")
	}
	out.resolve(in, node.Name)
	// The real node still exists: only our connection was removed.
	current, err := direct.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if err != nil || current.UID != node.UID {
		return out, fmt.Errorf("real node changed during cache observation: %v", err)
	}
	published := false
	for _, address := range current.Status.Addresses {
		published = published || address.Type == corev1.NodeInternalIP && address.Address == out.Expected
	}
	if !published {
		return out, fmt.Errorf("real node no longer publishes the observed address")
	}
	fmt.Printf("verified read-only node resolution: node %s UID %s address %s; owned API route closed\n", node.Name, node.UID, out.Expected)
	return out, nil
}
