package pgtest

import "syscall"

// SIGQUIT is postgres' immediate-shutdown signal.
var sigQuit = syscall.SIGQUIT
