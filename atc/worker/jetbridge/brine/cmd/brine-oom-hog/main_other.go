//go:build !linux

// The hog only means anything inside a Linux cgroup; elsewhere it refuses,
// so the package still builds on the development Mac.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "brine-oom-hog: only runs on Linux, inside a memory-limited cgroup")
	os.Exit(2)
}
