package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/concourse/concourse/artifactcap"
	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"

	"code.cloudfoundry.org/lager/v3"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	port := flag.Int("port", 7780, "HTTP server port")
	listenAddress := flag.String("listen-address", "", "Address to bind the HTTP server to. Empty means every address, which is what the daemon does in a pod: one network namespace, one address, one daemon. Set it to hold a single address instead — for running two daemons on one host, where the kernel will only let two listeners share a port if each holds a different, specific address.")
	metricsPort := flag.Int("metrics-port", 0, "Port for a plain-HTTP listener that serves /metrics and nothing else, for a Prometheus scraper that cannot complete the mTLS port's handshake. 0 opens no listener. Bound on --listen-address, like --port.")
	storagePath := flag.String("storage-path", "/var/concourse/artifacts", "Path to artifact storage directory")
	ttl := flag.Duration("ttl", 2*time.Hour, "TTL for artifact cleanup sweep")
	shutdownTimeout := flag.Duration("shutdown-timeout", 25*time.Second, "How long SIGTERM may take: in-flight requests (an output-plane publish among them) drain first, then mirror jobs, within this. Keep it under the pod's terminationGracePeriodSeconds.")
	resolveCapabilityKeyFile := flag.String("resolve-capability-key", "", "Path to the raw 32-byte key required to authorize resolve operations")
	nodeName := flag.String("node-name", "", "Kubernetes node name (for node labeling)")
	peerDiscovery := flag.Bool("peer-discovery", false, "Enable EndpointSlice peer discovery without node labeling; this also enables outbound mirroring (--mirror-replicas, default 2); --node-name continues to enable both")
	namespace := flag.String("namespace", "default", "Kubernetes namespace")
	kubeconfig := flag.String("kubeconfig", "", "Path to a kubeconfig file. When empty the in-cluster config is used, which is what the daemon does in a pod. Set this to run the daemon against a cluster from outside one — for debugging against a live cluster, and for tests that need two daemons able to discover each other.")
	serviceName := flag.String("service-name", "artifact-daemon", "Headless service name for EndpointSlice peer discovery")
	labelKey := flag.String("label-key", "concourse.dev/artifact-cache", "Node label key to set on startup")
	tlsCert := flag.String("tls-cert", "", "Path to TLS server certificate (enables HTTPS with mTLS)")
	tlsKey := flag.String("tls-key", "", "Path to TLS server private key")
	tlsCACert := flag.String("tls-ca-cert", "", "Path to CA certificate for verifying client certificates")
	mirrorReplicas := flag.Int("mirror-replicas", 2, "Replication factor for outbound mirror: 0=disabled, N=local + (N-1) peers, -1=all peers")
	mirrorConcurrency := flag.Int("mirror-concurrency", 4, "Max concurrent in-flight mirror jobs")
	mirrorTimeout := flag.Duration("mirror-timeout", 5*time.Minute, "Per-peer per-job mirror PUT timeout")

	// Durable tier. Off unless --durable-store names a backend, and the daemon
	// behaves exactly as before when it is off.
	durableStore := flag.String("durable-store", "", "Fail-open store for resource caches: \"\" (disabled), \"gcs\" or \"disk\"")
	durableBucket := flag.String("durable-bucket", "", "The cache's own GCS bucket or disk namespace; never the strict-input or output one")
	durableEndpoint := flag.String("durable-endpoint", "", "GCS emulator endpoint (empty is real GCS), or the disk store's HTTPS origin")
	durableStoreID := flag.String("durable-store-id", "", "Expected persistent disk storage identity, for --durable-store=disk")
	durableTokenFile := flag.String("durable-token-file", "", "Disk storage cache-role credential file, for --durable-store=disk")
	durableCACert := flag.String("durable-ca-cert", "", "Disk storage CA certificate, for --durable-store=disk")
	durableTimeout := flag.Duration("durable-timeout", 5*time.Minute, "Per-operation timeout for the durable store")
	durableMaintenanceInterval := flag.Duration("durable-maintenance-interval", defaultMaintenanceInterval, "How often to walk the durable store to reclaim expired objects and measure what remains. Every daemon runs its own enumeration, and a List is billed per page, so this is deliberately slow.")

	// Retention is JetBridge's own, not a bucket lifecycle rule, so the period
	// lives here rather than as a string an operator types into a cloud console
	// that has to match a prefix this code composes. A class with no entry is
	// kept forever.
	var durableRetention RetentionPolicy
	flag.Var(&durableRetention, "durable-retention", "Retention for one class of durable artifact, as CLASS=DURATION (e.g. resource-caches=720h). Repeatable. A class with no entry is never reclaimed.")
	durableMaxBytes := flag.Int64("durable-max-bytes", 5<<30, "Largest single artifact to store durably; 0 disables the limit")

	// Hangar is a strict immutable-tree service composed beside the fail-open
	// cache tier. It names its own store and shares no setting, client or
	// namespace with the cache.
	hangarStore := flag.String("hangar-store", "", "Strict-input storage profile: gcs or disk")
	hangarBucket := flag.String("hangar-bucket", "", "Strict-input bucket or disk namespace")
	hangarEndpoint := flag.String("hangar-endpoint", "", "Strict-input storage endpoint")
	hangarPrefix := flag.String("hangar-prefix", "", "Strict-input object prefix; empty selector inherits durable prefix")
	hangarStoreID := flag.String("hangar-store-id", "", "Expected persistent disk storage identity")
	hangarTokenFile := flag.String("hangar-token-file", "", "Disk storage input credential file")
	hangarCACert := flag.String("hangar-ca-cert", "", "Disk storage CA certificate")
	hangarEnabled := flag.Bool("hangar-enabled", false, "Enable strict Hangar tree publication and materialization")
	hangarScratchDir := flag.String("hangar-scratch-dir", "/var/concourse/hangar-scratch", "Absolute private scratch directory for Hangar verification")
	hangarWarrantKey := flag.String("hangar-warrant-key", "", "Path to the raw 32-byte materialization warrant key")
	hangarWarrantTTL := flag.Duration("hangar-warrant-ttl", 15*time.Minute, "Maximum accepted Hangar materialization warrant lifetime")
	// The pre-rename spellings stay accepted so a chart or operator still
	// passing them keeps working; both names write the same variable.
	flag.StringVar(hangarWarrantKey, "hangar-capability-key", "", "Deprecated alias for --hangar-warrant-key")
	flag.DurationVar(hangarWarrantTTL, "hangar-capability-ttl", 15*time.Minute, "Deprecated alias for --hangar-warrant-ttl")
	hangarMaxContentBytes := flag.Int64("hangar-max-content-bytes", 10<<30, "Maximum regular-file content admitted in one Hangar tree")
	hangarMaxEntries := flag.Int64("hangar-max-entries", 100000, "Maximum filesystem entries admitted in one Hangar tree")

	// The output plane: exact execution control, and with --output-bucket the
	// durable-capture extension. Mounted on this daemon's listener when
	// --capability-key is given (the base facet cannot verify a capability
	// without it); its control and steps directories are this daemon's storage
	// root and its steps/ beneath it.
	var planeConfig outputplane.Config
	outputplane.BindFlags(flag.CommandLine, &planeConfig)

	flag.Parse()

	logger := lager.NewLogger("artifact-daemon")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))

	// Decided before anything with a side effect — the node labels below —
	// so a refused configuration has nothing to clean up.
	tlsEnabled, err := daemonTLSMode(*tlsCert, *tlsKey, *tlsCACert)
	if err != nil {
		logger.Error("tls-config-invalid", err)
		os.Exit(1)
	}

	// Loaded before the node is labelled for the same reason. Build pods are
	// scheduled only onto nodes carrying the readiness label, so a daemon that
	// labelled first and then died on an unreadable key kept attracting pods
	// through every restart of its crash loop — and exited without removing
	// the label, since this exit ran no cleanup.
	resolveCapabilityKey, err := loadResolveCapabilityKey(*resolveCapabilityKeyFile)
	if err != nil {
		logger.Error("failed-to-load-resolve-capability-key", err)
		os.Exit(1)
	}

	// How the daemon reaches the cluster is an INPUT, resolved once here and
	// handed to everything that needs it: the node labelers and, below, peer
	// discovery. Two adapters sit on this one seam — the in-cluster config in a
	// pod, an explicit --kubeconfig from outside one — and the modules that
	// consume the client (NodeLabeler, PeerResolver) never learn which. A
	// daemon with neither --node-name nor --peer-discovery labels nothing and
	// has no peers, so it builds no client at all. --peer-discovery asks for
	// the client and the peers without the labeling: the brine live tier runs
	// the daemon with namespace-local read access only, which cannot patch a
	// node.
	var labeler *NodeLabeler
	var hangarLabeler *NodeLabeler
	var k8sClient kubernetes.Interface
	if daemonClientNeeded(*nodeName, *peerDiscovery) {
		var err error
		k8sClient, err = buildK8sClient(*kubeconfig)
		if err != nil {
			logger.Error("failed-to-create-k8s-client", err)
			os.Exit(1)
		}
	}
	if *nodeName != "" {
		labeler = NewNodeLabeler(logger, k8sClient, *nodeName, *labelKey)
		hangarLabeler = NewNodeLabeler(logger, k8sClient, *nodeName, HangarReadyLabel)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := prepareDaemonLabels(ctx, *labelKey, hangarLabeler, labeler); err != nil {
			cancel()
			logger.Error("failed-to-prepare-node-labels", err)
			os.Exit(1)
		}
		cancel()
		logger.Info("node-labeled", lager.Data{"node": *nodeName, "label": *labelKey})
	} else {
		logger.Info("skipping-node-labeling", lager.Data{"reason": "no --node-name provided"})
	}

	// The daemon now creates its own storage directory. New production
	// behaviour, and deliberate: the storage root is acquired at construction,
	// and a missing hostPath would otherwise turn a first boot into a startup
	// failure. The chart's hostPath uses DirectoryOrCreate, so the kubelet
	// normally makes it — this covers the cases where it has not.
	if err := os.MkdirAll(*storagePath, 0755); err != nil {
		logger.Error("failed-to-create-storage-path", err, lager.Data{"path": *storagePath})
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, func() error { return nil })
		cleanupCancel()
		os.Exit(1)
	}

	server, err := NewServer(logger, *storagePath, *nodeName)
	if err != nil {
		logger.Error("failed-to-open-storage-root", err, lager.Data{"path": *storagePath})
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, func() error { return nil })
		cleanupCancel()
		os.Exit(1)
	}
	closeHangar := func() error { return nil }

	// Set up alias persistence so volume-handle mappings survive restarts.
	aliasStore := NewAliasStore(logger, *storagePath, server.Root())
	server.Registry().SetAliasStore(aliasStore)

	// /resolve and /resolve-batch are mTLS-exempt by design, so this key is
	// their only authentication. Absent, they are open to anything that can
	// reach the port — said once, loudly, rather than left to be inferred.
	if resolveCapabilityKey == nil {
		logger.Info("resolve-unauthenticated", lager.Data{
			"detail": "no --resolve-capability-key: POST /resolve and /resolve-batch accept any caller",
		})
	} else {
		if err := server.SetResolveCapabilityKey(resolveCapabilityKey); err != nil {
			// Unreachable while loadResolveCapabilityKey checks the same
			// thing, but the node is labelled by now, so a failure here
			// must take the label down with it.
			logger.Error("failed-to-configure-resolve-capability", err)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
			cleanupCancel()
			os.Exit(1)
		}
		logger.Info("resolve-capability-required")
	}

	// Scan hostPath at startup to populate registry with existing artifacts.
	if err := server.Registry().ScanHostPath(*storagePath); err != nil {
		logger.Error("failed-to-scan-hostpath", err)
		// Non-fatal — daemon can still serve explicitly registered artifacts.
	}

	// Load persisted aliases (after scan so stale validation can check paths).
	if err := server.Registry().LoadAliases(); err != nil {
		logger.Error("failed-to-load-aliases", err)
		// Non-fatal — aliases will be re-registered by ATC on next build.
	}

	// TTL sweeper (with registry ref for alias cleanup). Started below,
	// after the mirror is wired up, so its step-dir-removed callback can
	// prune mirror status without racing sweeper startup.
	sweepDone := make(chan struct{})

	// Cancelled alongside the sweeper at shutdown, so a maintenance walk does
	// not hold the process open mid-enumeration.
	maintenanceCtx, maintenanceCancel := context.WithCancel(context.Background())

	// A restore assembles the artifact in a temporary directory under steps/,
	// and the sweeper is what reclaims one left behind by a crash. If a restore
	// may outlive the TTL, the sweeper can instead delete a live restore's
	// working directory out from under it.
	if *durableStore != "" && *durableTimeout >= *ttl {
		logger.Error("durable-timeout-exceeds-ttl", fmt.Errorf(
			"--durable-timeout (%s) must be less than --ttl (%s)", *durableTimeout, *ttl))
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
		cleanupCancel()
		os.Exit(1)
	}

	// The cache, the strict-input store and the output plane's store are three
	// namespaces, and no two may be one, refused before any is dialled.
	if err := validateStorageNamespaces(*durableBucket, hangarInputNamespace(*hangarEnabled, *hangarBucket),
		outputNamespace(planeConfig)); err != nil {
		logger.Error("storage-namespaces-invalid", err)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
		cleanupCancel()
		os.Exit(1)
	}

	closeDurable := func() error { return nil }
	startRetention := func(tier *DurableTier) {
		maintainer := NewStoreMaintainer(logger, tier, server.Metrics(), *durableMaintenanceInterval, durableRetention)
		go maintainer.Run(maintenanceCtx)
	}
	if tier, closeTier, err := buildDurableTier(maintenanceCtx, logger, server.Metrics(), durableOptions{
		kind:      *durableStore,
		bucket:    *durableBucket,
		endpoint:  *durableEndpoint,
		storeID:   *durableStoreID,
		tokenFile: *durableTokenFile,
		caCert:    *durableCACert,
		timeout:   *durableTimeout,
		maxBytes:  *durableMaxBytes,
	}, startRetention); err != nil {
		// Misconfiguration is worth failing on: an operator who asked for a
		// durable store and silently did not get one would discover it as a
		// mysteriously cold cache months later.
		logger.Error("durable-store-config-invalid", err)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
		cleanupCancel()
		os.Exit(1)
	} else if tier != nil {
		closeDurable = closeTier
		server.SetDurableTier(tier)
		logger.Info("durable-store-enabled", lager.Data{"backend": *durableStore, "connected": tier.Ready()})

		if len(durableRetention) == 0 {
			// Not an error -- an operator may genuinely want to keep
			// everything, or may be relying on a bucket lifecycle rule -- but
			// it is worth saying out loud, because the alternative is
			// discovering it as a bill.
			logger.Info("durable-retention-unset", lager.Data{
				"note": "no --durable-retention given; nothing will ever be reclaimed",
			})
		} else {
			logger.Info("durable-retention", lager.Data{"policy": durableRetention.String()})
		}
	}

	var tlsCfg *tls.Config
	if tlsEnabled {
		var err error
		tlsCfg, err = BuildTLSConfig(*tlsCert, *tlsKey, *tlsCACert)
		if err != nil {
			logger.Error("failed-to-build-tls-config", err)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
			cleanupCancel()
			os.Exit(1)
		}
	}

	hangarService, hangarClose, err := buildHangarService(context.Background(), logger, *storagePath, hangarOptions{
		Enabled: *hangarEnabled, ScratchDir: *hangarScratchDir, WarrantKey: *hangarWarrantKey,
		MaxContentBytes: *hangarMaxContentBytes, MaxEntries: *hangarMaxEntries, WarrantTTL: *hangarWarrantTTL,
		Store: *hangarStore, StoreID: *hangarStoreID, TokenFile: *hangarTokenFile, CACert: *hangarCACert, Bucket: *hangarBucket, Prefix: *hangarPrefix, Endpoint: *hangarEndpoint, Timeout: *durableTimeout,
		TLSCert: *tlsCert, TLSKey: *tlsKey, TLSCACert: *tlsCACert,
	})
	if err != nil {
		logger.Error("hangar-config-invalid", err)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
		cleanupCancel()
		os.Exit(1)
	}
	if hangarService != nil {
		server.SetHangarService(hangarService)
		closeHangar = hangarClose
	}

	var plane *outputplane.Plane
	if planeConfig.CapabilityKeyFile != "" {
		planeConfig.NodeName = *nodeName
		planeConfig.ControlDir = *storagePath
		planeConfig.StepsDir = filepath.Join(*storagePath, "steps")
		if err := validateOutputScratch(planeConfig.ScratchDir, *storagePath, *hangarScratchDir, *hangarEnabled); err != nil {
			logger.Error("output-scratch-invalid", err)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
			cleanupCancel()
			os.Exit(1)
		}
		if err := os.MkdirAll(planeConfig.StepsDir, 0755); err != nil {
			logger.Error("failed-to-create-steps-path", err, lager.Data{"path": planeConfig.StepsDir})
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
			cleanupCancel()
			os.Exit(1)
		}
		var nodes kubernetes.Interface
		if *nodeName != "" {
			nodes = k8sClient
		}
		var servingCertificate []byte
		if tlsCfg != nil && len(tlsCfg.Certificates) > 0 && len(tlsCfg.Certificates[0].Certificate) > 0 {
			servingCertificate = tlsCfg.Certificates[0].Certificate[0]
		}
		plane, err = outputplane.Open(context.Background(), planeConfig, nodes, servingCertificate, os.Stdout)
		if err != nil {
			logger.Error("output-plane-config-invalid", err)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
			cleanupCancel()
			os.Exit(1)
		}
		server.SetOutputPlane(plane.Handler())
		closeStrict := closeHangar
		closeHangar = func() error { return errors.Join(closeStrict(), plane.Close()) }
	}

	sweeper := NewSweeper(logger, *storagePath, *ttl, 5*time.Minute, server.Registry())
	sweeper.SetGuard(server.Guard())
	sweeper.SetSourceLedger(server.SourceLedger())

	// Set up peer resolver for cross-node artifact resolution. It shares the
	// client built above: peer discovery is one more consumer of the same
	// seam, not a second place that works out how to reach the cluster.
	var mirror *Mirror
	if k8sClient != nil {
		podIP := os.Getenv("POD_IP")

		var peerTLS *PeerTLSConfig
		if tlsEnabled {
			peerTLS = &PeerTLSConfig{
				CertPath:   *tlsCert, // daemon uses its own server cert as client cert for peers
				KeyPath:    *tlsKey,
				CACertPath: *tlsCACert,
				// Peers are dialed by pod IP; their certificate names the
				// headless service, so that is what they are verified against.
				ServerName: peerTLSServerName(*serviceName, *namespace),
			}
		}

		peers := NewPeerResolver(logger, k8sClient, *namespace, *serviceName, *port, podIP, peerTLS)
		server.SetPeerResolver(peers)
		logger.Info("peer-resolver-configured", lager.Data{"service": *serviceName, "my-ip": podIP})

		// Wire up the outbound mirror manager. The mirror reuses the
		// peer resolver for endpoint discovery and shares the daemon's
		// TLS config (when enabled) for cross-node PUTs.
		if *mirrorReplicas != 0 {
			mirrorClient := buildMirrorHTTPClient(logger, peerTLS, *mirrorTimeout)
			scheme := "http"
			if tlsEnabled {
				scheme = "https"
			}
			mirror = NewMirror(MirrorConfig{
				StoragePath:    *storagePath,
				Port:           *port,
				Scheme:         scheme,
				Replicas:       *mirrorReplicas,
				Concurrency:    *mirrorConcurrency,
				PerPeerTimeout: *mirrorTimeout,
				Peers:          peers,
				Client:         mirrorClient,
				Logger:         logger.Session("mirror"),
				Guard:          server.Guard(),
				Root:           server.Root(),
			})
			server.SetMirrorTrigger(mirror.Trigger)
			logger.Info("mirror-configured", lager.Data{
				"replicas":    *mirrorReplicas,
				"concurrency": *mirrorConcurrency,
				"timeout":     mirrorTimeout.String(),
			})
		} else {
			logger.Info("mirror-disabled", lager.Data{"reason": "--mirror-replicas=0"})
		}
	}

	// Start the sweeper now that the mirror (if any) exists: swept step
	// dirs also drop their mirror status entries, keeping the status map
	// bounded. ForgetHandle is nil-receiver-safe, so this wiring is
	// unconditional.
	sweeper.SetOnStepDirRemoved(mirror.ForgetHandle)
	go func() {
		sweeper.Run(sweepDone)
	}()

	var handlerOpts []HandlerOption
	if tlsEnabled {
		handlerOpts = append(handlerOpts, WithTLS())
	}

	// An empty --listen-address keeps the address the daemon has always had:
	// net.JoinHostPort("", "7780") is ":7780", every address on the host.
	httpServer := &http.Server{
		Addr:    net.JoinHostPort(*listenAddress, strconv.Itoa(*port)),
		Handler: server.Handler(handlerOpts...),
	}

	if tlsEnabled {
		httpServer.TLSConfig = tlsCfg
	}

	// Bound before the node is labelled, so a taken metrics port stops the
	// daemon at startup instead of leaving it ready and unscrapeable -- which
	// is how the daemon went unmonitored before this listener existed.
	var metricsServer *http.Server
	var metricsListener net.Listener
	if *metricsPort != 0 {
		metricsServer = &http.Server{
			Addr:              net.JoinHostPort(*listenAddress, strconv.Itoa(*metricsPort)),
			Handler:           server.MetricsHandler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		metricsListener, err = net.Listen("tcp", metricsServer.Addr)
		if err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			cleanupErr := cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
			cleanupCancel()
			logger.Error("failed-to-bind-metrics-listener", errors.Join(err, cleanupErr))
			os.Exit(1)
		}
	}

	var readinessLabeler *NodeLabeler
	if hangarService != nil {
		readinessLabeler = hangarLabeler
	}
	bindCtx, bindCancel := context.WithTimeout(context.Background(), 10*time.Second)
	listener, err := listenAndAdvertiseHangar(bindCtx, httpServer.Addr, readinessLabeler, net.Listen)
	bindCancel()
	if err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr := cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, nil, closeHangar)
		cleanupCancel()
		logger.Error("failed-to-bind-or-advertise", errors.Join(err, cleanupErr))
		os.Exit(1)
	}
	if readinessLabeler != nil {
		logger.Info("hangar-node-labeled", lager.Data{"node": *nodeName, "label": HangarReadyLabel})
	}
	// The bound address, as a plain line: --port=0 asks the kernel for a port,
	// and a harness that started the daemon that way learns it from here
	// rather than from a probe that another process could answer.
	fmt.Fprintf(os.Stdout, "artifact-daemon listening on %s\n", listener.Addr())
	// The output plane's facet labels go on last, after the listener exists:
	// a label advertised before the daemon can answer is a pod scheduled onto
	// a node whose hold is refused on arrival.
	if plane != nil {
		advertiseCtx, advertiseCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := plane.Advertise(advertiseCtx)
		advertiseCancel()
		if err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			cleanupErr := cleanupDaemonServices(cleanupCtx, hangarLabeler, labeler, func() error {
				return errors.Join(plane.Withdraw(cleanupCtx), listener.Close())
			}, closeHangar)
			cleanupCancel()
			logger.Error("failed-to-advertise-output-plane", errors.Join(err, cleanupErr))
			os.Exit(1)
		}
	}

	// One slot per server, so the one that fails second never blocks on a
	// send nobody receives.
	errCh := make(chan error, 2)
	if metricsServer != nil {
		go func() {
			logger.Info("serving-metrics", lager.Data{"address": metricsServer.Addr})
			errCh <- metricsServer.Serve(metricsListener)
		}()
	}
	go func() {
		logger.Info("starting", lager.Data{
			"port":           *port,
			"listen-address": *listenAddress,
			"storage-path":   *storagePath,
			"node-name":      *nodeName,
			"namespace":      *namespace,
			"ttl":            ttl.String(),
			"tls":            tlsEnabled,
		})
		if tlsEnabled {
			// The listener is already bound so readiness is truthful; ServeTLS
			// still owns TLS negotiation and HTTP/2 setup.
			errCh <- httpServer.ServeTLS(listener, "", "")
		} else {
			errCh <- httpServer.Serve(listener)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	var serverFailure error
	select {
	case sig := <-sigCh:
		logger.Info("shutting-down", lager.Data{"signal": sig.String()})
	case err := <-errCh:
		logger.Error("server-failed", err)
		serverFailure = err
	}

	// The grace budget goes to the requests in flight first -- an output-plane
	// publish or seal among them -- and to the background work after, each
	// bounded by what is left of it. The labels come off before the listener
	// closes: a node that still advertises a facet it has stopped serving is
	// where the scheduler sends the next capture.
	ctx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()
	cleanupErr := cleanupDaemonServices(ctx, hangarLabeler, labeler, func() error {
		var err error
		if plane != nil {
			err = plane.Withdraw(ctx)
		}
		err = errors.Join(err, httpServer.Shutdown(ctx))
		if metricsServer != nil {
			err = errors.Join(err, metricsServer.Shutdown(ctx))
		}
		return err
	}, closeHangar)

	// Drain mirror jobs with what is left of the budget. Best-effort: Stop
	// blocks until in-flight jobs complete, and an unfinished mirror is a
	// peer that resolves the artifact from this node or the durable tier.
	if mirror != nil {
		stopped := make(chan struct{})
		go func() { mirror.Stop(); close(stopped) }()
		select {
		case <-stopped:
		case <-ctx.Done():
			logger.Info("mirror-drain-abandoned", lager.Data{"reason": "shutdown budget spent"})
		}
	}

	// Stop sweeper.
	close(sweepDone)
	maintenanceCancel()
	if err := closeDurable(); err != nil {
		logger.Error("durable-store-close-failed", err)
	}

	if cleanupErr != nil {
		logger.Error("shutdown-error", cleanupErr)
		os.Exit(1)
	}
	if serverFailure != nil && !errors.Is(serverFailure, http.ErrServerClosed) {
		os.Exit(1)
	}

	logger.Info("stopped")
}

// buildK8sClient creates a Kubernetes client, from an explicit kubeconfig when
// one is given and from the in-cluster config otherwise.
//
// The in-cluster path is what runs in production. The kubeconfig path exists
// because without it the daemon cannot talk to any cluster it is not running
// inside: rest.InClusterConfig reads KUBERNETES_SERVICE_HOST and a
// service-account token whose path client-go hardcodes. That made the daemon
// undebuggable from a laptop, and made peer discovery — the whole of
// mirroring, evacuation and cross-node fallback — unreachable by any test that
// does not deploy into Kubernetes. atc/worker/jetbridge/config.go has taken
// both paths since it was written; this brings the daemon into line with it.
func buildK8sClient(kubeconfig string) (kubernetes.Interface, error) {
	if kubeconfig != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("kubeconfig %q: %w", kubeconfig, err)
		}
		return kubernetes.NewForConfig(config)
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	return kubernetes.NewForConfig(config)
}

