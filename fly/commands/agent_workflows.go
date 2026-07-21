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

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

// AgentWorkflowsCommand groups the workflow-definition subcommands.
type AgentWorkflowsCommand struct {
	List    WorkflowsListCommand    `command:"list" description:"List workflow definitions (latest and live versions)"`
	Show    WorkflowsShowCommand    `command:"show" description:"Print a workflow definition version"`
	Import  WorkflowsImportCommand  `command:"import" description:"Import a workflow definition YAML file as a new version"`
	SetLive WorkflowsSetLiveCommand `command:"set-live" description:"Mark a workflow definition version live (human promotion)"`

	Stats     WorkflowsStatsCommand     `command:"stats" description:"Show per-version run statistics for a workflow"`
	Annotate  WorkflowsAnnotateCommand  `command:"annotate" description:"Set an operator note on a workflow"`
	Deprecate WorkflowsDeprecateCommand `command:"deprecate" description:"Hide a workflow from default listings"`
	Restore   WorkflowsRestoreCommand   `command:"restore" description:"Un-hide a deprecated workflow"`
}

// workflowSummary mirrors agent/api/workflows.WorkflowSummary.
type workflowSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	LiveVersion   int    `json:"live_version"`
	CreatedAt     int64  `json:"created_at"`
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
		resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/workflows/"+name+"/versions", nil)
		if err != nil {
			return err
		}
		var versions []workflow.Definition
		if err := decodeOrError(resp, &versions); err != nil {
			return err
		}
		for _, v := range versions {
			if v.Version > version { // latest…
				version = v.Version
			}
		}
		for _, v := range versions {
			if v.Live { // …unless one is live
				version = v.Version
			}
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
	fmt.Fprintf(os.Stderr, "# %s version %d  hash %s  live=%v\n", def.Name, def.Version, def.ContentHash, def.Live)
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
	cfg, err := workflow.Compile(m)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"files": m})
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(cfg.Name)+"/versions",
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
	// Parse client-side first: same validation the server runs, but the
	// error message points at the local file.
	cfg, err := workflow.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	resp, err := agentAPIRequest(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(cfg.Name)+"/versions", bytes.NewReader(raw))
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
	if err := decodeOrError(resp, nil); err != nil {
		return err
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
	Tickets      int     `json:"tickets"`
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
		{Contents: "tickets", Color: color.New(color.Bold)},
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
			{Contents: strconv.Itoa(s.Tickets)},
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
