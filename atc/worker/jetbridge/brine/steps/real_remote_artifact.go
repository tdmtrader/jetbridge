package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"k8s.io/client-go/kubernetes"
)

// tcpRoute owns its listener and every accepted connection. Closing a route
// cancels forwarding and waits for its goroutines before daemon disposal.
type tcpRoute struct {
	net.Listener
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	once     sync.Once
	closeErr error
}

func (r *tcpRoute) Close() error {
	r.once.Do(func() {
		r.closeErr = r.Listener.Close()
		r.cancel()
		r.wg.Wait()
	})
	return r.closeErr
}

// routeWithDrops never parses HTTP or generates a response. Faulted connections
// are closed after the client sends a byte; others reach the real daemon.
// The remaining-drop count drives faults only, never an assertion.
func routeWithDrops(address, target string, dropFirst int, alwaysDrop bool) (net.Listener, error) {
	if dropFirst < 0 {
		return nil, fmt.Errorf("negative connection-drop count")
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for daemon route %s: %w", address, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	route := &tcpRoute{Listener: ln, cancel: cancel}
	route.wg.Add(1)
	go func() {
		defer route.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			drop := alwaysDrop || dropFirst > 0
			if dropFirst > 0 {
				dropFirst--
			}
			route.wg.Add(1)
			go func() {
				defer route.wg.Done()
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				if drop {
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					var first [1]byte
					_, _ = conn.Read(first[:])
					if tcp, ok := conn.(*net.TCPConn); ok {
						_ = tcp.SetLinger(0)
					}
					return
				}
				forwardConn(ctx, conn, target)
			}()
		}
	}()
	return route, nil
}

func (r *RemoteArtifact) daemon(rec *brine.Recorder) (*jetbridge.DaemonSetVolume, error) {
	if r.Forgotten {
		return jetbridge.NewDaemonSetVolume(r.Key, r.Key, "k8s-worker-1", nil, "",
			jetbridge.NewConfig("", ""), nil), nil
	}
	if !filepath.IsLocal(r.Key) || !filepath.IsLocal(r.FileName) {
		return nil, fmt.Errorf("artifact key and member must stay inside the owned daemon root")
	}
	ctx, cancel := context.WithTimeout(execLogger("live-remote-artifact"), 3*time.Minute)
	rec.RegisterDisposer(cancel)
	r.Ctx = ctx
	d, err := newLiveArtifactDaemon(ctx, rec)
	if err != nil {
		return nil, err
	}
	r.nodeName = d.pod.Spec.NodeName
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	seed := func(endpoint, content string) error {
		archive, err := plainTarOfOneFile(r.FileName, content)
		if err != nil {
			return err
		}
		_, err = readDaemonHTTP(ctx, client, http.MethodPut, endpoint+"/stream-in/"+r.Key,
			bytes.NewReader(archive), http.StatusCreated)
		return err
	}
	if err := seed(d.url(), r.Content); err != nil {
		return nil, err
	}
	if err := registerDaemonArtifact(ctx, client, d.url(), r.Key, filepath.Join(d.store.root, "steps", r.Key)); err != nil {
		return nil, err
	}
	r.expectedRaw, err = readDaemonHTTP(ctx, client, http.MethodGet, d.url()+"/artifacts/"+r.Key, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if r.Fallback {
		peerURL, err := startLiveArtifactPeer(ctx, rec, d)
		if err != nil {
			return nil, err
		}
		if r.Mirror != "" {
			if err := seed(peerURL, r.Mirror); err != nil {
				return nil, err
			}
			r.expectedRaw, err = readDaemonHTTP(ctx, client, http.MethodGet, peerURL+"/artifacts/steps/"+r.Key, nil, http.StatusOK)
			if err != nil {
				return nil, err
			}
		}
	}
	if r.ServerError {
		// A real unreadable legacy file shadows the registered alias. Both
		// pods drop DAC_OVERRIDE; the production daemon produces the 500.
		archive, err := plainTarOfOneFile(r.FileName, r.Content)
		if err != nil {
			return nil, err
		}
		path := filepath.Join("/store", r.Key)
		if _, err := d.store.exec(ctx, d.store.observer.Name,
			[]string{"sh", "-ec", "cat > \"$1\"; chmod 000 \"$1\"; if cat \"$1\" >/dev/null 2>&1; then echo \"owned artifact unexpectedly readable\" >&2; exit 1; fi", "unreadable-artifact", path},
			bytes.NewReader(archive)); err != nil {
			return nil, fmt.Errorf("make owned artifact unreadable: %w", err)
		}
		if _, err := readDaemonHTTP(ctx, client, http.MethodGet, d.url()+"/artifacts/"+r.Key, nil, http.StatusInternalServerError); err != nil {
			return nil, fmt.Errorf("real daemon must report storage failure: %w", err)
		}
	}
	if r.Refused {
		// Peer provisioning needs the producer executable, so stop it last.
		// close verifies actual ECONNREFUSED; the existing Node stays intact.
		if err := d.close(ctx); err != nil {
			return nil, err
		}
	}
	producer := net.JoinHostPort(d.nodeIP, fmt.Sprint(d.port))
	routeAddress := ""
	if r.DropFirst > 0 || r.NeverAnswers {
		route, err := routeWithDrops("127.0.0.1:0", producer, r.DropFirst, r.NeverAnswers)
		if err != nil {
			return nil, err
		}
		rec.RegisterDisposer(func() {
			if err := route.Close(); err != nil {
				panic(err)
			}
		})
		routeAddress = route.Addr().String()
	}
	r.nodeReads = new(execObservation)
	cs, err := kubernetes.NewForConfig(r.nodeReads.config(d.store.cluster.Config))
	if err != nil {
		return nil, err
	}
	cfg := jetbridge.NewConfig(d.store.cluster.Namespace, "")
	cfg.ArtifactDaemonPort = int(d.port)
	var volume *jetbridge.DaemonSetVolume
	err = constructThroughTCPRoute(producer, routeAddress, func() error {
		var observeErr error
		r.trace, observeErr = observeDaemonConstruction(map[string]bool{producer: true}, func() {
			volume = jetbridge.NewDaemonSetVolume(r.Key, r.Key, "k8s-worker-1", nil,
				r.nodeName, cfg, jetbridge.NewNodeIPResolver(cs))
		})
		return observeErr
	})
	if err != nil {
		return nil, err
	}
	if r.Fallback {
		volume.SetDaemonClient(jetbridge.NewDaemonClient(lagertest.NewTestLogger("brine-remote-artifact"),
			d.store.cluster.Clientset, cfg.Namespace, livePeerService, int(d.port), nil))
	}
	return volume, nil
}

// constructThroughTCPRoute changes only the selected destination's TCP path.
// The route forwards unchanged bytes to the real node or closes connections;
// URL/Host and the production node-name resolver remain unchanged. The separate
// observer stays passive. Restore the default before I/O and on panic.
func constructThroughTCPRoute(address, route string, construct func() error) error {
	if route == "" {
		return construct()
	}
	original, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("expected standard HTTP transport, got %T", http.DefaultTransport)
	}
	transport := original.Clone()
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target == address {
			target = route
		}
		return dial(ctx, network, target)
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	return construct()
}

