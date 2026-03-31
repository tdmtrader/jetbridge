package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	probeClient *http.Client
	fetchClient *http.Client
}

// NewPeerResolver creates a PeerResolver that discovers peers via the
// given headless service's EndpointSlices.
func NewPeerResolver(logger lager.Logger, clientset kubernetes.Interface, namespace, service string, port int, myPodIP string) *PeerResolver {
	return &PeerResolver{
		logger:    logger,
		clientset: clientset,
		namespace: namespace,
		service:   service,
		port:      port,
		myPodIP:   myPodIP,
		probeClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		fetchClient: &http.Client{
			Timeout: 3 * time.Minute,
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
			url := fmt.Sprintf("http://%s:%d/artifacts/steps/%s", ip, p.port, key)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
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
	logger := p.logger.Session("peer-fetch", lager.Data{"key": key, "peer": peerIP, "dest": destPath})

	url := fmt.Sprintf("http://%s:%d/artifacts/steps/%s", peerIP, p.port, key)

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		resp, err := p.fetchClient.Do(req)
		if err != nil {
			lastErr = err
			logger.Error("fetch-attempt-failed", err, lager.Data{"attempt": attempt})
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("peer returned %d", resp.StatusCode)
			logger.Error("fetch-bad-status", lastErr, lager.Data{"attempt": attempt})
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			continue
		}

		// Stream response (tar) to a temp file, then extract.
		err = extractTarToDir(resp.Body, destPath)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			logger.Error("extract-failed", err, lager.Data{"attempt": attempt})
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			continue
		}

		logger.Info("fetched", lager.Data{"attempt": attempt})
		return nil
	}

	return fmt.Errorf("peer fetch failed after 3 attempts: %w", lastErr)
}

// extractTarToDir reads a tar stream and extracts files to destDir.
func extractTarToDir(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		target := filepath.Join(destDir, hdr.Name)

		// Prevent path traversal.
		rel, err := filepath.Rel(destDir, target)
		if err != nil || len(rel) >= 2 && rel[:2] == ".." {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, os.FileMode(hdr.Mode))
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Symlink(hdr.Linkname, target)
		}
	}
	return nil
}
