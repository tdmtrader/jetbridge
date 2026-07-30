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
	_, err := policy.Canonical()
	return err
}

// Canonical resolves every path, including ancestor symlinks, and repeats all
// forbidden-overlap checks on the physical targets. Callers install Landlock
// rules only from the returned policy.
func (policy Policy) Canonical() (Policy, error) {
	writableRoot, err := canonicalPath(policy.WritableRoot, true)
	if err != nil {
		return Policy{}, fmt.Errorf("broker sandbox: writable root: %w", err)
	}
	scratchRoot := filepath.Dir(writableRoot)
	forbidden := canonicalForbiddenPaths([]string{
		"/workspace",
		"/run/concourse/agent-broker",
		"/proc",
		scratchRoot,
	})
	readOnlyPaths := make([]string, 0, len(policy.ReadOnlyPaths))
	seen := map[string]struct{}{}
	for _, path := range policy.ReadOnlyPaths {
		canonical, err := canonicalPath(path, false)
		if err != nil {
			return Policy{}, fmt.Errorf("broker sandbox: read-only path %q: %w", path, err)
		}
		for _, denied := range forbidden {
			if pathsOverlap(canonical, denied) {
				return Policy{}, fmt.Errorf(
					"broker sandbox: read-only path %q resolves to %q and overlaps denied workspace, authority, proc, or scratch path %q",
					path,
					canonical,
					denied,
				)
			}
		}
		if _, found := seen[canonical]; !found {
			seen[canonical] = struct{}{}
			readOnlyPaths = append(readOnlyPaths, canonical)
		}
	}
	return Policy{WritableRoot: writableRoot, ReadOnlyPaths: readOnlyPaths}, nil
}

func canonicalPath(path string, requireDirectory bool) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", errors.New("must be an absolute clean non-root path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved || resolved == string(filepath.Separator) {
		return "", errors.New("resolved to an invalid path")
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("resolved path remains a symlink")
	}
	if requireDirectory && !info.IsDir() {
		return "", errors.New("must be a directory")
	}
	return resolved, nil
}

func canonicalForbiddenPaths(paths []string) []string {
	result := make([]string, 0, len(paths)*2)
	seen := map[string]struct{}{}
	for _, path := range paths {
		for _, candidate := range []string{filepath.Clean(path), resolvedPathIfPresent(path)} {
			if candidate == "" {
				continue
			}
			if _, found := seen[candidate]; !found {
				seen[candidate] = struct{}{}
				result = append(result, candidate)
			}
		}
	}
	return result
}

func resolvedPathIfPresent(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return resolved
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func ExecArgs(policy Policy, binary string, arguments []string) ([]string, error) {
	canonical, err := policy.Canonical()
	if err != nil {
		return nil, err
	}
	resolvedBinary, err := canonicalExecutable(binary)
	if err != nil {
		return nil, fmt.Errorf("broker sandbox: harness binary: %w", err)
	}
	result := []string{"sandbox-exec", "--writable-root", canonical.WritableRoot}
	for _, path := range canonical.ReadOnlyPaths {
		result = append(result, "--read-only", path)
	}
	result = append(result, "--", resolvedBinary)
	result = append(result, arguments...)
	return result, nil
}

func canonicalExecutable(path string) (string, error) {
	resolved, err := canonicalPath(path, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return resolved, nil
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
