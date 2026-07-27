package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

// AgentWorkflowsCommand groups the workflow-definition subcommands.
type AgentWorkflowsCommand struct {
	List      WorkflowsListCommand      `command:"list" description:"List workflow definitions (latest and live versions)"`
	Show      WorkflowsShowCommand      `command:"show" description:"Print a workflow definition version"`
	Import    WorkflowsImportCommand    `command:"import" description:"Import a workflow definition YAML file as a new version"`
	SetLive   WorkflowsSetLiveCommand   `command:"set-live" description:"Mark a workflow definition version live (human promotion)"`
	Run       WorkflowsRunCommand       `command:"run" description:"Create a durable workflow run from named snapshot inputs"`
	Runs      WorkflowsRunsCommand      `command:"runs" description:"List durable runs for one workflow definition"`
	ShowRun   WorkflowsShowRunCommand   `command:"show-run" description:"Inspect one durable workflow run or its output manifest"`
	CancelRun WorkflowsCancelRunCommand `command:"cancel-run" description:"Request cancellation of a durable workflow run"`
	RetryRun  WorkflowsRetryRunCommand  `command:"retry-run" description:"Retry a terminal durable workflow run with its pinned inputs"`

	Stats     WorkflowsStatsCommand     `command:"stats" description:"Show per-version run statistics for a workflow"`
	Annotate  WorkflowsAnnotateCommand  `command:"annotate" description:"Set an operator note on a workflow"`
	Deprecate WorkflowsDeprecateCommand `command:"deprecate" description:"Hide a workflow from default listings"`
	Restore   WorkflowsRestoreCommand   `command:"restore" description:"Un-hide a deprecated workflow"`
}

// workflowSummary mirrors agent/api/workflows.WorkflowSummary.
type workflowSummary struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	LatestVersion    int    `json:"latest_version"`
	SchemaVersion    int    `json:"schema_version"`
	SignatureVersion int    `json:"signature_version"`
	ContentHash      string `json:"content_hash"`
	LiveVersion      int    `json:"live_version"`
	CreatedAt        int64  `json:"created_at"`
}

func agentAPIRequest(target rc.Target, method, path string, body io.Reader) (*http.Response, error) {
	return agentAPIRequestWithType(target, method, path, "", body)
}

func agentAPIRequestWithType(target rc.Target, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, target.URL()+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// target.Client().HTTPClient() carries the target's auth transport.
	return target.Client().HTTPClient().Do(req)
}

func decodeOrError(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func loadAgentTarget() (rc.Target, error) {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return nil, err
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return target, nil
}

type WorkflowsListCommand struct {
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsListCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/workflows", nil)
	if err != nil {
		return err
	}
	var summaries []workflowSummary
	if err := decodeOrError(resp, &summaries); err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(summaries)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "name", Color: color.New(color.Bold)},
		{Contents: "latest", Color: color.New(color.Bold)},
		{Contents: "schema", Color: color.New(color.Bold)},
		{Contents: "signature", Color: color.New(color.Bold)},
		{Contents: "live", Color: color.New(color.Bold)},
		{Contents: "description", Color: color.New(color.Bold)},
	}}
	for _, s := range summaries {
		live := "none"
		if s.LiveVersion > 0 {
			live = strconv.Itoa(s.LiveVersion)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: s.Name},
			{Contents: strconv.Itoa(s.LatestVersion)},
			{Contents: strconv.Itoa(s.SchemaVersion)},
			{Contents: strconv.Itoa(s.SignatureVersion)},
			{Contents: live},
			{Contents: s.Description},
		})
	}
	sort.Sort(table.Data)
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type WorkflowsShowCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
		Version int    `positional-arg-name:"VERSION" description:"Version number (default: live version, else latest)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print the full definition record as JSON"`
}

