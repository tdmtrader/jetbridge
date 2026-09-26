//go:build !linux

package session

import "errors"

// RequireMemoryRuntime always refuses outside Linux: credential-bearing
// sessions need a tmpfs runtime. Capture and rendering work locally.
func RequireMemoryRuntime(string) error {
	return errors.New("credential-bearing sessions require Linux with a tmpfs runtime; capture and rendering work locally")
}