// remoteArtifactRead keeps transport errors distinct from decoding/assertion
// errors. A successful empty HTTP response must not satisfy a failed-open test.
type remoteArtifactRead struct {
	expected, received        []byte
	requests, minimumRequests int
	observationErr            error
}

func (r RemoteArtifact) read(volume *jetbridge.DaemonSetVolume) VolumeRead {
	observation := &remoteArtifactRead{expected: r.expectedRaw, minimumRequests: r.DropFirst + 1}
	if r.NeverAnswers {
		observation.minimumRequests = 3
	}
	out := VolumeRead{remote: observation}
	for _, encoding := range []struct {
		name  string
		value compression.Compression
	}{
		{"raw", nil}, {"gzip", compression.NewGzipCompression()},
	} {
		stream, openErr := volume.StreamOut(r.Ctx, ".", encoding.value)
		attempt := volumeReadAttempt{encoding: encoding.name, openErr: openErr}
		if stream != nil {
			if openErr == nil {
				if encoding.name == "raw" {
					observation.received, attempt.readErr = io.ReadAll(stream)
				} else {
					out.Files, attempt.readErr = filesInGzippedTar(stream)
				}
			}
			attempt.closeErr = stream.Close()
		} else if openErr == nil {
			attempt.readErr = fmt.Errorf("StreamOut returned no reader")
		}
		out.readAttempts = append(out.readAttempts, attempt)
		out.Err = errors.Join(out.Err, attempt.openErr, attempt.readErr, attempt.closeErr)
		if encoding.name == "raw" && r.trace != nil {
			requests, err := r.trace.requests()
			observation.observationErr = err
			for _, sent := range requests {
				observation.requests += len(sent)
			}
		}
	}
	observation.observationErr = errors.Join(observation.observationErr, r.nodeReads.requireNodeRead(r.nodeName))
	if out.Err != nil {
		out.Message = out.Err.Error()
	}
	return out
}

func (r *remoteArtifactRead) requireBytesAndRetries() error {
	if r.observationErr != nil {
		return r.observationErr
	}
	if len(r.expected) == 0 || !bytes.Equal(r.received, r.expected) {
		return fmt.Errorf("daemon raw bytes differ: received %d, expected %d", len(r.received), len(r.expected))
	}
	if r.requests < r.minimumRequests {
		return fmt.Errorf("expected at least %d actual daemon requests, observed %d", r.minimumRequests, r.requests)
	}
	return nil
}
