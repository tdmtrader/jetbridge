// Command hangar-output-daemon is the output plane's node-local publisher,
// receipt signer and control authority.
//
// It is a separate binary and a separate DaemonSet from cmd/artifact-daemon
// because a Kubernetes service account is Pod-wide: giving the existing daemon
// an output-bucket role would give its cache and strict-input identity the same
// role, and no amount of care inside one process takes that back. This binary
// links a publisher client and nothing else -- no cache client, no strict-input
// client, no list or delete capability, no database handle.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func nowUTC() time.Time { return time.Now().UTC() }

func main() {
	config := Config{}
	BindFlags(flag.CommandLine, &config)
	flag.Parse()

	if err := run(context.Background(), config, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "hangar-output-daemon: %v\n", err)
		os.Exit(1)
	}
}

// run builds everything and serves.
//
// The order is the fail-closed rule made concrete: the control store is opened
// and VALIDATED before the server exists, and a quarantined record makes the
// daemon start and stay unready rather than start and answer. A daemon that
// could not read its own ledger is not one that may say what it says.
func run(ctx context.Context, config Config, out *os.File) error {
	daemon, err := Build(ctx, config)
	if err != nil {
		return err
	}

	store, err := openControlStore(config.ControlDir)
	if err != nil {
		return err
	}
	defer store.Close()

	unready := ""
	quarantined, err := store.validate()
	if err != nil {
		return err
	}
	if len(quarantined) != 0 {
		unready = fmt.Sprintf("the output control ledger quarantined %d unreadable record(s) at "+
			"startup: %v. This daemon is the sole authority for the executions they described, "+
			"and a lost record is not an execution that never happened. It will stay unready "+
			"until an operator resolves them under %s/%s.",
			len(quarantined), quarantined, store.Path(), quarantineDirName)
	}

	base, err := OpenExecutionLedger(store, executioncontrol.NodeUID(config.NodeUID),
		daemon.ActivationEpoch(), daemon.ControlSigner(), nowUTC)
	if err != nil {
		return err
	}
	// The source ledger belongs to the OUTPUT facet: it holds capture
	// incarnations, and a daemon that captures nothing opens none. The route
	// table refuses every capture route on such a daemon before a handler could
	// reach this, so a nil here is unreachable rather than tolerated.
	var source *SourceLedger
	if daemon.OutputEnabled() {
		source, err = OpenSourceLedger(store, base, executioncontrol.NodeUID(config.NodeUID),
			daemon.ActivationEpoch(), daemon.CaptureSigner(), nowUTC, config.StepsDir)
		if err != nil {
			return err
		}
		defer source.Close()
	}

	secret, err := os.ReadFile(config.CapabilityKeyFile)
	if err != nil {
		return fmt.Errorf("%w: reading the capability key: %v", output.ErrIncomplete, err)
	}
	capability, err := executioncontrol.NewCapabilityVerifier(secret, config.CapabilityTTL, nowUTC)
	if err != nil {
		return err
	}
	// The spent nonces live in the same control directory the ledgers do. A
	// verifier that kept them in memory would be one restart away from
	// admitting a captured capability a second time inside its TTL.
	if err := capability.RememberSpentIn(capabilityReplayStore{store: store}); err != nil {
		return err
	}

	server := NewServerWithSpool(daemon, base, source, capability, unready,
		config.PublishConcurrency)

	// The first off-node caller landed in Phase 4: execProcess revalidates the
	// hold, takes writer tickets, records the start and the outcome, and asks
	// about cleanup -- all from the web pod, which is on another node. So the
	// control API is mTLS when it is configured for it, and every route but
	// the node-local capture hold requires a verified client certificate. The
	// hold's caller is the capture control init inside a Pod on this node; it
	// holds no client certificate and Req 24 will not give it one, which is
	// the same exemption cmd/artifact-daemon makes for /resolve.
	var tlsConfig *tls.Config
	if config.TLSEnabled() {
		built, err := buildControlTLSConfig(config)
		if err != nil {
			return err
		}
		tlsConfig = built
		server.RequireClientCertificates()
	}

	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return fmt.Errorf("%w: listening on %s: %v", output.ErrInfrastructure,
			config.ListenAddress, err)
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}

	fmt.Fprintf(out, "hangar-output-daemon listening on %s\n", listener.Addr())
	fmt.Fprintf(out, "  activation epoch: %d\n", daemon.ActivationEpoch())
	fmt.Fprintf(out, "  node uid:         %s\n", config.NodeUID)
	fmt.Fprintf(out, "  control ledger:   %s\n", store.Path())
	fmt.Fprintf(out, "  control key:      %s (public key %x)\n",
		config.ControlKeyID, daemon.ControlPublicKey()[:8])
	if daemon.OutputEnabled() {
		namespace := daemon.Namespace()
		fmt.Fprintf(out, "  bucket:           %s\n", namespace.Bucket())
		fmt.Fprintf(out, "  key prefix:       %s\n", namespace.Prefix())
		fmt.Fprintf(out, "  derived scope:    %s\n", namespace.Scope())
		fmt.Fprintf(out, "  receipt key:      %s (public key %x)\n",
			config.ReceiptKeyID, daemon.ReceiptPublicKey()[:8])
		fmt.Fprintf(out, "  materialize key:  %s\n", config.MaterializationKeyID)
	} else {
		fmt.Fprintf(out, "  facets:           base exact-execution-control only; "+
			"durable output capture is NOT enabled on this node\n")
	}
	if tlsConfig != nil {
		fmt.Fprintf(out, "  control API:      https, client certificate required "+
			"(node-local capture hold exempt)\n")
	} else {
		fmt.Fprintf(out, "  control API:      http, node-local only\n")
	}
	if unready != "" {
		fmt.Fprintf(out, "\nNOT READY: %s\n", unready)
	}

	// The two ready labels go on LAST, after everything above has built and the
	// listener exists. A label advertised before the daemon can answer is a pod
	// scheduled onto a node whose hold is refused on arrival, and the whole
	// point of the hint is that it does not send work somewhere it cannot land.
	//
	// A quarantined ledger advertises nothing at all: readiness is false, and
	// telling the scheduler otherwise would be this daemon's one visible claim
	// contradicting its own /readyz.
	labeler, err := buildFacetLabeler(config)
	if err != nil {
		return err
	}
	if unready == "" {
		if err := labeler.Advertise(ctx, daemon.OutputEnabled()); err != nil {
			return err
		}
	}

	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}

	// Shutdown takes the labels off before the listener closes, and in that
	// order: a node that still advertises a facet it has stopped serving is
	// where the scheduler sends the next capture.
	//
	// signal.Notify rather than a shell trap, and SIGTERM explicitly, because
	// SIGTERM is what a kubelet sends and a disposition inherited from the
	// process that started this one is not something a handler can recover.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signals
		withdrawal, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := labeler.WithdrawAll(withdrawal); err != nil {
			fmt.Fprintf(os.Stderr, "hangar-output-daemon: withdrawing node labels: %v\n", err)
		}
		_ = httpServer.Shutdown(withdrawal)
	}()

	if err := httpServer.Serve(listener); err != nil &&
		!errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// buildFacetLabeler connects to the API server only when there is a node to
