package jetbridge

import (
	"context"
	"fmt"
	"net"
	"time"
)

// discoveryTimeout reserves time for the InternalIP fallback and daemon request.
const discoveryTimeout = 2 * time.Second

// daemonAddress reaches the artifact daemon on nodeName over the pod network.
// Discover on every call: a restarted daemon has a new pod IP. Only discovery
// without a ready endpoint for this node falls back to the cached InternalIP.
func daemonAddress(ctx context.Context, nodeName string, daemon *DaemonClient, nodes *NodeIPResolver) (string, error) {
	// Keep the node resolver's sentinel and error shape even if an endpoint
	// happens to carry an IP-shaped node name.
	if net.ParseIP(nodeName) != nil {
		return "", fmt.Errorf("%w: %q", ErrNodeNameIsIP, nodeName)
	}
	if daemon != nil && daemon.service != "" && nodeName != "" {
		timeout := discoveryTimeout
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline)/2)
		}
		discoveryCtx, cancel := context.WithTimeout(ctx, timeout)
		endpoints, err := daemon.daemonEndpoints(discoveryCtx)
		cancel()
		if err == nil {
			for _, endpoint := range endpoints {
				if endpoint.Node == nodeName && endpoint.IP != "" {
					return endpoint.IP, nil
				}
			}
		}
	}
	return nodes.Resolve(ctx, nodeName)
}
