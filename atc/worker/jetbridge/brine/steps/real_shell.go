package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// runBusyboxScript executes the pod's unchanged sh command. Every external
// utility comes from the real BusyBox applet installation, not a shell
// function, scripted HTTP response, or host-tool fallback.
func runBusyboxScript(ctx context.Context, argv, env []string) (out []byte, err error) {
	if len(argv) != 3 || filepath.Base(argv[0]) != "sh" || argv[1] != "-c" {
		return nil, fmt.Errorf("expected a sh -c script, got %v", argv)
	}
	binary := os.Getenv("BRINE_BUSYBOX_BINARY")
	if binary == "" || !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("BRINE_BUSYBOX_BINARY must name the absolute BusyBox binary built by scripts/build-private-network")
	}
	applets, err := os.MkdirTemp("", "brine-fetch-applets-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(applets)) }()
	install := exec.CommandContext(ctx, binary, "--install", "-s", applets)
	if output, err := install.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("install real BusyBox applets: %w: %s", err, output)
	}
	for _, name := range []string{"sh", "wget", "sleep"} {
		if _, err := os.Stat(filepath.Join(applets, name)); err != nil {
			return nil, fmt.Errorf("required BusyBox applet %s: %w", name, err)
		}
	}
	cmd := exec.CommandContext(ctx, filepath.Join(applets, "sh"), argv[1:]...)
	cmd.Env = append(env, "PATH="+applets)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error { return stopCommandGroup(cmd) }
	defer func() { _ = stopCommandGroup(cmd) }()
	out, err = cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("fetch script did not finish within its context: %w", ctx.Err())
	}
	return out, err
}

// BusyBox script execution owns its process group. Cancellation
// must stop descendants too, before their filesystem fixtures are removed.
func stopCommandGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
