// Package sandbox applies the fail-closed filesystem boundary used by native
// child harnesses. It intentionally does not restrict networking: provider
// API access remains available while parent workspace and authority paths do
// not.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const MinimumABI = 3

type Policy struct {
	WritableRoot  string
	ReadOnlyPaths []string
}

func (policy Policy) Validate() error {
	if err := validPath(policy.WritableRoot, true); err != nil {
		return fmt.Errorf("broker sandbox: writable root: %w", err)
	}
	scratchRoot := filepath.Dir(policy.WritableRoot)
	for _, path := range policy.ReadOnlyPaths {
		if err := validPath(path, false); err != nil {
			return fmt.Errorf("broker sandbox: read-only path %q: %w", path, err)
		}
		for _, forbidden := range []string{
			"/workspace",
			"/run/concourse/agent-broker",
			"/proc",
			scratchRoot,
		} {
			if pathsOverlap(path, forbidden) {
				return fmt.Errorf("broker sandbox: read-only path %q overlaps denied path %q", path, forbidden)
			}
		}
	}
	return nil
}

func validPath(path string, requireDirectory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return errors.New("must be an absolute clean non-root path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("must not be a symlink")
	}
	if requireDirectory && !info.IsDir() {
		return errors.New("must be a directory")
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func ExecArgs(policy Policy, binary string, arguments []string) ([]string, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || binary == string(filepath.Separator) {
		return nil, errors.New("broker sandbox: harness binary must be an absolute clean non-root path")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, fmt.Errorf("broker sandbox: harness binary: %w", err)
	}
	result := []string{"sandbox-exec", "--writable-root", policy.WritableRoot}
	for _, path := range policy.ReadOnlyPaths {
		result = append(result, "--read-only", path)
	}
	result = append(result, "--", binary)
	result = append(result, arguments...)
	return result, nil
}

func ParseExecArgs(arguments []string) (Policy, string, []string, error) {
	var policy Policy
	for index := 0; index < len(arguments); {
		switch arguments[index] {
		case "--writable-root":
			if policy.WritableRoot != "" || index+1 >= len(arguments) {
				return Policy{}, "", nil, errors.New("broker sandbox: one writable root is required")
			}
			policy.WritableRoot = arguments[index+1]
			index += 2
		case "--read-only":
			if index+1 >= len(arguments) {
				return Policy{}, "", nil, errors.New("broker sandbox: read-only path value is required")
			}
			policy.ReadOnlyPaths = append(policy.ReadOnlyPaths, arguments[index+1])
			index += 2
		case "--":
			if policy.WritableRoot == "" || index+1 >= len(arguments) {
				return Policy{}, "", nil, errors.New("broker sandbox: native harness command is required")
			}
			return policy, arguments[index+1], append([]string(nil), arguments[index+2:]...), nil
		default:
			return Policy{}, "", nil, fmt.Errorf("broker sandbox: unsupported argument %q", arguments[index])
		}
	}
	return Policy{}, "", nil, errors.New("broker sandbox: native harness separator is required")
}

func ValidateAvailability(abi int, err error) error {
	if err != nil {
		switch {
		case errors.Is(err, syscall.ENOSYS):
			return fmt.Errorf("broker sandbox: Landlock is unavailable: %w", err)
		case errors.Is(err, syscall.EOPNOTSUPP):
			return fmt.Errorf("broker sandbox: Landlock is disabled by the kernel: %w", err)
		case errors.Is(err, syscall.EPERM):
			return fmt.Errorf("broker sandbox: Landlock is blocked by the container seccomp policy: %w", err)
		default:
			return fmt.Errorf("broker sandbox: query Landlock ABI: %w", err)
		}
	}
	if abi < MinimumABI {
		return fmt.Errorf("broker sandbox: Landlock ABI %d is below required ABI %d", abi, MinimumABI)
	}
	return nil
}

func ExistingEssentialReadPaths() []string {
	candidates := []string{
		"/bin", "/usr", "/lib", "/lib64",
		"/etc/ssl", "/etc/ca-certificates", "/etc/pki",
		"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf",
		"/etc/ld.so.cache", "/etc/ld.so.preload", "/etc/localtime",
		"/dev/null", "/dev/urandom", "/dev/random",
	}
	result := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved || resolved == "/" {
			continue
		}
		if _, found := seen[resolved]; !found {
			seen[resolved] = struct{}{}
			result = append(result, resolved)
		}
	}
	return result
}
