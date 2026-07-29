// Command agent-output exposes the platform-issued managed output builder.
// It can author and preflight candidates, but deliberately contains no seal
// operation or credential for minting snapshot authority.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// PlatformAuthorityPath is mounted read-only by the task runtime. It is not a
// command-line option: choosing an authority document would let the agent mint
// its own input bindings and output ports.
const PlatformAuthorityPath = "/concourse/agent-output/authority.json"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runCLI(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "agent-output: command is required")
		return exitUsage
	}
	if args[0] == "serve" {
		if len(args) != 1 {
			fmt.Fprintln(stderr, "agent-output: serve accepts no authority or endpoint override")
			return exitUsage
		}
		builder, err := build()
		if err != nil {
			fmt.Fprintf(stderr, "agent-output: load authority: %v\n", err)
			return exitFailure
		}
		listener, err := outputbuilder.ListenMCP(outputbuilder.DefaultMCPAddress)
		if err != nil {
			fmt.Fprintf(stderr, "agent-output: serve: %v\n", err)
			return exitFailure
		}
		server := &http.Server{Handler: outputbuilder.NewMCPServer(builder), BaseContext: func(net.Listener) context.Context { return ctx }}
		go func() { <-ctx.Done(); _ = server.Close() }()
		err = server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(stderr, "agent-output: serve: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	if args[0] != "describe" && args[0] != "write" && args[0] != "validate" {
		fmt.Fprintf(stderr, "agent-output: unknown command %q\n", args[0])
		return exitUsage
	}
	if hasAuthorityOverride(args[1:]) {
		fmt.Fprintln(stderr, "agent-output: authority is platform-owned and cannot be overridden")
		return exitUsage
	}
	builder, err := build()
	if err != nil {
		fmt.Fprintf(stderr, "agent-output: load authority: %v\n", err)
		return exitFailure
	}
	return outputbuilder.NewCLI(builder, stdin, stdout, stderr).Run(ctx, args)
}

func hasAuthorityOverride(args []string) bool {
	for _, arg := range args {
		if arg == "--authority" || strings.HasPrefix(arg, "--authority=") {
			return true
		}
	}
	return false
}
func build() (*outputbuilder.Builder, error) {
	authority, err := outputbuilder.LoadAuthority(PlatformAuthorityPath)
	if err != nil {
		return nil, err
	}
	canonicalizer := snapshot.Canonicalizer{MaxEntries: snapshot.DefaultMaxSnapshotEntries, MaxContentBytes: snapshot.DefaultMaxSnapshotContentBytes}
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(canonicalizer))
	if err != nil {
		return nil, err
	}
	return outputbuilder.New(authority, registry, canonicalizer)
}
