package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ErrNodeNameIsIP is returned by Resolve when the caller passes an
// IP-shaped string as a nodeName. The K8s Nodes API is keyed by Node
// object name, never by IP, so such calls can only fail — surfacing
// this sentinel makes accidental misuse loud.
var ErrNodeNameIsIP = errors.New("node name argument is an IP address, not a K8s Node name")

// NodeIPResolver resolves Kubernetes node names to their internal IP addresses.
// Results are cached with a TTL to avoid repeated API calls.
type NodeIPResolver struct {
	clientset kubernetes.Interface

	mu    sync.RWMutex
	cache map[string]nodeIPEntry
}

type nodeIPEntry struct {
	ip        string
	expiresAt time.Time
}

const nodeIPCacheTTL = 5 * time.Minute

// NewNodeIPResolver creates a NodeIPResolver backed by the given K8s clientset.
func NewNodeIPResolver(clientset kubernetes.Interface) *NodeIPResolver {
	return &NodeIPResolver{
		clientset: clientset,
		cache:     make(map[string]nodeIPEntry),
	}
}

// Resolve returns the internal IP address for the given node name.
func (r *NodeIPResolver) Resolve(ctx context.Context, nodeName string) (string, error) {
	// Check cache first.
	r.mu.RLock()
	entry, ok := r.cache[nodeName]
	r.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.ip, nil
	}

	// Fetch from K8s API.
	node, err := r.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get node %s: %w", nodeName, err)
	}

	ip := nodeInternalIP(node)
	if ip == "" {
		return "", fmt.Errorf("node %s has no InternalIP address", nodeName)
	}

	r.mu.Lock()
	r.cache[nodeName] = nodeIPEntry{ip: ip, expiresAt: time.Now().Add(nodeIPCacheTTL)}
	r.mu.Unlock()

	return ip, nil
}

// nodeInternalIP extracts the InternalIP from a Node's status addresses.
func nodeInternalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}
