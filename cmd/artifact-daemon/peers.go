package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Peer fetches stream whole artifact tars, so they are bounded by how long
// the peer can go silent, never by how long the transfer takes in total. A
// whole-request cap (http.Client.Timeout) covers reading the body, so it
// severs a large or slow transfer mid-tar and surfaces at the step as a
// missing artifact rather than a timeout.
//
// These are vars so tests can shrink the windows; nothing else writes them.
var (
	// peerFetchResponseHeaderTimeout bounds the wait for a peer's response
	// headers, so a dead or wedged peer fails fast. Connect and TLS handshake
	// are bounded by the cloned default transport.
	peerFetchResponseHeaderTimeout = 30 * time.Second

	// peerFetchStallTimeout bounds how long a fetch will wait for the *next*
	// bytes of the body. It resets on every read that makes progress, so a
	// transfer may run as long as it needs while a peer that stops sending is
	// still cut loose.
	peerFetchStallTimeout = 2 * time.Minute
)

// PeerResolver discovers peer artifact-daemon pods via EndpointSlices
// and fetches artifacts from them for cross-node resolution.
type PeerResolver struct {
	logger      lager.Logger
	clientset   kubernetes.Interface
	namespace   string
	service     string
	port        int
	myPodIP     string // this pod's IP, to skip self during peer probe
	scheme      string // "http" or "https"
	probeClient *http.Client
	fetchClient *http.Client
}

// PeerTLSConfig holds optional mTLS configuration for peer communication.
type PeerTLSConfig struct {
	CertPath   string
	KeyPath    string
	CACertPath string
	ServerName string
}

func peerTLSServerName(namespace, service string) string {
	if namespace == "" || service == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.svc", service, namespace)
}

func loadPeerClientTLS(config *PeerTLSConfig) (*tls.Config, error) {
	if config == nil || config.CertPath == "" || config.KeyPath == "" || config.CACertPath == "" {
		return nil, fmt.Errorf("complete peer TLS certificate, key, and CA paths are required")
	}
	clientCert, err := tls.LoadX509KeyPair(config.CertPath, config.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load peer client certificate: %w", err)
	}
	caCertPEM, err := os.ReadFile(config.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("read peer CA certificate: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("parse peer CA certificate")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   config.ServerName,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

// NewPeerResolver creates a PeerResolver that discovers peers via the
// given headless service's EndpointSlices. When tlsCfg is non-nil, peer
// communication uses HTTPS with mTLS.
func NewPeerResolver(logger lager.Logger, clientset kubernetes.Interface, namespace, service string, port int, myPodIP string, tlsCfg *PeerTLSConfig) *PeerResolver {
	scheme := "http"
	// Clone rather than share http.DefaultTransport: the fetch transport
	// carries a ResponseHeaderTimeout, and mutating the process-wide default
	// would impose it on every other client here.
	fetchHTTPTransport := http.DefaultTransport.(*http.Transport).Clone()
	fetchHTTPTransport.ResponseHeaderTimeout = peerFetchResponseHeaderTimeout
	var probeTransport http.RoundTripper
	var fetchTransport http.RoundTripper = fetchHTTPTransport

	if tlsCfg != nil {
		scheme = "https"
		effective := *tlsCfg
		if effective.ServerName == "" {
			effective.ServerName = peerTLSServerName(namespace, service)
		}
		tlsConfig, err := loadPeerClientTLS(&effective)
		if err != nil {
			logger.Error("failed-to-configure-peer-tls", err)
			failure := failingRoundTripper{err: err}
			probeTransport = failure
			fetchTransport = failure
		} else {
			probeHTTPTransport := http.DefaultTransport.(*http.Transport).Clone()
			probeHTTPTransport.TLSClientConfig = tlsConfig
			fetchHTTPTransport.TLSClientConfig = tlsConfig.Clone()
			probeTransport = probeHTTPTransport
			logger.Info("peer-mtls-enabled", lager.Data{"server-name": effective.ServerName})
		}
	}

	return &PeerResolver{
		logger:    logger,
		clientset: clientset,
		namespace: namespace,
		service:   service,
		port:      port,
		myPodIP:   myPodIP,
		scheme:    scheme,
		probeClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: probeTransport,
		},
		// Deliberately no Timeout: it would cap the whole exchange including
		// the body read. fetchTarget bounds each attempt on stalled progress
		// instead.
		fetchClient: &http.Client{
			Transport: fetchTransport,
		},
	}
}

// peerIPs returns the IP addresses of all peer daemon pods (excluding self).
func (p *PeerResolver) peerIPs(ctx context.Context) ([]string, error) {
	if p.clientset == nil {
		return nil, nil
	}

	slices, err := p.clientset.DiscoveryV1().EndpointSlices(p.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + p.service,
	})
	if err != nil {
		return nil, fmt.Errorf("list endpoint slices for %s: %w", p.service, err)
	}

	var ips []string
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			for _, addr := range ep.Addresses {
				if addr != p.myPodIP {
					ips = append(ips, addr)
				}
			}
		}
	}
	return ips, nil
}

