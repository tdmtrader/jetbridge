package main

import "syscall"

// errDirNotEmpty is what rename(2) returns when the destination directory
// already has contents. A concurrent peer fetch or a second restore of the same
// resource cache is the normal way to hit it, and both copies are equally valid,
// so the tier treats it as success rather than a failure.
var errDirNotEmpty = syscall.ENOTEMPTY
