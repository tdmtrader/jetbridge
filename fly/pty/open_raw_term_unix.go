//go:build !windows
// +build !windows

package pty

import (
	"os"

	pkgterm "github.com/pkg/term"
)

func OpenRawTerm() (Term, error) {
	t, err := pkgterm.Open(os.Stdin.Name(), pkgterm.RawMode)
	if err != nil {
		return nil, err
	}

	return t, nil
}
