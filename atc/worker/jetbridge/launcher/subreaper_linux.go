package launcher

import "golang.org/x/sys/unix"

// becomeSubreaper makes orphaned descendants of the command reparent to the
// launcher rather than to PID 1, so it can reap them wherever it runs: as a
// pod's PID 1 that is already so, and in a test or a shared pid namespace
// where it is not.
func becomeSubreaper() {
	_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
