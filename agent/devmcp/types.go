// Package devmcp defines the dev-mcp per-repo tool contract
// (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §3.1)
// and a streamable-HTTP MCP client for invoking a repo's dev-mcp
// implementation from Go code.
package devmcp

// Status is the three-way result taxonomy shared by build/run_tests/lint:
// ok = the checked thing passed, failed = it ran and found problems,
// error = tooling itself broke.
type Status string

const (
	StatusOK     Status = "ok"
	StatusFailed Status = "failed"
	StatusError  Status = "error"
)

// Tool names as registered on every dev-mcp server.
const (
	ToolListComponents     = "list_components"
	ToolBuild              = "build"
	ToolRunTests           = "run_tests"
	ToolLint               = "lint"
	ToolAffectedComponents = "affected_components"
)

// Transport constants (00-shared-contracts.md §8.1, §8.5).
const (
	DefaultListenAddr = ":7780"
	EndpointPath      = "/mcp"
	HealthPath        = "/healthz"
	EnvListenAddr     = "MCP_LISTEN_ADDR"
	EnvEndpoint       = "DEV_MCP_URL"
)

// Component is one entry of the list_components result. IDs are opaque,
// repo-defined, stable strings.
type Component struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Kind        string   `json:"kind"` // service|library|cli|web|docs|other
}

// Failure is one structured failure in a ToolResult (optional; emitted
// when the implementation can parse test names / lint rules).
type Failure struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// ToolResult is the shared result payload of build, run_tests, and lint.
type ToolResult struct {
	Status          Status    `json:"status"`
	Summary         string    `json:"summary"`
	DurationSeconds float64   `json:"duration_seconds"`
	OutputTail      string    `json:"output_tail,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	Failures        []Failure `json:"failures,omitempty"`
}

// AffectedResult is the result payload of affected_components.
type AffectedResult struct {
	Components    []string `json:"components"`
	UnmappedPaths []string `json:"unmapped_paths,omitempty"`
}
