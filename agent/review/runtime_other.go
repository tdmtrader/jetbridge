//go:build !linux

package review

import "errors"

func requireMemoryRuntime(string) error {
	return errors.New("credential-bearing reviews require Linux with a tmpfs runtime; capture and rendering work locally")
}
