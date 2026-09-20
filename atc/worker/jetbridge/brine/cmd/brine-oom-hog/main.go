// Command brine-oom-hog runs out of memory on purpose, in a way the node
// cannot rescue.
//
// The live OOM scenarios give a task a 64 MiB cgroup limit and need the
// kernel to kill it: the runtime's reporting of OOMKilled is what they test.
// An ordinary allocator does not guarantee that. On a node with swap
// enabled and no per-cgroup swap accounting (cgroup v1 without
// swapaccount=1, which is what the task cluster runs), a container at its
// limit has its pages swapped out instead, the allocator finishes, and the
// premise silently depends on how full the node's swap happens to be.
//
// Locked pages cannot be swapped, so this program first tries to lock every
// mapping it will ever have (MCL_CURRENT|MCL_FUTURE), then touches N MiB.
// Locking needs CAP_IPC_LOCK or a memlock rlimit above N MiB, and the
// PodSecurity baseline the fixture namespaces enforce refuses the
// capability, so on the task cluster the lock is refused and the guarantee
// comes from the node instead: swap is off there (decision 2026-09-20), so
// the limit is the only thing that can stop the allocation and the kernel's
// answer is the OOM kill. The lock is kept as best effort for a node that
// does allow it. A refused lock is reported on stderr and the allocation
// proceeds. If it somehow completes, the program exits 9, the same code
// the awk allocator it replaced used, so "the allocation ran to completion"
// still reads the same in a scenario's error, and means the node let it
// swap.
//
// Usage: brine-oom-hog MIB

//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: brine-oom-hog MIB")
		os.Exit(2)
	}
	mib, err := strconv.Atoi(os.Args[1])
	if err != nil || mib <= 0 {
		fmt.Fprintf(os.Stderr, "brine-oom-hog: MIB must be a positive integer, got %q\n", os.Args[1])
		os.Exit(2)
	}
	if err := syscall.Mlockall(syscall.MCL_CURRENT | syscall.MCL_FUTURE); err != nil {
		fmt.Fprintf(os.Stderr, "brine-oom-hog: pages not locked (%v); relying on the node having no swap\n", err)
	}
	const mebibyte = 1 << 20
	const page = 4096
	keep := make([][]byte, 0, mib)
	for i := 0; i < mib; i++ {
		block := make([]byte, mebibyte)
		for j := 0; j < len(block); j += page {
			block[j] = byte(i)
		}
		keep = append(keep, block)
	}
	fmt.Fprintf(os.Stderr, "brine-oom-hog: touched %d MiB of locked memory without being killed\n", len(keep))
	os.Exit(9)
}
