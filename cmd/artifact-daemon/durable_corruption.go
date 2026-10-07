package main

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"syscall"

	"github.com/concourse/concourse/hangar/objectstore"
)

// localFailures are errors that say something about THIS node -- a full disk,
// a permission, a descriptor limit, a read-only or failing filesystem -- and
// nothing about the object. extractTarToRoot marks some of them refused
// because os.Root's escape error has no identity of its own; they are excluded
// here by errno.
var localFailures = []error{
	syscall.ENOSPC, syscall.EDQUOT, syscall.EACCES, syscall.EPERM, syscall.EMFILE,
	syscall.ENFILE, syscall.EROFS, syscall.EIO, syscall.ENOMEM,
}

// isCorruptObject reports whether a restore failed because of the OBJECT --
// a hostile or malformed archive, or a stored body that ends early -- and so
// the object should be expired.
//
// Everything uncertain answers no. A store or network failure while reading
// the body (objectstore.ErrInfrastructure, which both backends wrap transport
// errors in), the restore's own deadline, and any local filesystem failure say
// nothing about the object, and expiring a good object on one of them would
// turn a node problem into a cluster-wide cache miss.
func isCorruptObject(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, objectstore.ErrInfrastructure) || errors.Is(err, objectstore.ErrUnauthorized) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, local := range localFailures {
		if errors.Is(err, local) {
			return false
		}
	}
	return errors.Is(err, ErrRefused) || errors.Is(err, objectstore.ErrCorrupt) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, tar.ErrHeader) ||
		errors.Is(err, tar.ErrFieldTooLong) || errors.Is(err, tar.ErrInsecurePath)
}
