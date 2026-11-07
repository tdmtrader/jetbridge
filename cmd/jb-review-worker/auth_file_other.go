//go:build !unix

package main

import (
	"errors"
	"os"
)

// openAuthFile refuses: a private tmpfs credential file is a Linux contract.
func openAuthFile(string) (*os.File, error) {
	return nil, errors.New("--auth-file needs a private tmpfs mount, which this platform does not have")
}
