// Package outputplane is the artifact daemon's output-plane half: the
// node-local publisher, capture routes and exact execution-control authority.
//
// It used to be its own binary and its own DaemonSet. It is now a part of
// cmd/artifact-daemon, served on that daemon's one listener under that
// daemon's one TLS configuration, and mounted only when the daemon is given
// --execution-control. Every warrant it verifies -- control and read -- is
// checked against the one Hangar key. The wire is unchanged: /execution/v1/*,
// /capture/v1/*, /input/v1/* and /read/v1/* keep their paths and their bodies.
//
// What it still holds no handle to is the point: no database handle, no list
// or delete capability over the output bucket, and no way to call the web.
package outputplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func nowUTC() time.Time { return time.Now().UTC() }

// Plane is the output plane as the artifact daemon mounts it.
type Plane struct {
	server  *Server
	store   *controlStore
	capture *CaptureLedger
	labeler *Labeler
}

// Patterns are the mux patterns the plane serves. They are prefixes and two
// exact paths, and none of them overlaps a route the artifact daemon serves
// itself: /healthz and /metrics stay the artifact daemon's.
var Patterns = []string{
	"/execution/v1/",
	"/capture/v1/",
	"/input/v1/",
	"/read/v1/",
	"/handshake",
	"/readyz",
}

// Open builds everything over the control store.
//
// nodes is the artifact daemon's Kubernetes client; nil means there is no node
// to label, which is how this runs outside a cluster. daemonCertificate is the
// DER of the certificate the daemon's mTLS listener serves with: the plane is
// TLS-only, so nil is refused, and every route but the node-local ones requires
// a verified client certificate that is not that one (see
// Server.RefuseDaemonCertificate).
func Open(ctx context.Context, config Config, nodes kubernetes.Interface, daemonCertificate []byte, out io.Writer) (_ *Plane, err error) {
	if len(daemonCertificate) == 0 {
		return nil, fmt.Errorf("%w: the output plane needs the daemon's mTLS (--tls-cert, "+
			"--tls-key, --tls-ca-cert): its off-node routes carry warrants, and over "+
			"plaintext those are interceptable inside their window",
			output.ErrIncomplete)
	}
	labeler := NewLabeler(nodes, config.NodeName)
	if labeler != nil {
		lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
		node, lookupErr := labeler.nodes.CoreV1().Nodes().Get(lookup, config.NodeName, metav1.GetOptions{})
		cancel()
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve the output plane's Node identity: %w", lookupErr)
		}
		if node.DeletionTimestamp != nil || node.UID == "" {
			return nil, errors.New("the output plane's Node is unavailable")
		}
		if config.NodeUID != "" && config.NodeUID != string(node.UID) {
			return nil, errors.New("the configured node UID differs from the Kubernetes Node UID")
		}
		config.NodeUID = string(node.UID)
	}
	daemon, err := Build(ctx, config)
	if err != nil {
		return nil, err
	}

	store, err := openControlStore(config.ControlDir)
	if err != nil {
		return nil, err
	}
	plane := &Plane{store: store, labeler: labeler}
	defer func() {
		if err != nil {
			_ = plane.Close()
		}
	}()

	base, err := OpenExecutionLedger(store, executioncontrol.NodeUID(config.NodeUID), nowUTC)
	if err != nil {
		return nil, err
	}
	// The source ledger belongs to the capture extension: it reads step markers,
	// and a daemon that captures nothing opens none. The route
	// table refuses every capture route on such a daemon before a handler could
	// reach this, so a nil here is unreachable rather than tolerated.
	if daemon.OutputEnabled() {
		terminations := config.Terminations
		switch {
		case terminations != nil:
		case nodes != nil:
			if config.PodTerminationsNamespace == "" {
				return nil, fmt.Errorf("%w: --pod-terminations-namespace is required: a capture seal "+
					"reads the task namespace's Pods on this node, and no other namespace's",
					output.ErrIncomplete)
			}
			terminations = NewNodePodTerminations(nodes, config.PodTerminationsNamespace, config.NodeName)
		case config.PodTerminationsDir != "":
			terminations = DeclaredPodTerminations(config.PodTerminationsDir)
		}
		plane.capture, err = OpenCaptureLedger(store, base, daemon, CaptureLedgerConfig{
			Node: executioncontrol.NodeUID(config.NodeUID), StepsDir: config.StepsDir,
			ScratchDir: config.ScratchDir, Terminations: terminations, SealWait: config.SealWait,
		})
		if err != nil {
			return nil, err
		}
	}

	// One verifier for every warrant the plane admits, over the one Hangar
	// key; it is stateless, and the control purposes' single use lives beside
	// the ledgers in the control directory.
	warrants, err := hangar.NewVerifier(config.Key, hangar.MaxWarrantTTL, nowUTC)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", output.ErrIncomplete, err)
	}
	spent, err := openSpentWarrants(store, nowUTC)
	if err != nil {
		return nil, err
	}

	plane.server = NewServerWithSpool(daemon, base, plane.capture, warrants, spent,
		config.PublishConcurrency)
	if err := plane.server.configureReads(config, store); err != nil {
		return nil, err
	}
	plane.server.RequireClientCertificates()
	plane.server.RefuseDaemonCertificate(daemonCertificate)

	fmt.Fprintf(out, "output plane mounted\n")
	fmt.Fprintf(out, "  node uid:         %s\n", config.NodeUID)
	fmt.Fprintf(out, "  control ledger:   %s\n", store.Path())
	if daemon.OutputEnabled() {
		namespace := daemon.Namespace()
		fmt.Fprintf(out, "  bucket:           %s\n", namespace.Bucket())
		fmt.Fprintf(out, "  key prefix:       %s\n", namespace.Prefix())
		fmt.Fprintf(out, "  derived scope:    %s\n", namespace.Scope())
	} else {
		fmt.Fprintf(out, "  capture:          exact execution control only; "+
			"durable output capture is NOT enabled on this node\n")
	}
	fmt.Fprintf(out, "  control API:      https, the control plane's client certificate "+
		"required (node-local routes exempt)\n")

	return plane, nil
}

// Handler serves the plane's routes. The artifact daemon mounts it under
// Patterns on its own mux.
func (plane *Plane) Handler() http.Handler { return plane.server.Handler() }

// Advertise puts the readiness labels on, LAST, after everything has built and the
// listener exists. A label advertised before the daemon can answer is a pod
// scheduled onto a node whose hold is refused on arrival.
func (plane *Plane) Advertise(ctx context.Context) error {
	return plane.labeler.Advertise(ctx, plane.server.daemon.OutputEnabled())
}

// Withdraw takes the labels off. Shutdown calls it before the listener closes:
// a node that still advertises a protocol it has stopped serving is where the
// scheduler sends the next capture.
func (plane *Plane) Withdraw(ctx context.Context) error { return plane.labeler.WithdrawAll(ctx) }

// Close releases the ledgers and the control store.
func (plane *Plane) Close() error {
	var err error
	if plane.capture != nil {
		err = errors.Join(err, plane.capture.Close())
	}
	if plane.store != nil {
		err = errors.Join(err, plane.store.Close())
	}

	return err
}
