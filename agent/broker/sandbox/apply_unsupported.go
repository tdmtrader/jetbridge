//go:build !linux

package sandbox

import "errors"

func Apply(Policy) error {
	return errors.New("broker sandbox: Landlock requires Linux")
}

func Exec([]string) error {
	return errors.New("broker sandbox: Landlock requires Linux")
}
