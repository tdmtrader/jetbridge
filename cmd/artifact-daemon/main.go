package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/artifactcap"
	"github.com/concourse/concourse/agent/hangar"
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
	resolveCapabilityKeyFile := flag.String("resolve-capability-key", "", "Path to the raw 32-byte key required to authorize resolve operations")
	resolveMaxConcurrent := flag.Int("resolve-max-concurrent", defaultResolveMaxConcurrent, "Daemon-wide maximum number of concurrent artifact resolve operations")
	resolveTimeout := flag.Duration("resolve-timeout", defaultResolveTimeout, "Maximum lifetime of one local or peer artifact resolve operation")
	hangarGCSBucket := flag.String("hangar-gcs-bucket", "", "GCS bucket for durable immutable agent snapshots; empty retains legacy local-only snapshot reads")
	hangarGCSEndpoint := flag.String("hangar-gcs-endpoint", "", "Explicit GCS-compatible JSON endpoint for a long-lived emulator; empty uses production application-default credentials")
	hangarScratchDir := flag.String("hangar-scratch-dir", "", "Absolute daemon-local scratch directory for Hangar compression and verified reads; defaults under storage-path")
	hangarReadTimeout := flag.Duration("hangar-read-timeout", 5*time.Minute, "Maximum time to read and verify one durable Hangar object")
	hangarWriteTimeout := flag.Duration("hangar-write-timeout", 5*time.Minute, "Maximum time to compress and commit one durable Hangar object")
	hangarInventoryInterval := flag.Duration("hangar-inventory-interval", DefaultHangarInventoryInterval, "How often to enumerate the Hangar store and publish its aggregate residency; 0 disables. Every daemon lists the same bucket, so raise this on large clusters")
	hangarInventoryTimeout := flag.Duration("hangar-inventory-timeout", DefaultHangarInventoryTimeout, "Maximum time for one Hangar residency enumeration")

	flag.Parse()

	logger := lager.NewLogger("artifact-daemon")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))
	resolveCapabilityKey, err := artifactcap.LoadKeyFile(*resolveCapabilityKeyFile)
	if err != nil {
		logger.Error("failed-to-load-resolve-capability-key", err)
		os.Exit(1)
	}

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
	if *hangarGCSBucket != "" {
		scratchDir := *hangarScratchDir
		if scratchDir == "" {
			scratchDir = filepath.Join(*storagePath, ".hangar-scratch")
		}
		if !filepath.IsAbs(scratchDir) {
			logger.Error("invalid-hangar-scratch-dir", fmt.Errorf("hangar scratch directory must be absolute"))
			os.Exit(1)
		}
		if err := os.MkdirAll(scratchDir, 0700); err != nil {
			logger.Error("create-hangar-scratch-dir", err)
			os.Exit(1)
		}
		client, err := hangar.NewStorageClient(context.Background(), *hangarGCSEndpoint)
		if err != nil {
			logger.Error("create-hangar-gcs-client", err)
			os.Exit(1)
		}
		store, err := hangar.NewGCSStore(client, hangar.GCSConfig{
			Bucket:       *hangarGCSBucket,
			ScratchDir:   scratchDir,
			ReadTimeout:  *hangarReadTimeout,
			WriteTimeout: *hangarWriteTimeout,
		})
		if err != nil {
			_ = client.Close()
			logger.Error("configure-hangar-gcs-store", err)
			os.Exit(1)
		}
		defer client.Close()
		server.SetHangarStore(store)
		logger.Info("hangar-configured", lager.Data{"bucket": *hangarGCSBucket, "emulator": *hangarGCSEndpoint != ""})
	}
	if err := server.ConfigureResolveLimits(*resolveMaxConcurrent, *resolveTimeout); err != nil {
		logger.Error("invalid-resolve-limits", err)
		os.Exit(1)
	}

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
					ServerName: peerTLSServerName(*namespace, *serviceName),
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
	handlerOpts = append(handlerOpts, WithResolveCapabilityKey(resolveCapabilityKey))
	if tlsEnabled {
		handlerOpts = append(handlerOpts, WithTLS())
	}

	httpServer := newArtifactHTTPServer(*port, server.Handler(handlerOpts...))

	// Wire preemption watcher if enabled. The watcher always latches a real
	// node-local metadata notice; mirroring is optional follow-up work.
	preemptCtx, preemptCancel := context.WithCancel(context.Background())
	defer preemptCancel()

	// Publish how much the durable store holds. Nothing else does: the snapshot
	// counters measure bytes in motion, so the store's own occupancy — the
	// quantity it is actually bounded by — is otherwise unobserved until a write
	// fails against a full store.
	server.StartHangarInventory(preemptCtx, *hangarInventoryInterval, *hangarInventoryTimeout)
	if *hangarGCSBucket != "" && *hangarInventoryInterval > 0 {
		logger.Info("hangar-inventory-started", lager.Data{
			"interval": hangarInventoryInterval.String(),
			"timeout":  hangarInventoryTimeout.String(),
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
	var transport http.RoundTripper = http.DefaultTransport.(*http.Transport).Clone()

	if peerTLS != nil {
		tlsConfig, err := loadPeerClientTLS(peerTLS)
		if err != nil {
			logger.Error("mirror-configure-tls-failed", err)
			transport = failingRoundTripper{err: err}
		} else {
			httpTransport := http.DefaultTransport.(*http.Transport).Clone()
			httpTransport.TLSClientConfig = tlsConfig
			transport = httpTransport
			logger.Info("mirror-mtls-enabled", lager.Data{"server-name": peerTLS.ServerName})
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
