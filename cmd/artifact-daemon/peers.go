package main

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
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
	scheme      string // "http" or "https"
	probeClient *http.Client
	fetchClient *http.Client
}

// PeerTLSConfig holds optional mTLS configuration for peer communication.
type PeerTLSConfig struct {
	CertPath   string
	KeyPath    string
	CACertPath string
}

// NewPeerResolver creates a PeerResolver that discovers peers via the
// given headless service's EndpointSlices. When tlsCfg is non-nil, peer
// communication uses HTTPS with mTLS.
func NewPeerResolver(logger lager.Logger, clientset kubernetes.Interface, namespace, service string, port int, myPodIP string, tlsCfg *PeerTLSConfig) *PeerResolver {
	scheme := "http"
	var probeTransport, fetchTransport http.RoundTripper

	if tlsCfg != nil && tlsCfg.CertPath != "" {
		clientCert, err := tls.LoadX509KeyPair(tlsCfg.CertPath, tlsCfg.KeyPath)
		if err != nil {
			logger.Error("failed-to-load-peer-client-cert", err)
		} else {
			caCertPEM, err := os.ReadFile(tlsCfg.CACertPath)
			if err != nil {
				logger.Error("failed-to-read-peer-ca-cert", err)
			} else {
				caPool := x509.NewCertPool()
				caPool.AppendCertsFromPEM(caCertPEM)
				tlsConfig := &tls.Config{
					Certificates: []tls.Certificate{clientCert},
					RootCAs:      caPool,
				}
				probeTransport = &http.Transport{TLSClientConfig: tlsConfig}
				fetchTransport = &http.Transport{TLSClientConfig: tlsConfig.Clone()}
				scheme = "https"
				logger.Info("peer-mtls-enabled")
			}
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
			url := peerURL(p.scheme, ip, p.port, "/artifacts/steps/", key)
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

	// The temp directory is a sibling of destPath, so the parent has to exist
	// before the first attempt and rename has to stay on one filesystem.
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create dest parent: %w", err)
	}

	// NOTE: the hardcoded "http" here is a known defect — Probe uses
	// p.scheme, so peer FETCH cannot work with TLS enabled. It is out of scope
	// for this track by explicit review decision and is preserved verbatim; only
	// the escaping changes.
	url := peerURL("http", peerIP, p.port, "/artifacts/steps/", key)

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

		// Extract into a sibling temp directory and promote by rename, the
		// same discipline DurableTier.Restore uses. Extracting straight into
		// destPath had two consequences: a refused archive left everything
		// written before the refusal sitting at the destination, and the next
		// retry then re-extracted over that residue and failed early with a
		// spurious "file exists" on a legitimate entry — so the error the
		// operator finally saw named the wrong cause entirely.
		parent, base, err := openParent(destPath)
		if err != nil {
			resp.Body.Close()
			return err
		}

		tmpDir, err := extractTarInto(resp.Body, parent, ".fetch-")
		resp.Body.Close()
		if err != nil {
			parent.Close()
			lastErr = err
			logger.Error("extract-failed", err, lager.Data{"attempt": attempt})
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			continue
		}

		// Clear any existing destination before promoting, the same way
		// copyArtifact does for the registry and filesystem resolve paths that
		// feed this identical dest. An earlier version treated a rename onto an
		// existing directory as success, copied from DurableTier.Restore — but
		// Restore's destination corresponds to a bucket key, so "either copy is
		// equally valid" holds there. This dest is chosen by the caller, so a
		// directory already sitting here is not necessarily this artifact, and
		// reporting success while delivering none of the fetched bytes is the
		// same lie as the nil return this track set out to remove.
		parent.RemoveAll(base)

		if err := parent.Rename(tmpDir, base); err != nil {
			parent.RemoveAll(tmpDir)
			parent.Close()

			// We cleared destPath ourselves a moment ago, so a directory there
			// NOW was created after that — which only a concurrent fetch of this
			// same key does, and its bytes are this same artifact. Keep theirs
			// rather than churning the directory under an in-flight reader.
			//
			// The timing is what makes this sound. An earlier revision tolerated
			// a PRE-EXISTING destination the same way, copying the policy from
			// DurableTier.Restore, and that was wrong: a directory already there
			// before we started is not necessarily this artifact, and treating it
			// as success delivered stale bytes under a nil error. Clearing first
			// and only then tolerating a reappearance separates the two cases.
			if errors.Is(err, os.ErrExist) || isNotEmptyErr(err) {
				logger.Info("raced-by-concurrent-fetch", lager.Data{"attempt": attempt})
				return nil
			}

			lastErr = fmt.Errorf("promote extracted artifact: %w", err)
			logger.Error("rename-failed", err, lager.Data{"attempt": attempt})
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			continue
		}

		parent.Close()
		logger.Info("fetched", lager.Data{"attempt": attempt})
		return nil
	}

	return fmt.Errorf("peer fetch failed after 3 attempts: %w", lastErr)
}

