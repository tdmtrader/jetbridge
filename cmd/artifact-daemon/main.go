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
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	storagePath := flag.String("storage-path", "/var/concourse/artifacts", "Path to artifact storage directory")
	ttl := flag.Duration("ttl", 2*time.Hour, "TTL for artifact cleanup sweep")
	nodeName := flag.String("node-name", "", "Kubernetes node name (for node labeling)")
	namespace := flag.String("namespace", "default", "Kubernetes namespace")
	labelKey := flag.String("label-key", "concourse.dev/artifact-cache", "Node label key to set on startup")
	_ = ttl
	_ = nodeName
	_ = namespace
	_ = labelKey

	flag.Parse()

	logger := lager.NewLogger("artifact-daemon")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))

	server := NewServer(logger, *storagePath)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server.Handler(),
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting", lager.Data{"port": *port, "storage-path": *storagePath})
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("shutdown-error", err)
		os.Exit(1)
	}

	logger.Info("stopped")
}
