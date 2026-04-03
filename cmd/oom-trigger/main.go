// oom-trigger is a minimal static binary that reliably triggers the Linux
// OOM killer by allocating large heap slices. Used in behavioral tests to
// verify that Concourse detects OOM-killed containers.
//
// Build: CGO_ENABLED=0 GOOS=linux go build -o oom-trigger ./cmd/oom-trigger
package main

func main() {
	var s [][]byte
	for {
		// Allocate 10 MB per iteration. The Go runtime requests real
		// heap pages from the OS, which count against the container's
		// memory cgroup — the OOM killer fires within seconds.
		s = append(s, make([]byte, 10<<20))
	}
}
