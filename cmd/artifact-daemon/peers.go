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
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	var probeTransport, fetchTransport http.RoundTripper

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
			fetchHTTPTransport := http.DefaultTransport.(*http.Transport).Clone()
			fetchHTTPTransport.TLSClientConfig = tlsConfig.Clone()
			probeTransport = probeHTTPTransport
			fetchTransport = fetchHTTPTransport
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
		fetchClient: &http.Client{
			Timeout:   3 * time.Minute,
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
	logger := p.logger.Session("peer-probe", lager.Data{"key": key})

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
			target := p.artifactURL(ip, key)
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
	logger := p.logger.Session("peer-fetch", lager.Data{"key": key, "peer": peerIP})
	target := p.artifactURL(peerIP, key)

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		resp, err := p.fetchClient.Do(req)
		if err != nil {
			lastErr = err
			logger.Error("fetch-attempt-failed", err, lager.Data{"attempt": attempt})
		} else if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("peer returned %d", resp.StatusCode)
			logger.Error("fetch-bad-status", lastErr, lager.Data{"attempt": attempt})
		} else {
			extractErr := extract(resp.Body)
			closeErr := resp.Body.Close()
			if extractErr == nil && closeErr == nil {
				logger.Info("fetched", lager.Data{"attempt": attempt})
				return nil
			}
			lastErr = errors.Join(extractErr, closeErr)
			logger.Error("extract-failed", lastErr, lager.Data{"attempt": attempt})
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

func (p *PeerResolver) artifactURL(peerIP, key string) string {
	target := url.URL{
		Scheme: p.scheme,
		Host:   net.JoinHostPort(peerIP, strconv.Itoa(p.port)),
		Path:   "/artifacts/steps/" + key,
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
	if receiptName != "" {
		if err := writeExclusiveFileAt(tmpHandle, receiptName, receiptContents, 0600); err != nil {
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
