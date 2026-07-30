//go:build linux && !amd64

package sandbox

import "errors"

func installChildSeccomp() error {
	return errors.New("broker sandbox: child seccomp filter supports linux/amd64 only")
}
