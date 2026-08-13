package jetbridge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
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
	// warmClient is used for durable restores, which legitimately take far
	// longer than a probe. It has no overall Timeout — the caller supplies a
	// context deadline — but does bound the dial and the wait for response
	// headers, so a black-holed pod IP cannot eat the whole warm budget.
	warmClient *http.Client
	scheme     string // "http" or "https"
}

// DaemonClientTLSConfig holds optional mTLS configuration for the DaemonClient.
type DaemonClientTLSConfig struct {
	CertPath   string
	KeyPath    string
	CACertPath string
}

// NewDaemonClient creates a DaemonClient that discovers daemon pods via the
// given headless service's EndpointSlices. When tlsCfg is non-nil, the client
// uses HTTPS with mTLS (client certificate + CA trust).
func NewDaemonClient(logger lager.Logger, clientset kubernetes.Interface, namespace, service string, port int, tlsCfg *DaemonClientTLSConfig) *DaemonClient {
	scheme := "http"
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if tlsCfg != nil && tlsCfg.CertPath != "" && tlsCfg.KeyPath != "" && tlsCfg.CACertPath != "" {
		serverName := ""
		if service != "" && namespace != "" {
			serverName = fmt.Sprintf("%s.%s.svc", service, namespace)
		}
		tlsConfig, err := loadDaemonClientTLS(tlsCfg.CertPath, tlsCfg.KeyPath, tlsCfg.CACertPath, serverName)
		if err != nil {
			logger.Error("failed-to-load-daemon-client-tls", err)
		} else {
			transport.TLSClientConfig = tlsConfig
			scheme = "https"
			logger.Info("mtls-enabled")
		}
	}

	return &DaemonClient{
		logger:    logger,
		clientset: clientset,
		namespace: namespace,
		service:   service,
		port:      port,
		scheme:    scheme,
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		},
		warmClient: &http.Client{Transport: warmTransport(transport)},
	}
}

// warmTransport clones the probe transport and bounds the two phases a restore
// must not stall in: connecting, and waiting for the daemon to say anything at
// all. The body may then stream for as long as the caller's context allows.
func warmTransport(base *http.Transport) *http.Transport {
	t := base.Clone()
	t.DialContext = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	t.ResponseHeaderTimeout = warmResponseHeaderTimeout

	return t
}

// daemonEndpoint is one artifact-daemon pod: where to reach it, and which node
// it speaks for.
type daemonEndpoint struct {
	IP   string
	Node string
}

// daemonEndpoints returns every ready artifact-daemon pod.
//
// The readiness filter is a fix, not a refinement: the previous version
// flattened every address in the slice regardless of condition, so a pod that
// was terminating could win a probe race and then be bound to as the source of
// an artifact.
func (d *DaemonClient) daemonEndpoints(ctx context.Context) ([]daemonEndpoint, error) {
	slices, err := d.clientset.DiscoveryV1().EndpointSlices(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + d.service,
	})
	if err != nil {
		return nil, fmt.Errorf("list endpoint slices for %s: %w", d.service, err)
	}

	var eps []daemonEndpoint
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			// Ready is a *bool; nil means "no opinion", which the API
			// documents as ready.
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			node := ""
			if ep.NodeName != nil {
				node = *ep.NodeName
			}
			for _, addr := range ep.Addresses {
				eps = append(eps, daemonEndpoint{IP: addr, Node: node})
			}
		}
	}

	return eps, nil
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

// ProbeResult is what a probe learned about the cluster, not just about the
// key. Endpoints and DurableCapable are what the caller needs to decide whether
// a durable warm is even worth attempting.
type ProbeResult struct {
	// IP and Node identify a daemon holding the key locally. Meaningful only
	// when ProbeResourceCache reported found.
	IP   string
	Node string

	// DurableCapable is true when any daemon advertised a durable tier. It is
	// the OR across every response, so one misconfigured pod cannot make a key
	// look permanently unavailable.
	DurableCapable bool

	// Endpoints is the daemon set as discovered, so a warm does not have to
	// re-list EndpointSlices.
	Endpoints []daemonEndpoint
}

// ProbeResourceCache asks every daemon whether it holds the key ON LOCAL DISK.
//
// A 200 has exactly one meaning here and it must keep it: these bytes are on
// this node right now. That is what makes the returned pod worth binding to.
// The durable store is deliberately not consulted — every daemon sees the same
// bucket, so if it were, all of them would answer yes for anything ever stored
// and the winner of the race below would be arbitrary, which is precisely the
// node affinity this probe exists to provide.
//
// Racing to the first responder is right when every responder is a genuine
// local holder: the fastest to answer is the least loaded.
//
// It also does NOT fall back to POST /resolve, as an earlier version did. That
// leg answered 200 off a PEER fetch — so a daemon holding nothing locally could
// win — and it copied the whole artifact into /tmp inside the daemon pod,
// outside the swept storage path, where nothing ever reclaimed it.
//
// Never returns an error. Discovery failure, an unreachable pod and a genuine
// miss are all "no", because the caller re-runs the get step either way.
func (d *DaemonClient) ProbeResourceCache(ctx context.Context, cacheKey string) (ProbeResult, bool) {
	logger := d.logger.Session("probe-resource-cache", lager.Data{"key": cacheKey})

	eps, err := d.daemonEndpoints(ctx)
	if err != nil {
		logger.Error("discovery-failed", err)
		return ProbeResult{}, false
	}
	if len(eps) == 0 {
		logger.Debug("no-daemons")
		return ProbeResult{}, false
	}

	type probeResult struct {
		ep             daemonEndpoint
		found          bool
		durableCapable bool
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan probeResult, len(eps))

	for _, ep := range eps {
		go func(ep daemonEndpoint) {
			url := fmt.Sprintf("%s://%s:%d/resource-caches/%s", d.scheme, ep.IP, d.port, cacheKey)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
			if err != nil {
				results <- probeResult{}
				return
			}

			resp, err := d.client.Do(req)
			if err != nil {
				logger.Debug("daemon-unreachable", lager.Data{"ip": ep.IP, "error": err.Error()})
				results <- probeResult{}
				return
			}
			resp.Body.Close()

			// Read capability on every status, not just 200. A daemon that
			// answers 404 for this key is still the daemon that can warm it,
			// and a transient 500 must not make a node look tier-incapable.
			capable := resp.Header.Get(DurableTierHeader) != ""

			results <- probeResult{ep: ep, found: resp.StatusCode == http.StatusOK, durableCapable: capable}
		}(ep)
	}

	out := ProbeResult{Endpoints: eps}
	for range eps {
		r := <-results
		if r.durableCapable {
			out.DurableCapable = true
		}
		if r.found {
			// Stop draining: the remaining responses matter only for
			// DurableCapable, which is not consulted on a hit.
			out.IP, out.Node = r.ep.IP, r.ep.Node
			logger.Info("cache-found", lager.Data{"daemon_ip": r.ep.IP, "node": r.ep.Node})
			return out, true
		}
	}

	logger.Debug("cache-not-found", lager.Data{"daemons_checked": len(eps), "durable_capable": out.DurableCapable})

	return out, false
}

