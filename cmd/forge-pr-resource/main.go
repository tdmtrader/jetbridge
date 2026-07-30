// Command forge-pr-resource is the intentionally thin Concourse resource
// adapter. The image installs this one binary under check, in, and out names.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/concourse/concourse/agent/pullrequest/resource"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr, resource.Dependencies{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func Run(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies resource.Dependencies) error {
	return resource.Run(ctx, executable, args, stdin, stdout, stderr, dependencies)
}
