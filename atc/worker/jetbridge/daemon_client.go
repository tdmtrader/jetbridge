package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	logger              lager.Logger
	clientset           kubernetes.Interface
	namespace           string
	service             string
	port                int
	client              *http.Client
	streamingClient     *http.Client
	uploadClient        *http.Client
	uploadAcceptTimeout time.Duration
	uploadStallTimeout  time.Duration
	scheme              string // "http" or "https"
	initializationErr   error
	checkpointEndpoints func(context.Context) ([]DaemonEndpoint, error)
}

// DaemonEndpoint pairs a daemon's stable Kubernetes node identity with its
// current routable address. NodeName, not pod IP, is persisted in snapshot
// locations so pod replacement does not change storage identity.
type DaemonEndpoint struct {
	NodeName string
	Address  string
}

func (endpoint DaemonEndpoint) URL(scheme string, port int, key string) (*url.URL, error) {
	if strings.TrimSpace(endpoint.NodeName) == "" || strings.TrimSpace(endpoint.Address) == "" {
		return nil, fmt.Errorf("daemon endpoint requires node name and address")
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("invalid daemon URL scheme %q", scheme)
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid daemon port %d", port)
	}
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00") {
		return nil, fmt.Errorf("invalid daemon artifact key")
	}
	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(endpoint.Address, strconv.Itoa(port)),
		Path:   "/artifacts/" + key,
	}, nil
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
	var initializationErr error

	if tlsCfg != nil {
		scheme = "https"
		if tlsCfg.CertPath == "" || tlsCfg.KeyPath == "" || tlsCfg.CACertPath == "" {
			initializationErr = errors.New("artifact daemon mTLS requires certificate, key, and CA certificate")
		}
		serverName := ""
		if service != "" && namespace != "" {
			serverName = fmt.Sprintf("%s.%s.svc", service, namespace)
		}
		if initializationErr == nil {
			tlsConfig, err := loadDaemonClientTLS(tlsCfg.CertPath, tlsCfg.KeyPath, tlsCfg.CACertPath, serverName)
			if err != nil {
				initializationErr = err
			} else {
				transport.TLSClientConfig = tlsConfig
				logger.Info("mtls-enabled")
			}
		}
		if initializationErr != nil {
			logger.Error("failed-to-load-daemon-client-tls", initializationErr)
		}
	}
	// Snapshot reads answer before they do their work: the daemon writes the
	// tar headers and then streams, so the gap between request and first
	// response header really is the handshake and really is bounded.
	streamingTransport := transport.Clone()
	streamingTransport.ResponseHeaderTimeout = 30 * time.Second

	// Snapshot writes do not. PUT /artifacts/snapshots/... answers only after
	// the archive has been read, digest-verified, linked, and durably committed
	// to Hangar, so its first response byte trails the last request byte by a
	// margin that scales with snapshot size. ResponseHeaderTimeout measures
	// precisely that margin, so on this endpoint it is not a handshake bound at
	// all — it is a whole-request deadline in disguise, and any snapshot whose
	// commit outlasts it can never be uploaded. Uploads get their own transport
	// with no such bound; snapshotUploadGuard supplies liveness bounds that are
	// derived from actual daemon behaviour instead of elapsed time.
	uploadTransport := transport.Clone()
	uploadTransport.ResponseHeaderTimeout = 0

	return &DaemonClient{
		logger:            logger,
		clientset:         clientset,
		namespace:         namespace,
		service:           service,
		port:              port,
		scheme:            scheme,
		initializationErr: initializationErr,
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		},
		streamingClient:     &http.Client{Transport: streamingTransport},
		uploadClient:        &http.Client{Transport: uploadTransport},
		uploadAcceptTimeout: defaultSnapshotUploadAcceptTimeout,
		uploadStallTimeout:  defaultSnapshotUploadStallTimeout,
	}
}

// NewDaemonClientChecked constructs a daemon client and rejects incomplete or
// invalid requested TLS. Callers enabling authenticated snapshot content must
// use this constructor so configuration failure cannot downgrade to HTTP.
func NewDaemonClientChecked(logger lager.Logger, clientset kubernetes.Interface, namespace, service string, port int, tlsCfg *DaemonClientTLSConfig) (*DaemonClient, error) {
	client := NewDaemonClient(logger, clientset, namespace, service, port, tlsCfg)
	if client.initializationErr != nil {
		return nil, client.initializationErr
	}
	return client, nil
}

// DaemonEndpoints returns one deterministic current address for every usable
// daemon node. EndpointSlice list/order and duplicate pod addresses cannot
// affect the result.
func (d *DaemonClient) DaemonEndpoints(ctx context.Context) ([]DaemonEndpoint, error) {
	if d == nil || d.clientset == nil {
		return nil, fmt.Errorf("daemon discovery client is required")
	}
	slices, err := d.clientset.DiscoveryV1().EndpointSlices(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + d.service,
	})
	if err != nil {
		return nil, fmt.Errorf("list endpoint slices for %s: %w", d.service, err)
	}
	addressByNode := make(map[string]string)
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			if endpoint.Conditions.Serving != nil && !*endpoint.Conditions.Serving {
				continue
			}
			if endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
				continue
			}
			if endpoint.NodeName == nil || strings.TrimSpace(*endpoint.NodeName) == "" {
				continue
			}
			nodeName := strings.TrimSpace(*endpoint.NodeName)
			for _, candidate := range endpoint.Addresses {
				candidate = strings.TrimSpace(candidate)
				if candidate == "" {
					continue
				}
				if current, found := addressByNode[nodeName]; !found || candidate < current {
					addressByNode[nodeName] = candidate
				}
			}
		}
	}
	endpoints := make([]DaemonEndpoint, 0, len(addressByNode))
	for nodeName, address := range addressByNode {
		endpoints = append(endpoints, DaemonEndpoint{NodeName: nodeName, Address: address})
	}
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].NodeName != endpoints[j].NodeName {
			return endpoints[i].NodeName < endpoints[j].NodeName
		}
		return endpoints[i].Address < endpoints[j].Address
	})
	return endpoints, nil
}

