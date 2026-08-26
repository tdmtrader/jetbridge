package steps

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ProtectEventStream moves the real stdout to a private file descriptor and
// points fd 1 at stderr, returning the file the adapter must emit its JSONL
// event stream to.
//
// Why this is necessary, and why it has to happen at the FILE DESCRIPTOR
// level: the brine adapter protocol requires stdout to carry nothing but the
// typed event stream. atc/postgresrunner routes initdb and postmaster output
// through ginkgo.GinkgoWriter, which core_dsl.go's init() binds to os.Stdout
// before any of our code runs — so reassigning the os.Stdout variable is too
// late, and GinkgoWriterInterface exposes no way to unbind it (SetMode is on
// the unexported internal.Writer). The postmaster child process also inherits
// fd 1 directly.
//
// Redirecting the descriptor catches all three. Without it a run emits the
// postmaster's startup chatter into the event stream: measured at 46 lines
// failing typed event decode on the first green run of this suite.
func ProtectEventStream() (*os.File, error) {
	saved, err := unix.Dup(unix.Stdout)
	if err != nil {
		return nil, fmt.Errorf("dup stdout: %w", err)
	}
	if err := unix.Dup2(unix.Stderr, unix.Stdout); err != nil {
		return nil, fmt.Errorf("redirect stdout to stderr: %w", err)
	}
	return os.NewFile(uintptr(saved), "brine-events"), nil
}
