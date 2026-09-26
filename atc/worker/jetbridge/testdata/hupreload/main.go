// Command hupreload is a stand-in for dockerd in the supervisor's SIGHUP
// tests: it takes SIGHUP for itself, as a daemon that reloads its config on
// SIGHUP does, and runs its arguments as a child.
//
// signal.Notify installs a handler even when SIGHUP was ignored on entry, and
// a handled signal is reset to its default action in a child across exec. So
// the child starts with SIGHUP able to kill it, whatever the supervisor did
// before -- the way dockerd's docker-proxy children died with the exec
// session. It exits with the child's status, 128+N for a child killed by
// signal N, like a shell.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hupreload COMMAND [ARG...]")
		os.Exit(2)
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			fmt.Println("hupreload: SIGHUP, reloading")
		}
	}()

	child := exec.Command(os.Args[1], os.Args[2:]...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	err := child.Run()

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &exitErr):
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			fmt.Printf("hupreload: child killed by %v\n", status.Signal())
			os.Exit(128 + int(status.Signal()))
		}
		os.Exit(exitErr.ExitCode())
	default:
		fmt.Fprintln(os.Stderr, "hupreload:", err)
		os.Exit(1)
	}
}
