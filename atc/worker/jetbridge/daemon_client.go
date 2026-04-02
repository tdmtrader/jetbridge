package jetbridge

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DaemonClient discovers artifact-daemon pods via EndpointSlices and queries
// them for resource cache existence. It mirrors the PeerResolver discovery
// pattern in cmd/artifact-daemon/peers.go but runs on the ATC side.
type DaemonClient struct {
	logger    lager.Logger
	clientset kubernetes.Interface
	namespace string
	service   string
	port      int
	client    *http.Client
}

// NewDaemonClient creates a DaemonClient that discovers daemon pods via the
// given headless service's EndpointSlices.
func NewDaemonClient(logger lager.Logger, clientset kubernetes.Interface, namespace, service string, port int) *DaemonClient {
	return &DaemonClient{
		logger:    logger,
		clientset: clientset,
		namespace: namespace,
		service:   service,
		port:      port,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// daemonIPs returns the IP addresses of all artifact-daemon pods.
func (d *DaemonClient) daemonIPs(ctx context.Context) ([]string, error) {
	slices, err := d.clientset.DiscoveryV1().EndpointSlices(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + d.service,
	})
	if err != nil {
		return nil, fmt.Errorf("list endpoint slices for %s: %w", d.service, err)
	}

	var ips []string
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			for _, addr := range ep.Addresses {
				ips = append(ips, addr)
			}
		}
	}
	return ips, nil
}

// ProbeResourceCache checks whether any daemon pod has the given resource
// cache key. The cache is registered as a symlink under steps/{key} on the
// daemon's hostPath, so we probe with HEAD /artifacts/steps/{key} which
// works with any daemon version (no new endpoints required).
//
// Returns the daemon pod IP that responded 200 (the node name is not
// available from the existing /artifacts HEAD response, but the IP is
// sufficient for the caller to record in the ArtifactLocator).
// If no daemon has it, returns ("", false, nil).
func (d *DaemonClient) ProbeResourceCache(ctx context.Context, cacheKey string) (string, bool, error) {
	logger := d.logger.Session("probe-resource-cache", lager.Data{"key": cacheKey})

	ips, err := d.daemonIPs(ctx)
	if err != nil {
		logger.Error("discovery-failed", err)
		return "", false, nil // treat discovery failure as cache miss
	}
	if len(ips) == 0 {
		logger.Debug("no-daemons")
		return "", false, nil
	}

	type probeResult struct {
		ip    string
		found bool
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan probeResult, len(ips))

	for _, ip := range ips {
		go func(ip string) {
			url := fmt.Sprintf("http://%s:%d/artifacts/steps/%s", ip, d.port, cacheKey)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
			if err != nil {
				results <- probeResult{}
				return
			}
			resp, err := d.client.Do(req)
			if err != nil {
				logger.Debug("daemon-unreachable", lager.Data{"ip": ip, "error": err.Error()})
				results <- probeResult{}
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				results <- probeResult{ip: ip, found: true}
				return
			}
			results <- probeResult{}
		}(ip)
	}

	for range ips {
		r := <-results
		if r.found {
			logger.Info("cache-found", lager.Data{"daemon_ip": r.ip})
			return r.ip, true, nil
		}
	}

	logger.Debug("cache-not-found", lager.Data{"daemons_checked": len(ips)})
	return "", false, nil
}