// label. Outside a cluster -- the conformance tier, this package's own tests --
// there is none, and that is a configuration rather than a failure.
func buildFacetLabeler(config Config) (*FacetLabeler, error) {
	if config.NodeName == "" {
		return nil, nil
	}
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("%w: this daemon was given --node-name %q and cannot reach the "+
			"Kubernetes API to advertise its facets: %v",
			output.ErrInfrastructure, config.NodeName, err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: building the Kubernetes client: %v",
			output.ErrInfrastructure, err)
	}

	return NewFacetLabeler(client, config.NodeName), nil
}

// buildControlTLSConfig is the server half of the ATC's own client-certificate
// plumbing, spelled the way cmd/artifact-daemon spells it.
//
// ClientAuth is VerifyClientCertIfGiven rather than RequireAndVerify because
// the node-local capture hold, /healthz, /readyz and /handshake are reachable
// without one; the per-route check in routes.go is what refuses a control-plane
// operation that arrives without a verified certificate. Requiring it at the
// handshake would take the hold away from the init container that has to make
// it.
func buildControlTLSConfig(config Config) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("%w: loading the control API server certificate: %v",
			output.ErrIncomplete, err)
	}
	caPEM, err := os.ReadFile(config.TLSCACert)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the control API client CA: %v",
			output.ErrIncomplete, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: no certificates in %s", output.ErrCorrupt, config.TLSCACert)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, nil
}
