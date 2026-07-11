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
	req, err := http.NewRequest(method, target.URL()+path, body)
	if err != nil {
		return nil, err
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
	return nil
}

type WorkflowsImportCommand struct {
	Args struct {
		File string `positional-arg-name:"FILE" required:"true" description:"Path to the workflow definition YAML"`
	} `positional-args:"yes"`
}

func (command *WorkflowsImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(command.Args.File)
	if err != nil {
		return err
	}
	// Parse client-side first: same validation the server runs, but the
	// error message points at the local file.
	cfg, err := workflow.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", command.Args.File, err)
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

	resp, err := agentAPIRequest(target, "PUT",
		"/api/v1/agent/workflows/"+url.PathEscape(command.Args.Name)+"/versions/"+strconv.Itoa(command.Args.Version)+"/live", nil)
	if err != nil {
		return err
	}
	if err := decodeOrError(resp, nil); err != nil {
		return err
	}

	fmt.Printf("workflow %s version %d is now live\n", command.Args.Name, command.Args.Version)
	return nil
}