func (command *WorkflowsShowCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	name := url.PathEscape(command.Args.Name)
	version := command.Args.Version
	if version == 0 {
		version, err = resolveDefaultWorkflowVersion(target, name)
		if err != nil {
			return err
		}
	}

	resp, err := agentAPIRequest(target, "GET",
		"/api/v1/agent/workflows/"+name+"/versions/"+strconv.Itoa(version), nil)
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(def)
	}
	fmt.Fprintf(os.Stderr, "# %s version %d  schema=%d signature=%d  hash %s  live=%v\n",
		def.Name, def.Version, def.SchemaVersion, def.SignatureVersion, def.ContentHash, def.Live)
	// The promotion audit: who made this version live, and when. Absent for a
	// version that has never been promoted.
	if def.PromotedAt > 0 {
		promotedBy := def.PromotedBy
		if promotedBy == "" {
			promotedBy = "unknown"
		}
		fmt.Fprintf(os.Stderr, "# promoted by %s at %s\n",
			promotedBy, time.Unix(def.PromotedAt, 0).Local().Format("2006-01-02 15:04"))
	}
	fmt.Print(def.RawYAML)
	// Manifest-backed definitions get a source summary (per-file sizes)
	// instead of a tree dump; stderr keeps stdout pipeable YAML.
	if len(def.SourceManifest) > 1 {
		fmt.Fprintf(os.Stderr, "# source files (%d):\n", len(def.SourceManifest))
		for _, p := range def.SourceManifest.Paths() {
			fmt.Fprintf(os.Stderr, "#   %-48s %7d bytes\n", p, len(def.SourceManifest[p]))
		}
	}
	return nil
}

func resolveDefaultWorkflowVersion(target rc.Target, escapedName string) (int, error) {
	latest := 0
	cursor := ""
	seen := map[string]struct{}{}
	for {
		query := url.Values{
			"limit": {strconv.Itoa(workflow.MaxVersionPageSize)},
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		resp, err := agentAPIRequest(
			target,
			http.MethodGet,
			"/api/v1/agent/workflows/"+escapedName+"/versions?"+query.Encode(),
			nil,
		)
		if err != nil {
			return 0, err
		}
		nextCursor := resp.Header.Get("X-Next-Cursor")
		var versions []workflow.Definition
		if err := decodeOrError(resp, &versions); err != nil {
			return 0, err
		}
		for _, candidate := range versions {
			if candidate.Version > latest {
				latest = candidate.Version
			}
			if candidate.Live {
				return candidate.Version, nil
			}
		}
		if nextCursor == "" {
			if latest == 0 {
				return 0, fmt.Errorf("workflow has no versions")
			}
			return latest, nil
		}
		parsed, err := strconv.Atoi(nextCursor)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("server returned invalid workflow version cursor %q", nextCursor)
		}
		if _, duplicate := seen[nextCursor]; duplicate {
			return 0, fmt.Errorf("server returned repeated workflow version cursor %q", nextCursor)
		}
		seen[nextCursor] = struct{}{}
		cursor = nextCursor
	}
}

type WorkflowsImportCommand struct {
	Args struct {
		Path string `positional-arg-name:"PATH" required:"true" description:"Workflow definition YAML file, a workflow source directory, or a directory of workflow directories"`
	} `positional-args:"yes"`
	SetLive bool `long:"set-live" description:"Promote each imported version live immediately (auto-promote deploy pipelines; manual set-live stays the default)"`
}

func (command *WorkflowsImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	info, err := os.Stat(command.Args.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return importWorkflowFile(target, command.Args.Path, command.SetLive)
	}

	dirs, err := workflow.DiscoverWorkflowDirs(command.Args.Path)
	if err != nil {
		return err
	}
	// Each import is independent and idempotent: a failure leaves the
	// others in place and a re-run converges (design 2026-07-17 §5).
	for _, dir := range dirs {
		if err := importWorkflowDir(target, dir, command.SetLive); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}

func importWorkflowDir(target rc.Target, dir string, setLive bool) error {
	m, err := workflow.ManifestFromDir(dir)
	if err != nil {
		return err
	}
	// Compile client-side first: same validation the server runs, but
	// the error message points at local files.
	compiled, err := workflow.CompileDefinition(m)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"files": m})
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(compiled.Name)+"/versions",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}
	fmt.Printf("imported %s version %d (hash %.12s)\n", def.Name, def.Version, def.ContentHash)

	if setLive {
		return setLiveVersion(target, def.Name, def.Version)
	}
	return nil
}