// ProbeStepArtifact checks whether any daemon pod has the given step
// artifact key on disk. Sends a concurrent HEAD /artifacts/steps/{key}
// to every discovered daemon IP and returns the IP of the first peer
// that responds 200.
//
// Used by DaemonSetVolume.StreamOut to fall back to peer reads when
// the originally-recorded producer node is unreachable (spot
// preemption, crash, network partition). Symmetric to the daemon-side
// peer probe in cmd/artifact-daemon/peers.go.
//
// Returns ("", false, nil) when no daemon has the key. Discovery
// failure is treated as a miss (returns nil error) so the caller
// falls through to its existing not-found error path.
func (d *DaemonClient) ProbeStepArtifact(ctx context.Context, key string) (string, bool, error) {
	logger := d.logger.Session("probe-step-artifact", lager.Data{"key": key})

	ips, err := d.daemonIPs(ctx)
	if err != nil {
		logger.Error("discovery-failed", err)
		return "", false, nil
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
			url := fmt.Sprintf("%s://%s:%d/artifacts/steps/%s", d.scheme, ip, d.port, key)
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
			logger.Info("artifact-found", lager.Data{"daemon_ip": r.ip})
			return r.ip, true, nil
		}
	}

	logger.Debug("artifact-not-found", lager.Data{"daemons_checked": len(ips)})
	return "", false, nil
}

// TriggerMirror fires a fire-and-forget POST /mirror to the daemon at
// daemonIP, asking it to schedule an async mirror of the artifact at
// `key` to peer daemons. The recorded producer node's daemon is the
// only one that actually runs the mirror (only it has the data on
// disk), so callers should target the producer's daemon IP.
//
// Best-effort by contract: returns nil on 202 success, on transport
// failure, AND on non-202 responses. All non-success outcomes are
// logged. The motivation is that mirror trigger is an optimization;
// failing to schedule it MUST NOT fail the producing step. If the
// mirror doesn't happen, the build's data lives on a single node and
// reverts to today's behavior — a node loss forces a rerun, but the
// step itself succeeded.
func (d *DaemonClient) TriggerMirror(ctx context.Context, daemonIP, key string) error {
	logger := d.logger.Session("trigger-mirror", lager.Data{"daemon_ip": daemonIP, "key": key})

	url := fmt.Sprintf("%s://%s:%d/mirror", d.scheme, daemonIP, d.port)
	body := fmt.Sprintf(`{"key":%q}`, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		logger.Error("create-request-failed", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		logger.Debug("daemon-unreachable", lager.Data{"error": err.Error()})
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		logger.Info("non-202", lager.Data{"status": resp.StatusCode})
	}
	return nil
}

// RegisterAlias registers an alias on all daemon pods via POST /register.
// The alias maps key → localPath in the daemon's registry. On a single-node
// cluster only one daemon exists; on multi-node, only the daemon whose node
// has the localPath will accept the registration (the daemon validates that
// the path exists on disk).
func (d *DaemonClient) RegisterAlias(ctx context.Context, key, localPath string, durable bool) error {
	logger := d.logger.Session("register-alias", lager.Data{"key": key})

	ips, err := d.daemonIPs(ctx)
	if err != nil {
		return fmt.Errorf("discover daemon IPs: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no daemon pods found")
	}

	body := fmt.Sprintf(`{"key":%q,"local_path":%q,"durable":%t}`, key, localPath, durable)
	registered := false

	for _, ip := range ips {
		url := fmt.Sprintf("%s://%s:%d/register", d.scheme, ip, d.port)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := d.client.Do(req)
		if err != nil {
			logger.Debug("daemon-unreachable", lager.Data{"ip": ip, "error": err.Error()})
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusCreated {
			logger.Info("registered", lager.Data{"daemon_ip": ip})
			registered = true
			break // Only need to register on the daemon that has the path
		}
		// 404 = path not found on this daemon's node, try next
		logger.Debug("daemon-rejected", lager.Data{"ip": ip, "status": resp.StatusCode})
	}

	if !registered {
		return fmt.Errorf("no daemon accepted registration for key %s (path: %s)", key, localPath)
	}
	return nil
}
