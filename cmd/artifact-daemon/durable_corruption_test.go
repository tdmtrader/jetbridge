package main

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"syscall"
	"testing"

	"github.com/concourse/concourse/hangar/objectstore"
)

// Only the OBJECT being at fault expires it. A node, store or network problem
// must never turn into deleting a good shared cache object.
func TestOnlyACorruptObjectIsExpired(t *testing.T) {
	pathErr := func(errno error) error {
		return &fs.PathError{Op: "mkdir", Path: "x", Err: errno}
	}
	for name, row := range map[string]struct {
		err     error
		corrupt bool
	}{
		"hostile entry refused":        {refused("create symlink %q: %w", "hatch", errors.New("escapes")), true},
		"truncated stored body":        {refused("write file %q: %w", "f", io.ErrUnexpectedEOF), true},
		"malformed tar header":         {refused("reading tar: %w", tar.ErrHeader), true},
		"store reports corrupt bytes":  {fmt.Errorf("read: %w", objectstore.ErrCorrupt), true},
		"bare unexpected EOF from tar": {io.ErrUnexpectedEOF, true},
		"disk full while extracting":   {refused("create file %q: %w", "f", pathErr(syscall.ENOSPC)), false},
		"permission denied":            {refused("create dir %q: %w", "d", pathErr(syscall.EACCES)), false},
		"out of descriptors":           {refused("create file %q: %w", "f", pathErr(syscall.EMFILE)), false},
		"read-only filesystem":         {refused("create file %q: %w", "f", pathErr(syscall.EROFS)), false},
		"network drop mid-body":        {refused("reading tar: %w", fmt.Errorf("%w: %w", objectstore.ErrInfrastructure, io.ErrUnexpectedEOF)), false},
		"credential expired mid-body":  {fmt.Errorf("%w: 403", objectstore.ErrUnauthorized), false},
		"restore deadline":             {refused("reading tar: %w", context.DeadlineExceeded), false},
		"cancelled":                    {context.Canceled, false},
		"unmarked environment failure": {fmt.Errorf("create temp dir: %w", pathErr(syscall.ENOSPC)), false},
		"unclassified error":           {errors.New("something"), false},
		"nil":                          {nil, false},
	} {
		if got := isCorruptObject(row.err); got != row.corrupt {
			t.Errorf("%s: isCorruptObject = %v, want %v (%v)", name, got, row.corrupt, row.err)
		}
	}
}
