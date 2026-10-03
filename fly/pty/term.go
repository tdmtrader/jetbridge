package pty

import (
	"io"
	"os"

	"golang.org/x/term"
)

type Term interface {
	io.ReadWriter

	Restore() error
}

func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