func (d *DaemonClient) snapshotHTTPClient() (*http.Client, error) {
	if d == nil {
		return nil, fmt.Errorf("daemon client is required")
	}
	if d.initializationErr != nil {
		return nil, d.initializationErr
	}
	if d.streamingClient == nil {
		return nil, fmt.Errorf("daemon streaming client is required")
	}
	return d.streamingClient, nil
}

// snapshotRepairHTTPClient returns a client for durable-metadata repair. The
// daemon proves a whole object before it answers, which can outlast both the
// probe client's short whole-request timeout and the streaming transport's
// response-header budget, so repair borrows the probe transport without either
// deadline and relies on the caller's context instead.
func (d *DaemonClient) snapshotRepairHTTPClient() (*http.Client, error) {
	if d == nil {
		return nil, fmt.Errorf("daemon client is required")
	}
	if d.initializationErr != nil {
		return nil, d.initializationErr
	}
	if d.client == nil || d.client.Transport == nil {
		return nil, fmt.Errorf("daemon probe transport is required")
	}
	return &http.Client{Transport: d.client.Transport}, nil
}

// snapshotUploadHTTPClient returns the transport reserved for snapshot writes.
// It is deliberately distinct from the streaming client: sharing one would
// force the deferred-response upload to live under a response-header bound that
// only the immediate-response read path can honour.
func (d *DaemonClient) snapshotUploadHTTPClient() (*http.Client, error) {
	if d == nil {
		return nil, fmt.Errorf("daemon client is required")
	}
	if d.initializationErr != nil {
		return nil, d.initializationErr
	}
	if d.uploadClient == nil {
		return nil, fmt.Errorf("daemon upload client is required")
	}
	return d.uploadClient, nil
}

func (d *DaemonClient) snapshotUploadBounds() snapshotUploadBounds {
	if d == nil {
		return snapshotUploadBounds{}.withDefaults()
	}
	return snapshotUploadBounds{accept: d.uploadAcceptTimeout, stall: d.uploadStallTimeout}.withDefaults()
}

func (d *DaemonClient) snapshotURL(endpoint DaemonEndpoint, key string) (*url.URL, error) {
	if d == nil {
		return nil, fmt.Errorf("daemon client is required")
	}
	return endpoint.URL(d.scheme, d.port, key)
}

func (d *DaemonClient) routeURL(address, route string) string {
	return (&url.URL{
		Scheme: d.scheme,
		Host:   net.JoinHostPort(address, strconv.Itoa(d.port)),
		Path:   "/" + strings.TrimPrefix(route, "/"),
	}).String()
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
// cache key registered. Sends a POST /resolve with a temporary destination
// to each daemon to check if the key exists in its registry. The daemon's
// resolveOne checks registry → filesystem → peers, so a registered alias
// will be found.
//
// Returns the daemon pod IP that responded with status "ok" or "not_found"
// indicating the key was found. If no daemon has it, returns ("", false, nil).
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
			// Use HEAD /artifacts/{key} — the daemon stats the raw path.
			// The registered alias key maps to a disk path, but the HEAD
			// handler checks storagePath/{key} not the registry.
			//
			// Instead, use the new HEAD /resource-caches/{key} endpoint
			// on upgraded daemons, falling back to checking if the daemon
			// has the key registered by POSTing a resolve with /dev/null
			// as destination (the daemon returns "ok" if found without
			// writing anything meaningful).

			// Try the new endpoint first (daemon v0.2.83+).
			requestURL := d.routeURL(ip, "/resource-caches/"+cacheKey)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, requestURL, nil)
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

			// Fallback for older daemons: POST /resolve with a probe-only
			// destination. The daemon checks registry → filesystem → peers.
			// We use a unique temp path to avoid conflicts.
			resolveURL := d.routeURL(ip, "/resolve")
			// Use /tmp/probe-{key} as destination — the daemon will try to
			// copy data there but we only care about the response status.
			resolveBody := fmt.Sprintf(`{"key":%q,"dest":"/tmp/concourse-probe-%s"}`, cacheKey, cacheKey)
			resolveReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveURL, strings.NewReader(resolveBody))
			if err != nil {
				results <- probeResult{}
				return
			}
			resolveReq.Header.Set("Content-Type", "application/json")

			resolveResp, err := d.client.Do(resolveReq)
			if err != nil {
				results <- probeResult{}
				return
			}
			resolveResp.Body.Close()

			if resolveResp.StatusCode == http.StatusOK {
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
			requestURL := d.routeURL(ip, "/artifacts/steps/"+key)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, requestURL, nil)
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

	requestURL := d.routeURL(daemonIP, "/mirror")
	body := fmt.Sprintf(`{"key":%q}`, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(body))
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
func (d *DaemonClient) RegisterAlias(ctx context.Context, key, localPath string) error {
	logger := d.logger.Session("register-alias", lager.Data{"key": key})

	ips, err := d.daemonIPs(ctx)
	if err != nil {
		return fmt.Errorf("discover daemon IPs: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no daemon pods found")
	}

	body := fmt.Sprintf(`{"key":%q,"local_path":%q}`, key, localPath)
	registered := false

	for _, ip := range ips {
		requestURL := d.routeURL(ip, "/register")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(body))
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