// Probe checks whether any peer daemon has the given artifact key.
// Returns the IP of the first peer that responds 200 to HEAD /artifacts/<key>,
// or ("", false) if no peer has it. Peers are probed concurrently.
func (p *PeerResolver) Probe(ctx context.Context, key string) (string, bool) {
	if err := validateCanonicalRelativeKey(key); err != nil || snapshotNamespaceKey(key) {
		return "", false
	}
	return p.probe(ctx, key, func(ip string) string { return p.artifactURL(ip, key) })
}

// ProbeSnapshot checks peers for one exact immutable digest. Snapshot keys use
// their reserved namespace and are never accepted by the generic resolver.
func (p *PeerResolver) ProbeSnapshot(ctx context.Context, digest string) (string, bool) {
	if _, err := snapshotDigestFromKey(snapshotKey(digest)); err != nil {
		return "", false
	}
	return p.probe(ctx, "snapshot:"+digest, func(ip string) string { return p.snapshotURL(ip, digest) })
}

// SnapshotPeers returns every reachable replica for a digest in stable order.
// Callers can fall through a corrupt or disappearing replica without turning
// a degraded copy into a workflow failure while another acknowledged copy is
// still readable.
func (p *PeerResolver) SnapshotPeers(ctx context.Context, digest string) []string {
	if _, err := snapshotDigestFromKey(snapshotKey(digest)); err != nil {
		return nil
	}
	ips, err := p.peerIPs(ctx)
	if err != nil || len(ips) == 0 {
		return nil
	}
	results := make(chan string, len(ips))
	for _, ip := range ips {
		go func(ip string) {
			request, err := http.NewRequestWithContext(ctx, http.MethodHead, p.snapshotURL(ip, digest), nil)
			if err != nil {
				results <- ""
				return
			}
			response, err := p.probeClient.Do(request)
			if err != nil {
				results <- ""
				return
			}
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				results <- ip
			} else {
				results <- ""
			}
		}(ip)
	}
	available := make([]string, 0, len(ips))
	for range ips {
		if ip := <-results; ip != "" {
			available = append(available, ip)
		}
	}
	sort.Strings(available)
	return available
}

func (p *PeerResolver) probe(ctx context.Context, identity string, targetForIP func(string) string) (string, bool) {
	logger := p.logger.Session("peer-probe", lager.Data{"key": identity})

	ips, err := p.peerIPs(ctx)
	if err != nil {
		logger.Error("discovery-failed", err)
		return "", false
	}
	if len(ips) == 0 {
		logger.Debug("no-peers")
		return "", false
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
			target := targetForIP(ip)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
			if err != nil {
				results <- probeResult{ip: ip, found: false}
				return
			}
			resp, err := p.probeClient.Do(req)
			if err != nil {
				logger.Debug("peer-unreachable", lager.Data{"peer": ip, "error": err.Error()})
				results <- probeResult{ip: ip, found: false}
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				results <- probeResult{ip: ip, found: true}
				return
			}
			results <- probeResult{ip: ip, found: false}
		}(ip)
	}

	for range len(ips) {
		r := <-results
		if r.found {
			logger.Info("peer-found", lager.Data{"peer": r.ip})
			return r.ip, true
		}
	}

	logger.Info("no-peer-has-artifact", lager.Data{"peers_checked": len(ips)})
	return "", false
}

// Fetch downloads an artifact from a peer daemon and writes it to destPath.
// It streams GET /artifacts/steps/<key> from the peer, which returns a tar
// stream, and extracts it to the destination directory.
func (p *PeerResolver) Fetch(ctx context.Context, peerIP, key, destPath string) error {
	return p.fetch(ctx, peerIP, key, func(body io.Reader) error {
		return extractTarToDir(ctx, body, destPath)
	})
}

