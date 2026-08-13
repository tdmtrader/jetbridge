package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	port := flag.Int("port", 7780, "HTTP server port")
	storagePath := flag.String("storage-path", "/var/concourse/artifacts", "Path to artifact storage directory")
	ttl := flag.Duration("ttl", 2*time.Hour, "TTL for artifact cleanup sweep")
	nodeName := flag.String("node-name", "", "Kubernetes node name (for node labeling)")
	namespace := flag.String("namespace", "default", "Kubernetes namespace")
	serviceName := flag.String("service-name", "artifact-daemon", "Headless service name for EndpointSlice peer discovery")
	labelKey := flag.String("label-key", "concourse.dev/artifact-cache", "Node label key to set on startup")
	tlsCert := flag.String("tls-cert", "", "Path to TLS server certificate (enables HTTPS with mTLS)")
	tlsKey := flag.String("tls-key", "", "Path to TLS server private key")
	tlsCACert := flag.String("tls-ca-cert", "", "Path to CA certificate for verifying client certificates")
	mirrorReplicas := flag.Int("mirror-replicas", 2, "Replication factor for outbound mirror: 0=disabled, N=local + (N-1) peers, -1=all peers")
	mirrorConcurrency := flag.Int("mirror-concurrency", 4, "Max concurrent in-flight mirror jobs")
	mirrorTimeout := flag.Duration("mirror-timeout", 5*time.Minute, "Per-peer per-job mirror PUT timeout")
	preemptionWatch := flag.Bool("preemption-watch", false, "Watch GCP metadata server for spot preemption notice and evacuate unmirrored artifacts before termination")
	preemptionBudget := flag.Duration("preemption-budget", 25*time.Second, "Total time budget for synchronous evacuation on preemption")

	// Durable tier. Off unless --durable-store names a backend, and the daemon
	// behaves exactly as before when it is off.
	durableStore := flag.String("durable-store", "", "Long-term store for resource caches: \"\" (disabled), \"gcs\", \"s3\" or \"filesystem\"")
	durablePath := flag.String("durable-path", "", "Root directory for --durable-store=filesystem")
	durableBucket := flag.String("durable-bucket", "", "Bucket for --durable-store=gcs or =s3")
	durablePrefix := flag.String("durable-prefix", "", "Key prefix inside the bucket, so one bucket can serve several clusters or consumers")
	durableEndpoint := flag.String("durable-endpoint", "", "Endpoint override; set this for MinIO and other S3-compatible stores")
	durableRegion := flag.String("durable-s3-region", "us-east-1", "S3 region")
	durableTimeout := flag.Duration("durable-timeout", 5*time.Minute, "Per-operation timeout for the durable store")
	durableMaintenanceInterval := flag.Duration("durable-maintenance-interval", defaultMaintenanceInterval, "How often to walk the durable store to reclaim expired objects and measure what remains. Every daemon runs its own enumeration, and a List is billed per page, so this is deliberately slow.")

	// Retention is JetBridge's own, not a bucket lifecycle rule, so the period
	// lives here rather than as a string an operator types into a cloud console
	// that has to match a prefix this code composes. A class with no entry is
	// kept forever.
	var durableRetention RetentionPolicy
	flag.Var(&durableRetention, "durable-retention", "Retention for one class of durable artifact, as CLASS=DURATION (e.g. resource-caches=720h). Repeatable. A class with no entry is never reclaimed.")
	durableMaxBytes := flag.Int64("durable-max-bytes", 5<<30, "Largest single artifact to store durably; 0 disables the limit")

	flag.Parse()

	logger := lager.NewLogger("artifact-daemon")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))

	// Build K8s client for node labeling.
	var labeler *NodeLabeler
	if *nodeName != "" {
		k8sClient, err := buildK8sClient()
		if err != nil {
			logger.Error("failed-to-create-k8s-client", err)
			os.Exit(1)
		}

		labeler = NewNodeLabeler(logger, k8sClient, *nodeName, *labelKey)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := labeler.AddLabel(ctx); err != nil {
			cancel()
			logger.Error("failed-to-label-node", err)
			os.Exit(1)
		}
		cancel()
		logger.Info("node-labeled", lager.Data{"node": *nodeName, "label": *labelKey})
	} else {
		logger.Info("skipping-node-labeling", lager.Data{"reason": "no --node-name provided"})
	}

	server := NewServer(logger, *storagePath, *nodeName)

	// Set up alias persistence so volume-handle mappings survive restarts.
	aliasStore := NewAliasStore(logger, *storagePath)
	server.Registry().SetAliasStore(aliasStore)

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
		os.Exit(1)
	}

	if tier, err := buildDurableTier(logger, server.Metrics(), durableOptions{
		kind:     *durableStore,
		path:     *durablePath,
		bucket:   *durableBucket,
		prefix:   *durablePrefix,
		endpoint: *durableEndpoint,
		region:   *durableRegion,
		timeout:  *durableTimeout,
		maxBytes: *durableMaxBytes,
	}); err != nil {
		// Misconfiguration is worth failing on: an operator who asked for a
		// durable store and silently did not get one would discover it as a
		// mysteriously cold cache months later.
		logger.Error("durable-store-config-invalid", err)
		os.Exit(1)
	} else if tier != nil {
		server.SetDurableTier(tier)
		logger.Info("durable-store-enabled", lager.Data{"backend": *durableStore})

		maintainer := NewStoreMaintainer(logger, tier, server.Metrics(), *durableMaintenanceInterval, durableRetention)
		go maintainer.Run(maintenanceCtx)

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

	sweeper := NewSweeper(logger, *storagePath, *ttl, 5*time.Minute, server.Registry())
	sweeper.SetGuard(server.Guard())

	tlsEnabled := *tlsCert != "" && *tlsKey != "" && *tlsCACert != ""

	// Set up peer resolver for cross-node artifact resolution.
	var mirror *Mirror
	if *nodeName != "" {
		k8sClientForPeers, err := buildK8sClient()
		if err != nil {
			logger.Error("failed-to-create-peer-k8s-client", err)
			// Non-fatal — cross-node resolution won't work but local still does.
		} else {
			podIP := os.Getenv("POD_IP")

			var peerTLS *PeerTLSConfig
			if tlsEnabled {
				peerTLS = &PeerTLSConfig{
					CertPath:   *tlsCert, // daemon uses its own server cert as client cert for peers
					KeyPath:    *tlsKey,
					CACertPath: *tlsCACert,
				}
			}

			peers := NewPeerResolver(logger, k8sClientForPeers, *namespace, *serviceName, *port, podIP, peerTLS)
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

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server.Handler(handlerOpts...),
	}

	// Wire preemption watcher if enabled. The watcher long-polls GCP
	// metadata in its own goroutine and fires Mirror.Evacuate when the
	// preempted endpoint transitions to TRUE.
	preemptCtx, preemptCancel := context.WithCancel(context.Background())
	defer preemptCancel()
	if *preemptionWatch && mirror != nil {
		watcher := NewPreemptionWatcher(logger.Session("preempt"), DefaultPreemptionMetadataURL,
			func(ctx context.Context) {
				logger.Info("evacuating-on-preemption", lager.Data{
					"budget": preemptionBudget.String(),
				})
				mirror.Evacuate(ctx, *preemptionBudget)
			})
		go watcher.Run(preemptCtx)
		logger.Info("preemption-watcher-started", lager.Data{
			"budget": preemptionBudget.String(),
		})
	} else if *preemptionWatch {
		logger.Info("preemption-watch-disabled", lager.Data{
			"reason": "mirror not configured (--mirror-replicas=0 or no node-name)",
		})
	}

	if tlsEnabled {
		tlsCfg, err := BuildTLSConfig(*tlsCert, *tlsKey, *tlsCACert)
		if err != nil {
			logger.Error("failed-to-build-tls-config", err)
			os.Exit(1)
		}
		httpServer.TLSConfig = tlsCfg
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting", lager.Data{
			"port":         *port,
			"storage-path": *storagePath,
			"node-name":    *nodeName,
			"namespace":    *namespace,
			"ttl":          ttl.String(),
			"tls":          tlsEnabled,
		})
		if tlsEnabled {
			// Cert/key already loaded into TLSConfig; pass empty strings.
			errCh <- httpServer.ListenAndServeTLS("", "")
		} else {
			errCh <- httpServer.ListenAndServe()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		logger.Info("shutting-down", lager.Data{"signal": sig.String()})
	case err := <-errCh:
		logger.Error("server-failed", err)
		os.Exit(1)
	}

	// Cancel the preemption watcher's poll loop so it exits cleanly.
	preemptCancel()

	// Drain mirror jobs before sweeping / shutting down. This is best-effort
	// — Wait blocks until in-flight jobs complete (capped by per-peer timeout).
	if mirror != nil {
		mirror.Stop()
	}

	// Stop sweeper.
	close(sweepDone)
	maintenanceCancel()

	// Remove node label before shutting down.
	if labeler != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := labeler.RemoveLabel(ctx); err != nil {
			logger.Error("failed-to-remove-node-label", err)
		} else {
			logger.Info("node-label-removed")
		}
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("shutdown-error", err)
		os.Exit(1)
	}

	logger.Info("stopped")
}

// buildK8sClient creates a Kubernetes client using in-cluster config.
func buildK8sClient() (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	return kubernetes.NewForConfig(config)
}

// buildMirrorHTTPClient constructs the http.Client used by the Mirror
// manager for PUT /stream-in to peers. When peerTLS is configured, the
// client uses mTLS (same client cert as the peer probe path).
func buildMirrorHTTPClient(logger lager.Logger, peerTLS *PeerTLSConfig, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if peerTLS != nil && peerTLS.CertPath != "" {
		clientCert, err := tls.LoadX509KeyPair(peerTLS.CertPath, peerTLS.KeyPath)
		if err != nil {
			logger.Error("mirror-load-client-cert-failed", err)
		} else {
			caCertPEM, err := os.ReadFile(peerTLS.CACertPath)
			if err != nil {
				logger.Error("mirror-read-ca-cert-failed", err)
			} else {
				caPool := x509.NewCertPool()
				caPool.AppendCertsFromPEM(caCertPEM)
				transport.TLSClientConfig = &tls.Config{
					Certificates: []tls.Certificate{clientCert},
					RootCAs:      caPool,
				}
				logger.Info("mirror-mtls-enabled")
			}
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