// buildMirrorHTTPClient constructs the http.Client used by the Mirror
// manager for PUT /stream-in to peers. When peerTLS is configured, the
// client uses mTLS, built by the same PeerTLSConfig.clientTLS as the peer
// probe and fetch clients, so it verifies peers against the same name.
func buildMirrorHTTPClient(logger lager.Logger, peerTLS *PeerTLSConfig, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if peerTLS != nil && peerTLS.CertPath != "" {
		tlsConfig, err := peerTLS.clientTLS()
		if err != nil {
			logger.Error("mirror-configure-mtls-failed", err)
		} else {
			transport.TLSClientConfig = tlsConfig
			logger.Info("mirror-mtls-enabled", lager.Data{"server-name": tlsConfig.ServerName})
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

// loadResolveCapabilityKey reads --resolve-capability-key and checks it is a
// key a verifier will accept, so every way it can be wrong surfaces before the
// daemon advertises itself. An empty path is not an error: it means resolve is
// unauthenticated, and returns a nil key.
func loadResolveCapabilityKey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	key, err := artifactcap.LoadKeyFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := artifactcap.NewVerifier(key); err != nil {
		return nil, err
	}
	return key, nil
}

// daemonClientNeeded reports whether the daemon must talk to the Kubernetes
// API at all. A node name means labeling (and, through the same client,
// peers); --peer-discovery means peers alone, for a namespace-scoped
// ServiceAccount that may not patch nodes.
func daemonClientNeeded(nodeName string, peerDiscovery bool) bool {
	return nodeName != "" || peerDiscovery
}
