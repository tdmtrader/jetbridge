package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/concourse/concourse/agent/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	exit, err := runner.Run(ctx, runner.FromEnv())
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-runner: %v\n", err)
		if exit == 0 {
			exit = 2
		}
	}
	os.Exit(exit)
}
