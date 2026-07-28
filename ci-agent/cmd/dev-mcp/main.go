// Command dev-mcp serves a repo's dev-mcp implementation: the five-tool MCP
// contract (00-shared-contracts.md §3.1) backed by commands declared in
// dev-mcp.yml, over streamable HTTP, with GET /healthz for readiness probes
// (§8.5 packaging convention).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/concourse/ci-agent/devmcp"
)

func main() {
	configPath := flag.String("config", "dev-mcp.yml", "path to the dev-mcp config file")
	workdir := flag.String("workdir", ".", "workspace root that commands run in")
	flag.Parse()

	cfg, err := devmcp.Load(*configPath)
	if err != nil {
		log.Fatalf("dev-mcp: %s", err)
	}

	// F13 delta (00 §3.1, 2026-07-09): a set-but-invalid, <= 0, or > 30s
	// heartbeat is a FATAL startup error — never clamped silently. The
	// <= 0 check must live HERE: devmcp.NewServer's <= 0 fallback would
	// otherwise silently substitute DefaultHeartbeat.
	heartbeat := devmcp.DefaultHeartbeat
	if raw := os.Getenv("DEV_MCP_PROGRESS_INTERVAL"); raw != "" {
		heartbeat, err = time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("dev-mcp: invalid DEV_MCP_PROGRESS_INTERVAL: %s", err)
		}
		if heartbeat <= 0 || heartbeat > 30*time.Second {
			log.Fatalf("dev-mcp: DEV_MCP_PROGRESS_INTERVAL must be > 0 and <= 30s (contracts §3.1 progress bound), got %s", heartbeat)
		}
	}

	server := devmcp.NewServer(heartbeat)
	core, err := devmcp.NewCore(cfg, *workdir)
	if err != nil {
		log.Fatalf("dev-mcp: %s", err)
	}
	devmcp.RegisterTools(server, core)

	addr := os.Getenv("MCP_LISTEN_ADDR")
	if addr == "" {
		addr = ":7780"
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", server)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	httpServer := &http.Server{Addr: addr, Handler: mux}

	// drained is closed only after Shutdown returns; main must wait on it
	// after ListenAndServe unblocks (which happens as soon as the listener
	// closes, while in-flight requests are still draining) or the process
	// exits mid-drain and in-flight tool calls lose their final frame.
	drained := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
		close(drained)
	}()

	log.Printf("dev-mcp: serving %d components on %s", len(cfg.Components), addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("dev-mcp: %s", err)
	}
	<-drained
}