// extractTarToDir reads a tar stream and extracts files to destDir.
// extractTarToDir is the path-taking wrapper. Kept for callers that own a
// directory rather than a handle; the real work is in extractTarToRoot.
//
// Its own failures are ENVIRONMENT-attributable — a caller that cannot create
// or open its own destination is not being told something about the archive —
// so they are deliberately NOT marked with ErrRefused.
// extractTarInto extracts an archive into a fresh directory under parent and
// returns its name. The caller renames it into place.
//
// parent is a HANDLE, and the extraction root is derived from it with
// OpenRoot rather than reopened from a path. The previous version took destDir
// as a string, os.MkdirAll'd it and os.OpenRoot'd it — both ambient — so a
// symlink at destDir anchored the "contained" root outside the store and every
// write inside it landed there. os.Root contains operations within a root; it
// cannot contain the choice of root.
func extractTarInto(r io.Reader, parent *os.Root, prefix string) (string, error) {
	tmp, err := mkdirTempIn(parent, prefix)
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	root, err := parent.OpenRoot(tmp)
	if err != nil {
		parent.RemoveAll(tmp)
		return "", fmt.Errorf("open temp dir: %w", err)
	}
	defer root.Close()

	if err := extractTarToRoot(r, root); err != nil {
		parent.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

// extractTarToRoot extracts into a root the CALLER owns.
//
// Taking the handle rather than a path is the point: a caller that already
// holds a nested boundary — stream-in, on root.OpenRoot("steps") — must not
// have to re-join a path to call this, because "the boundary as an argument" is
// the shape that let an earlier track validate against the wrong root.
func extractTarToRoot(r io.Reader, root *os.Root) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return refused("reading tar: %w", err)
		}

		// Normalize permissions: strip setuid/setgid, enforce minimum readable floor.
		// .Perm() masks to the low 9 bits before the mode reaches os.Root, which
		// rejects a FileMode carrying anything else ("unsupported file mode").
		// This is not a behaviour change: hdr.Mode is a TAR mode, where setuid is
		// 0o4000, while os.ModeSetuid is bit 23 — so os.FileMode(hdr.Mode) never
		// carried a flag os.OpenFile would act on, and its syscallMode() already
		// reduced to exactly these bits. The stray 0o4000 was simply discarded
		// later instead of earlier.
		mode := sanitizeMode(hdr.Typeflag, os.FileMode(hdr.Mode)).Perm()

		// Every failure below fails the WHOLE extraction. A skipped entry
		// produces a tree the caller believes is complete, which is the same
		// class of defect as the discarded errors this replaced.
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(hdr.Name, mode); err != nil {
				return refused("create dir %q: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if dir := path.Dir(hdr.Name); dir != "." {
				if err := root.MkdirAll(dir, 0755); err != nil {
					return refused("create parent of %q: %w", hdr.Name, err)
				}
			}
			f, err := root.OpenFile(hdr.Name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return refused("create file %q: %w", hdr.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				// A truncated archive surfaces HERE, not from tr.Next — measured
				// at five truncation offsets, four of which failed in Copy. It is
				// the likeliest corrupt input from a flaky or hostile peer, and
				// reporting it as environment failure makes mirror.go read it as
				// a PEER fault.
				if errors.Is(err, io.ErrUnexpectedEOF) {
					f.Close()
					return refused("write file %q: %w", hdr.Name, err)
				}
				f.Close()
				return fmt.Errorf("write file %q: %w", hdr.Name, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close file %q: %w", hdr.Name, err)
			}
		case tar.TypeSymlink:
			// os.Root will not FOLLOW a link out of the root, but it does not
			// validate the link's own target — so without this the extraction
			// stays safe while leaving an outward-pointing link on disk for the
			// next consumer to follow.
			if err := validateSymlinkTarget(hdr.Name, hdr.Linkname); err != nil {
				return err
			}
			if dir := path.Dir(hdr.Name); dir != "." {
				if err := root.MkdirAll(dir, 0755); err != nil {
					return refused("create parent of symlink %q: %w", hdr.Name, err)
				}
			}
			if err := root.Symlink(hdr.Linkname, hdr.Name); err != nil {
				return refused("create symlink %q -> %q: %w", hdr.Name, hdr.Linkname, err)
			}
		case tar.TypeLink:
			// A hard link's target is a path inside the archive, so it takes
			// the same containment rule as a symlink target. Previously this
			// case did not exist: the entry fell through the switch, was
			// dropped, and the extraction still reported success — handing the
			// caller a tree missing files it asked for.
			//
			// The target is resolved against the ARCHIVE ROOT, not against the
			// entry's own directory. That is what root.Link below does with it,
			// and validating it any other way makes validation and use disagree
			// — the first version passed hdr.Name here, so a legal archive with
			// "target.txt" at the root and a hard link at "a/b/link" was
			// validated as "a/b/target.txt" and wrongly refused. Unreachable
			// while stream-in dropped TypeLink silently; reachable now.
			if err := validateSymlinkTarget(".", hdr.Linkname); err != nil {
				return err
			}
			if dir := path.Dir(hdr.Name); dir != "." {
				if err := root.MkdirAll(dir, 0755); err != nil {
					return refused("create parent of hard link %q: %w", hdr.Name, err)
				}
			}
			if err := root.Link(hdr.Linkname, hdr.Name); err != nil {
				return refused("create hard link %q -> %q: %w", hdr.Name, hdr.Linkname, err)
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// pax metadata, not a filesystem entry. Go's reader applies these
			// to subsequent headers itself; there is nothing to materialize and
			// skipping them loses nothing.
			continue
		default:
			// Everything else — character and block devices, FIFOs, sockets,
			// unknown vendor types. An artifact has no business carrying them,
			// and the daemon has no way to materialize one safely. Refuse
			// rather than skip: a silent drop hands back a tree the caller
			// believes is complete, which is the defect this function's error
			// propagation exists to prevent.
			return refused("entry %q has unsupported type %q", hdr.Name, string(rune(hdr.Typeflag)))
		}
	}
	return nil
}