func importWorkflowFile(target rc.Target, path string, setLive bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Compile the raw file as a one-file manifest so local validation matches
	// the server, including resolution of referenced assets.
	compiled, err := workflow.CompileDefinition(workflow.Manifest{"workflow.yml": string(raw)})
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	resp, err := agentAPIRequest(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(compiled.Name)+"/versions", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}
	fmt.Printf("imported %s version %d (hash %.12s)\n", def.Name, def.Version, def.ContentHash)

	if setLive {
		return setLiveVersion(target, def.Name, def.Version)
	}
	return nil
}

func setLiveVersion(target rc.Target, name string, version int) error {
	resp, err := agentAPIRequest(target, "PUT",
		"/api/v1/agent/workflows/"+url.PathEscape(name)+"/versions/"+strconv.Itoa(version)+"/live", nil)
	if err != nil {
		return err
	}
	var result workflow.PromotionResult
	if err := decodeOrError(resp, &result); err != nil {
		return err
	}
	if result.SignatureChanged && result.PreviousLive != nil {
		fmt.Printf("warning: public signature changed from %d to %d\n",
			result.PreviousLive.SignatureVersion, result.Target.SignatureVersion)
	}
	fmt.Printf("workflow %s version %d is now live\n", name, version)
	return nil
}

type WorkflowsSetLiveCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Version number to mark live"`
	} `positional-args:"yes"`
}

func (command *WorkflowsSetLiveCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return setLiveVersion(target, command.Args.Name, command.Args.Version)
}

// workflowVersionStats mirrors agent/schema.WorkflowVersionStats (the derived
// fields the stats handler emits).
type workflowVersionStats struct {
	Version      *int    `json:"version"`
	Runs         int     `json:"runs"`
	WorkflowRuns int     `json:"workflow_runs"`
	SuccessRate  float64 `json:"success_rate"`
	AvgCostUSD   float64 `json:"avg_cost_usd"`
	AvgTurns     float64 `json:"avg_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

type WorkflowsStatsCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsStatsCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	resp, err := agentAPIRequest(target, "GET",
		"/api/v1/agent/workflows/"+url.PathEscape(command.Args.Name)+"/stats", nil)
	if err != nil {
		return err
	}
	var rows []workflowVersionStats
	if err := decodeOrError(resp, &rows); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(rows)
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "version", Color: color.New(color.Bold)},
		{Contents: "runs", Color: color.New(color.Bold)},
		{Contents: "workflow runs", Color: color.New(color.Bold)},
		{Contents: "success", Color: color.New(color.Bold)},
		{Contents: "avg cost", Color: color.New(color.Bold)},
		{Contents: "avg turns", Color: color.New(color.Bold)},
	}}
	for _, s := range rows {
		version := "ad-hoc"
		if s.Version != nil {
			version = "v" + strconv.Itoa(*s.Version)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: version},
			{Contents: strconv.Itoa(s.Runs)},
			{Contents: strconv.Itoa(s.WorkflowRuns)},
			{Contents: fmt.Sprintf("%.0f%%", s.SuccessRate*100)},
			{Contents: fmt.Sprintf("$%.2f", s.AvgCostUSD)},
			{Contents: fmt.Sprintf("%.1f", s.AvgTurns)},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func putWorkflowLifecycle(target rc.Target, name string, body map[string]any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "PUT",
		"/api/v1/agent/workflows/"+url.PathEscape(name),
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return decodeOrError(resp, nil)
}

type WorkflowsAnnotateCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
		Note string `positional-arg-name:"NOTE" required:"true" description:"Operator note"`
	} `positional-args:"yes"`
}

func (command *WorkflowsAnnotateCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	if err := putWorkflowLifecycle(target, command.Args.Name, map[string]any{"annotation": command.Args.Note}); err != nil {
		return err
	}
	fmt.Printf("annotated %s\n", command.Args.Name)
	return nil
}

type WorkflowsDeprecateCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
}

func (command *WorkflowsDeprecateCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	if err := putWorkflowLifecycle(target, command.Args.Name, map[string]any{"hidden": true}); err != nil {
		return err
	}
	fmt.Printf("deprecated %s (hidden from default listings)\n", command.Args.Name)
	return nil
}

type WorkflowsRestoreCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
}

func (command *WorkflowsRestoreCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	if err := putWorkflowLifecycle(target, command.Args.Name, map[string]any{"hidden": false}); err != nil {
		return err
	}
	fmt.Printf("restored %s\n", command.Args.Name)
	return nil
}
