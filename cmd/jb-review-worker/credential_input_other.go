//go:build !linux

package main

import "os"

// RunWorker rejects credential-bearing execution on other operating systems
// before reading input. The private input-reader MCP remains portable.
func credentialInput() (*os.File, error) { return os.Stdin, nil }