// FetchSnapshot streams an immutable canonical archive from a selected peer.
// The caller owns digest verification and atomic extraction so a corrupt peer
// can never publish partial input state.
func (p *PeerResolver) FetchSnapshot(
	ctx context.Context,
	peerIP string,
	digest string,
	consume func(io.Reader) error,
) error {
	if consume == nil {
		return fmt.Errorf("snapshot consumer is required")
	}
	if _, err := snapshotDigestFromKey(snapshotKey(digest)); err != nil {
		return fmt.Errorf("invalid peer snapshot digest")
	}
	return p.fetchTarget(ctx, peerIP, "snapshot:"+digest, p.snapshotURL(peerIP, digest), consume)
}

// FetchIntoOpenedDirectory is the production peer-fetch path. The caller
// supplies an already-open parent directory, so retries and final publication
// cannot be redirected by swapping any pathname component.
func (p *PeerResolver) FetchIntoOpenedDirectory(ctx context.Context, peerIP, key string, parent *os.File, destName string) error {
	return p.fetch(ctx, peerIP, key, func(body io.Reader) error {
		return extractTarIntoOpenedDirectory(ctx, body, parent, destName)
	})
}

// FetchIntoOpenedDirectoryWithReceipt is the resolve path. It prepares the
// token-specific receipt inside the same private extraction tree and publishes
// it last when preserving an existing kubelet-mounted destination inode.
func (p *PeerResolver) FetchIntoOpenedDirectoryWithReceipt(ctx context.Context, peerIP, key string, parent *os.File, destName, receiptName string, receiptContents []byte) error {
	return p.fetch(ctx, peerIP, key, func(body io.Reader) error {
		return extractTarIntoOpenedDirectoryWithReceipt(ctx, body, parent, destName, receiptName, receiptContents)
	})
}

func (p *PeerResolver) fetch(ctx context.Context, peerIP, key string, extract func(io.Reader) error) error {
	if err := validateCanonicalRelativeKey(key); err != nil || snapshotNamespaceKey(key) {
		return fmt.Errorf("invalid peer artifact key")
	}
	return p.fetchTarget(ctx, peerIP, key, p.artifactURL(peerIP, key), extract)
}

