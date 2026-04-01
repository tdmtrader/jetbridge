package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// startupSweep scans the container scratch directory for leftover directories
// from a previous agent instance. For each, it kills any orphaned process via
// its PID file and removes the directory.
func startupSweep(workDir string) {
	containersDir := filepath.Join(workDir, "containers")
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		fmt.Fprintf(os.Stderr, "startup-sweep: failed to read containers dir: %v\n", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		handle := entry.Name()
		containerDir := filepath.Join(containersDir, handle)

		killFromPidFile(containerDir, handle)

		if err := os.RemoveAll(containerDir); err != nil {
			fmt.Fprintf(os.Stderr, "startup-sweep: failed to remove %s: %v\n", containerDir, err)
		} else {
			fmt.Fprintf(os.Stderr, "startup-sweep: cleaned up stale container %s\n", handle)
		}
	}
}

// killFromPidFile reads a PID file and sends SIGKILL to the process group if
// the process is still running.
func killFromPidFile(containerDir, handle string) {
	pidFile := filepath.Join(containerDir, handle+".pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}

	// Signal 0 checks if process exists without sending a signal.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "startup-sweep: killing orphaned process %d (container %s)\n", pid, handle)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
