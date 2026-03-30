package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/concourse/concourse/atc/worker/native/agentpb"
	"google.golang.org/grpc"
)

func main() {
	listen := flag.String("listen", ":7799", "address to listen on (host:port)")
	workDir := flag.String("work-dir", "/tmp/concourse/native-agent", "base directory for container scratch space")
	cacheDir := flag.String("cache-dir", "/tmp/concourse/native-agent/caches", "durable directory for task caches")
	token := flag.String("token", "", "shared secret for token auth (checks authorization metadata)")
	tlsCert := flag.String("tls-cert", "", "path to server TLS certificate")
	tlsKey := flag.String("tls-key", "", "path to server TLS private key")
	tlsCA := flag.String("tls-ca", "", "path to CA certificate for client verification (mTLS)")
	flag.Parse()

	srv := &server{
		workDir:  *workDir,
		cacheDir: *cacheDir,
	}

	// Clean up orphaned processes from a previous agent instance.
	startupSweep(*workDir)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *listen, err)
	}

	var opts []grpc.ServerOption
	if *token != "" {
		opts = append(opts,
			grpc.ChainUnaryInterceptor(tokenAuthUnaryInterceptor(*token)),
			grpc.ChainStreamInterceptor(tokenAuthStreamInterceptor(*token)),
		)
	}
	if *tlsCert != "" && *tlsKey != "" && *tlsCA != "" {
		creds, err := loadServerTLS(*tlsCert, *tlsKey, *tlsCA)
		if err != nil {
			log.Fatalf("failed to load TLS credentials: %v", err)
		}
		opts = append(opts, creds)
	}

	grpcServer := grpc.NewServer(opts...)
	agentpb.RegisterNativeAgentServer(grpcServer, srv)

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "received %s, shutting down...\n", sig)
		grpcServer.GracefulStop()
	}()

	fmt.Fprintf(os.Stderr, "native-agent listening on %s\n", lis.Addr())
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
