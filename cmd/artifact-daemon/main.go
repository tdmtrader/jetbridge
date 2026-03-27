package main

import (
	"context"
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
	port := flag.Int("port", 8080, "HTTP server port")
	storagePath := flag.String("storage-path", "/var/concourse/artifacts", "Path to artifact storage directory")
	ttl := flag.Duration("ttl", 2*time.Hour, "TTL for artifact cleanup sweep")
	nodeName := flag.String("node-name", "", "Kubernetes node name (for node labeling)")
	namespace := flag.String("namespace", "default", "Kubernetes namespace")
	labelKey := flag.String("label-key", "concourse.dev/artifact-cache", "Node label key to set on startup")

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

	// Start TTL sweeper in background.
	sweepDone := make(chan struct{})
	sweeper := NewSweeper(logger, *storagePath, *ttl, 5*time.Minute)
	go func() {
		sweeper.Run(sweepDone)
	}()

	server := NewServer(logger, *storagePath)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server.Handler(),
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting", lager.Data{
			"port":         *port,
			"storage-path": *storagePath,
			"node-name":    *nodeName,
			"namespace":    *namespace,
			"ttl":          ttl.String(),
		})
		errCh <- httpServer.ListenAndServe()
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
