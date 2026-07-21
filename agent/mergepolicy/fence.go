package mergepolicy

import (
	"fmt"
	"path"
	"strings"
)

// FenceResult is the outcome of evaluating a policy against a real diff.
type FenceResult struct {
	Passed bool
	Reason string // always populated when Passed is false
}

// EvaluateFence checks the DELIVERED DIFF against the policy — never the
// workflow's self-description. "Version bump" is a category of intent; this
// looks at what actually changed.
func EvaluateFence(p Policy, changes []Change) FenceResult {
	if len(changes) == 0 {
		return FenceResult{Reason: "empty diff: nothing to merge"}
	}
	total := 0
	for _, c := range changes {
		if !allowedPath(p.AllowedPaths, c.Path) {
			return FenceResult{Reason: fmt.Sprintf("path %q is outside the allowlist", c.Path)}
		}
		total += c.LinesAdded + c.LinesDeleted
	}
	if total > p.MaxChangedLines {
		return FenceResult{Reason: fmt.Sprintf("%d changed lines exceeds the ceiling of %d", total, p.MaxChangedLines)}
	}
	return FenceResult{Passed: true}
}

func allowedPath(patterns []string, p string) bool {
	for _, pattern := range patterns {
		if matchPath(pattern, p) {
			return true
		}
	}
	return false
}

// matchPath supports a trailing "/**" for subtree matching (which
// path.Match cannot express) and delegates everything else to path.Match.
func matchPath(pattern, p string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	ok, err := path.Match(pattern, p)
	return err == nil && ok
}
