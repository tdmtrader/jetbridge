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
	"path/filepath"
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
		authority, ok := parseAuthority(args[1:], stderr)
		if !ok {
			return exitUsage
		}
		builder, err := build(authority)
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
	authority, remaining, ok := parseCommand(args[1:], stderr)
	if !ok {
		return exitUsage
	}
	builder, err := build(authority)
	if err != nil {
		fmt.Fprintf(stderr, "agent-output: load authority: %v\n", err)
		return exitFailure
	}
	return outputbuilder.NewCLI(builder, stdin, stdout, stderr).Run(ctx, append([]string{args[0]}, remaining...))
}

func parseCommand(args []string, stderr io.Writer) (string, []string, bool) {
	var authority string
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] != "--authority" {
			remaining = append(remaining, args[index])
			continue
		}
		if authority != "" || index+1 == len(args) {
			fmt.Fprintln(stderr, "agent-output: --authority must be supplied exactly once")
			return "", nil, false
		}
		index++
		authority = args[index]
	}
	if !validAuthorityPath(authority) {
		fmt.Fprintln(stderr, "agent-output: --authority must be one absolute clean path")
		return "", nil, false
	}
	return authority, remaining, true
}
func parseAuthority(args []string, stderr io.Writer) (string, bool) {
	authority, rest, ok := parseCommand(args, stderr)
	return authority, ok && len(rest) == 0
}
func validAuthorityPath(name string) bool {
	return name != "" && filepath.IsAbs(name) && filepath.Clean(name) == name
}
func build(name string) (*outputbuilder.Builder, error) {
	authority, err := outputbuilder.LoadAuthority(name)
	if err != nil {
		return nil, err
	}
	canonicalizer := snapshot.Canonicalizer{}
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(canonicalizer))
	if err != nil {
		return nil, err
	}
	return outputbuilder.New(authority, registry, canonicalizer)
}
