package claude

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/concourse/ci-agent/config"
)

// Adapter implements the adapter.Adapter interface using Claude Code CLI.
type Adapter struct {
	cliPath string
	model   string
}

// New creates a Claude Code adapter with the given CLI path and optional model.
func New(cliPath, model string) *Adapter {
	return &Adapter{cliPath: cliPath, model: model}
}

// BuildCommand constructs the CLI command for a review invocation.
func (a *Adapter) BuildCommand(repoDir, prompt string) *exec.Cmd {
	args := []string{"--print", "-p", prompt}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}

	cmd := exec.Command(a.cliPath, args...)
	cmd.Dir = repoDir
	return cmd
}

// BuildReviewPrompt constructs the review prompt from config and options.
func BuildReviewPrompt(repoDir string, cfg *config.ReviewConfig, diffOnly bool, baseRef string) (string, error) {
	var b strings.Builder

	b.WriteString("You are a code review agent. Analyze the repository for real defects (not style issues).\n\n")

	// Categories.
	var enabledCats []string
	for cat, cc := range cfg.Categories {
		if cc.Enabled {
			enabledCats = append(enabledCats, cat)
		}
	}
	if len(enabledCats) > 0 {
		b.WriteString(fmt.Sprintf("Focus on these categories: %s\n\n", strings.Join(enabledCats, ", ")))
	}

	// Include/exclude.
	if len(cfg.Include) > 0 {
		b.WriteString(fmt.Sprintf("Include files matching: %s\n", strings.Join(cfg.Include, ", ")))
	}
	if len(cfg.Exclude) > 0 {
		b.WriteString(fmt.Sprintf("Exclude files matching: %s\n", strings.Join(cfg.Exclude, ", ")))
	}

	// Diff mode.
	if diffOnly && baseRef != "" {
		b.WriteString(fmt.Sprintf("\nOnly review files changed in diff against base ref: %s\n", baseRef))
		b.WriteString(fmt.Sprintf("Use `git diff %s...HEAD --name-only` to determine changed files.\n", baseRef))
	}

	b.WriteString(`
For each concern:
1. Write a failing Go test that proves the defect
2. Classify severity by what the test demonstrates

Output a JSON array with this structure:
[
  {
    "title": "short description",
    "description": "detailed explanation",
    "file": "path/to/file.go",
    "line": 42,
    "severity_hint": "critical|high|medium|low",
    "category": "security|correctness|performance|maintainability|testing",
    "test_code": "package ...\n\nimport \"testing\"\n\nfunc TestXxx(t *testing.T) { ... }",
    "test_file": "path/to/file_test.go",
    "test_name": "TestXxx"
  }
]

If a concern cannot be proven with a test, omit test_code, test_file, and test_name.
Output ONLY the JSON array, no other text.
`)

	return b.String(), nil
}
