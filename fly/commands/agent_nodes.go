package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// AgentNodesCommand groups the reusable node-definition catalog commands.
type AgentNodesCommand struct {
	List      NodesListCommand      `command:"list" description:"List reusable node definitions"`
	Show      NodesShowCommand      `command:"show" description:"Print a reusable node definition version"`
	Import    NodesImportCommand    `command:"import" description:"Import a reusable node source directory"`
	Release   NodesReleaseCommand   `command:"release" description:"Release a node version"`
	Deprecate NodesDeprecateCommand `command:"deprecate" description:"Mark a node version deprecated"`
	Restore   NodesRestoreCommand   `command:"restore" description:"Clear a node version deprecation"`
}

type nodeSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	CreatedAt     int64  `json:"created_at"`
}

type NodesListCommand struct {
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *NodesListCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(target, http.MethodGet, "/api/v1/agent/nodes", nil)
	if err != nil {
		return err
	}
	var nodes []nodeSummary
	if err := decodeOrError(response, &nodes); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(nodes)
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "name", Color: color.New(color.Bold)},
		{Contents: "latest", Color: color.New(color.Bold)},
		{Contents: "description", Color: color.New(color.Bold)},
	}}
	for _, node := range nodes {
		table.Data = append(table.Data, ui.TableRow{{Contents: node.Name}, {Contents: strconv.Itoa(node.LatestVersion)}, {Contents: node.Description}})
	}
	sort.Sort(table.Data)
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type NodesShowCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" description:"Version number (default: latest released, else latest imported)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print the full node record as JSON"`
}

func (command *NodesShowCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	name := url.PathEscape(command.Args.Name)
	version := command.Args.Version
	if version == 0 {
		version, err = resolveDefaultNodeVersion(target, name)
		if err != nil {
			return err
		}
	}
	response, err := agentAPIRequest(target, http.MethodGet, "/api/v1/agent/nodes/"+name+"/versions/"+strconv.Itoa(version), nil)
	if err != nil {
		return err
	}
	var node workflow.NodeDefinition
	if err := decodeOrError(response, &node); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(node)
	}
	fmt.Fprintf(os.Stderr, "# %s version %d  hash %s\n", node.Name, node.Version, node.ContentHash)
	if node.Release.ReleasedAt > 0 {
		fmt.Fprintf(os.Stderr, "# released %s by %s (%s)\n", node.Release.Compatibility, node.Release.ReleasedBy, strconv.FormatInt(node.Release.ReleasedAt, 10))
	}
	if node.DeprecatedAt > 0 {
		fmt.Fprintf(os.Stderr, "# deprecated by %s (%s)\n", node.DeprecatedBy, strconv.FormatInt(node.DeprecatedAt, 10))
	}
	_, err = fmt.Print(node.SourceManifest[workflow.NodeFileName])
	return err
}

func resolveDefaultNodeVersion(target rc.Target, escapedName string) (int, error) {
	latestImported, latestReleased := 0, 0
	err := followAgentHistoryPages("node version", workflowVersionCursor, func(cursor string) (string, bool, error) {
		query := url.Values{"limit": {strconv.Itoa(workflow.MaxVersionPageSize)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		response, err := agentAPIRequest(target, http.MethodGet, "/api/v1/agent/nodes/"+escapedName+"/versions?"+query.Encode(), nil)
		if err != nil {
			return "", false, err
		}
		next := response.Header.Get("X-Next-Cursor")
		var nodes []workflow.NodeDefinition
		if err := decodeOrError(response, &nodes); err != nil {
			return "", false, err
		}
		for _, node := range nodes {
			if node.Version > latestImported {
				latestImported = node.Version
			}
			if node.Release.ReleasedAt > 0 && node.Version > latestReleased {
				latestReleased = node.Version
			}
		}
		return next, false, nil
	})
	if err != nil {
		return 0, err
	}
	if latestReleased > 0 {
		return latestReleased, nil
	}
	if latestImported == 0 {
		return 0, fmt.Errorf("node has no versions")
	}
	return latestImported, nil
}

type NodesImportCommand struct {
	Args struct {
		Path string `positional-arg-name:"PATH" required:"true" description:"Node source directory"`
	} `positional-args:"yes"`
}

func (command *NodesImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return importNodeDir(target, command.Args.Path)
}

func importNodeDir(target rc.Target, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: node import requires a source directory", dir)
	}
	manifest, err := workflow.ManifestFromDir(dir)
	if err != nil {
		return err
	}
	compiled, err := workflow.CompileNodeDefinition(manifest)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	payload, err := json.Marshal(map[string]workflow.Manifest{"files": manifest})
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(target, http.MethodPost, "/api/v1/agent/nodes/"+url.PathEscape(compiled.Name)+"/versions", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	var node workflow.NodeDefinition
	if err := decodeOrError(response, &node); err != nil {
		return err
	}
	_, err = fmt.Printf("imported %s version %d (hash %.12s)\n", node.Name, node.Version, node.ContentHash)
	return err
}

type NodesReleaseCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Node version"`
	} `positional-args:"yes"`
	Compatibility workflow.ReleaseCompatibility `long:"compatibility" required:"true" description:"Release compatibility declaration"`
}

func (command *NodesReleaseCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return releaseNodeVersion(target, command.Args.Name, command.Args.Version, command.Compatibility)
}

func releaseNodeVersion(target rc.Target, name string, version int, compatibility workflow.ReleaseCompatibility) error {
	payload, err := json.Marshal(struct {
		Compatibility workflow.ReleaseCompatibility `json:"compatibility"`
	}{Compatibility: compatibility})
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(target, http.MethodPut, "/api/v1/agent/nodes/"+url.PathEscape(name)+"/versions/"+strconv.Itoa(version)+"/release", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	var release workflow.NodeRelease
	if err := decodeOrError(response, &release); err != nil {
		return err
	}
	_, err = fmt.Printf("released %s version %d as %s\n", name, version, release.Compatibility)
	return err
}

type NodesDeprecateCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Node version"`
	} `positional-args:"yes"`
}

func (command *NodesDeprecateCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return setNodeDeprecation(target, command.Args.Name, command.Args.Version, true)
}

type NodesRestoreCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Node version"`
	} `positional-args:"yes"`
}

func (command *NodesRestoreCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return setNodeDeprecation(target, command.Args.Name, command.Args.Version, false)
}

func setNodeDeprecation(target rc.Target, name string, version int, deprecated bool) error {
	payload, err := json.Marshal(struct {
		Deprecated bool `json:"deprecated"`
	}{Deprecated: deprecated})
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(target, http.MethodPut, "/api/v1/agent/nodes/"+url.PathEscape(name)+"/versions/"+strconv.Itoa(version)+"/deprecation", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := decodeOrError(response, nil); err != nil {
		return err
	}
	verb := "restored"
	if deprecated {
		verb = "deprecated"
	}
	_, err = fmt.Printf("%s %s version %d\n", verb, name, version)
	return err
}