func (p *PeerResolver) fetchTarget(
	ctx context.Context,
	peerIP string,
	identity string,
	target string,
	extract func(io.Reader) error,
) error {
	logger := p.logger.Session("peer-fetch", lager.Data{"key": identity, "peer": peerIP})

	base, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		// Each attempt gets its own cancellable context so the stall guard
		// can abort a peer that has gone quiet mid-body without touching the
		// caller's context or the other attempts.
		fetched := func() bool {
			attemptCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			resp, err := p.fetchClient.Do(base.Clone(attemptCtx))
			if err != nil {
				lastErr = err
				logger.Error("fetch-attempt-failed", err, lager.Data{"attempt": attempt})
				return false
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				lastErr = fmt.Errorf("peer returned %d", resp.StatusCode)
				logger.Error("fetch-bad-status", lastErr, lager.Data{"attempt": attempt})
				return false
			}
			body := newStallGuardedReader(resp.Body, peerFetchStallTimeout, cancel)
			extractErr := extract(body)
			body.stop()
			closeErr := resp.Body.Close()
			if extractErr == nil && closeErr == nil {
				logger.Info("fetched", lager.Data{"attempt": attempt})
				return true
			}
			lastErr = errors.Join(extractErr, closeErr)
			logger.Error("extract-failed", lastErr, lager.Data{"attempt": attempt})
			return false
		}()
		if fetched {
			return nil
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	return fmt.Errorf("peer fetch failed after 3 attempts: %w", lastErr)
}

// stallGuardedReader cancels the in-flight request when the body goes quiet
// for longer than the stall window. It is the body-side half of the timeout
// policy: the transport bounds connect, TLS and headers, and this bounds the
// gap between bytes, so a transfer is limited by the peer falling silent
// rather than by how much data it has to send.
type stallGuardedReader struct {
	inner  io.Reader
	timer  *time.Timer
	window time.Duration
}

func newStallGuardedReader(inner io.Reader, window time.Duration, cancel context.CancelFunc) *stallGuardedReader {
	return &stallGuardedReader{
		inner:  inner,
		timer:  time.AfterFunc(window, cancel),
		window: window,
	}
}

func (s *stallGuardedReader) Read(p []byte) (int, error) {
	n, err := s.inner.Read(p)
	if n > 0 {
		s.timer.Reset(s.window)
	}
	return n, err
}

// stop releases the guard once the body has been consumed. The attempt's
// context is cancelled by the caller's defer either way.
func (s *stallGuardedReader) stop() {
	s.timer.Stop()
}

func (p *PeerResolver) artifactURL(peerIP, key string) string {
	target := url.URL{
		Scheme: p.scheme,
		Host:   net.JoinHostPort(peerIP, strconv.Itoa(p.port)),
		Path:   "/artifacts/steps/" + key,
	}
	return target.String()
}

func (p *PeerResolver) snapshotURL(peerIP, digest string) string {
	target := url.URL{
		Scheme: p.scheme,
		Host:   net.JoinHostPort(peerIP, strconv.Itoa(p.port)),
		Path:   "/artifacts/" + snapshotKey(digest),
	}
	return target.String()
}

// extractTarToDir reads a tar stream and extracts files to destDir.
func extractTarToDir(ctx context.Context, r io.Reader, destDir string) error {
	if destDir == "" || !filepath.IsAbs(destDir) || filepath.Clean(destDir) != destDir {
		return fmt.Errorf("destination is not a canonical absolute path")
	}
	if info, err := os.Lstat(destDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("create destination parent: %w", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve destination parent: %w", err)
	}
	parentHandle, err := openDirectoryNoFollow(resolvedParent)
	if err != nil {
		return fmt.Errorf("anchor destination parent: %w", err)
	}
	defer parentHandle.Close()
	return extractTarIntoOpenedDirectory(ctx, r, parentHandle, filepath.Base(destDir))
}

func extractTarIntoOpenedDirectory(ctx context.Context, r io.Reader, parent *os.File, destName string) error {
	return extractTarIntoOpenedDirectoryWithReceipt(ctx, r, parent, destName, "", nil)
}

func extractTarIntoOpenedDirectoryWithReceipt(ctx context.Context, r io.Reader, parent *os.File, destName, receiptName string, receiptContents []byte) error {
	return extractTarIntoOpenedDirectoryWithReceiptAndVerify(ctx, r, parent, destName, receiptName, receiptContents, nil)
}

func extractTarIntoOpenedDirectoryWithReceiptAndVerify(
	ctx context.Context,
	r io.Reader,
	parent *os.File,
	destName string,
	receiptName string,
	receiptContents []byte,
	verify func() error,
) error {
	if destName == "" || destName == "." || destName == ".." || filepath.Base(destName) != destName {
		return fmt.Errorf("peer destination name is not a single safe component")
	}
	tmpName, tmpHandle, err := randomDirectoryAt(parent, ".peer-fetch-")
	if err != nil {
		return fmt.Errorf("create peer extraction directory: %w", err)
	}
	defer removeTreeAt(parent, tmpName)
	defer tmpHandle.Close()
	root, err := openRootAt(tmpHandle)
	if err != nil {
		return fmt.Errorf("anchor peer extraction directory: %w", err)
	}
	extractErr := extractTarAnchored(ctx, root, r)
	closeErr := root.Close()
	if extractErr != nil || closeErr != nil {
		return fmt.Errorf("extract peer tar: %w", errors.Join(extractErr, closeErr))
	}
	if verify != nil {
		if err := verify(); err != nil {
			return fmt.Errorf("verify extracted archive: %w", err)
		}
	}
	if receiptName != "" {
		if err := writeExclusiveFileAt(tmpHandle, receiptName, receiptContents, 0444); err != nil {
			return fmt.Errorf("write peer resolve receipt: %w", err)
		}
	}
	temporaryUnchanged, err := sameOpenDirectoryAt(parent, tmpName, tmpHandle)
	if err != nil {
		return fmt.Errorf("revalidate peer extraction directory: %w", err)
	}
	if !temporaryUnchanged {
		return fmt.Errorf("peer extraction directory changed before publication")
	}
	if err := publishPreparedDirectoryAt(ctx, parent, tmpName, tmpHandle, parent, destName, receiptName, nil); err != nil {
		return fmt.Errorf("publish peer extraction: %w", err)
	}
	return nil
}
