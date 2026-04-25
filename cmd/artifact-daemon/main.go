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

	// Start TTL sweeper in background (with registry ref for alias cleanup).
	sweepDone := make(chan struct{})
	sweeper := NewSweeper(logger, *storagePath, *ttl, 5*time.Minute, server.Registry())
	go func() {
		sweeper.Run(sweepDone)
	}()

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
