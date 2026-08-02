package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	noderunsapi "github.com/concourse/concourse/agent/api/noderuns"
	nodeupgradesapi "github.com/concourse/concourse/agent/api/nodeupgrades"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
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
	Run       NodesRunCommand       `command:"run" description:"Run an exact reusable node version"`
	Runs      NodesRunsCommand      `command:"runs" description:"List runs for an exact reusable node version"`
	ShowRun   NodesShowRunCommand   `command:"show-run" description:"Show one reusable node run"`
	CancelRun NodesCancelRunCommand `command:"cancel-run" description:"Request cancellation of a reusable node run"`
	Consumers NodesConsumersCommand `command:"consumers" description:"List consumers of an exact node version"`
	Upgrade   NodesUpgradeCommand   `command:"upgrade" description:"Upgrade selected workflows to an exact node version"`
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

// resolveDefaultNodeVersion is a human convenience for `nodes show` when
// VERSION is omitted: it picks "latest released, else latest imported" over
// a version sequence that is shared across actors and can advance between
// this scan and any later action. It must NOT be used to choose a version to
// release, run, or upgrade — those commands require an explicit version for
// exactly that reason.
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
	Json bool `long:"json" description:"Print the imported node record as JSON"`
}

func (command *NodesImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return importNodeDir(target, command.Args.Path, command.Json)
}

func importNodeDir(target rc.Target, dir string, jsonOutput bool) error {
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
	name := ""
	if err != nil {
		var catalogRequired workflow.BrokerCatalogRequiredError
		if !errors.As(err, &catalogRequired) {
			return fmt.Errorf("%s: %w", dir, err)
		}
		name = catalogRequired.DefinitionName
	} else {
		name = compiled.Name
	}
	payload, err := json.Marshal(map[string]workflow.Manifest{"files": manifest})
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(target, http.MethodPost, "/api/v1/agent/nodes/"+url.PathEscape(name)+"/versions", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	var node workflow.NodeDefinition
	if err := decodeOrError(response, &node); err != nil {
		return err
	}
	if jsonOutput {
		return displayhelpers.JsonPrint(node)
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
	Json          bool                          `long:"json" description:"Print the released node record as JSON"`
}

func (command *NodesReleaseCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return releaseNodeVersion(target, command.Args.Name, command.Args.Version, command.Compatibility, command.Json)
}

// nodeReleaseResult is the release endpoint's response (which carries only
// the release fields, not the node identity) enriched with the name and
// version the caller released — so `--json | jq -r .version` reports the
// same version the caller acted on without a second round trip.
//
// The flattening of the embedded workflow.NodeRelease is deliberate, for jq
// ergonomics: `release --json | jq .compatibility` rather than needing a
// `.release.compatibility` path. This intentionally diverges from `nodes
// show --json`, which nests the same four fields under "release" (they are
// still workflow.NodeRelease there, embedded inside workflow.NodeDefinition
// as a named, tagged field). Do not "fix" this divergence by nesting here.
//
// CAUTION: because Name/Version are declared directly on this struct while
// NodeRelease is embedded, encoding/json's shallower-field-wins rule means
// that if workflow.NodeRelease ever gains its own Name or Version field
// (e.g. the server starts echoing node identity in the release response),
// this struct will silently keep emitting the caller-supplied values here
// instead and the server's values will vanish from the JSON with no compile
// error. TestAgentNodesReleaseJSONFlattensAllNodeReleaseFields in
// agent_nodes_test.go guards against exactly that regression — keep it
// passing if NodeRelease's field set changes.
type nodeReleaseResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	workflow.NodeRelease
}

func releaseNodeVersion(target rc.Target, name string, version int, compatibility workflow.ReleaseCompatibility, jsonOutput bool) error {
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
	if jsonOutput {
		return displayhelpers.JsonPrint(nodeReleaseResult{Name: name, Version: version, NodeRelease: release})
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

type NodesRunCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Exact node version"`
	} `positional-args:"yes"`
	Input          []string `long:"input" description:"Bind a named input as NAME=SNAPSHOT-ID (repeatable)"`
	Param          []string `long:"param" description:"Set a declared node parameter as NAME=VALUE (repeatable)"`
	IdempotencyKey string   `long:"idempotency-key" description:"Idempotency key for this invocation (generated when omitted)"`
	Json           bool     `long:"json" description:"Print command result as JSON"`
}

func (command *NodesRunCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	detail, err := runNodeVersion(target, command.Args.Name, command.Args.Version, command.Input, command.Param, command.IdempotencyKey)
	if err != nil {
		return err
	}
	return printAgentWorkflowRunDetail(Fly.Target, detail, command.Json)
}

func runNodeVersion(target rc.Target, name string, version int, inputValues, paramValues []string, idempotencyKey string) (workflowrunsapi.RunDetail, error) {
	if version <= 0 {
		return workflowrunsapi.RunDetail{}, fmt.Errorf("agent node run: version must be positive")
	}
	inputs, err := parseAgentWorkflowRunInputs(inputValues)
	if err != nil {
		return workflowrunsapi.RunDetail{}, err
	}
	params, err := parseNodeRunParameters(paramValues)
	if err != nil {
		return workflowrunsapi.RunDetail{}, err
	}
	if idempotencyKey == "" {
		idempotencyKey, err = newAgentWorkflowRunIdempotencyKey()
		if err != nil {
			return workflowrunsapi.RunDetail{}, err
		}
	}
	payload, err := json.Marshal(noderunsapi.CreateRequest{Inputs: inputs, Params: params, IdempotencyKey: idempotencyKey})
	if err != nil {
		return workflowrunsapi.RunDetail{}, err
	}
	response, err := agentAPIRequestWithType(target, http.MethodPost, nodeVersionRunsPath(name, version), "application/json", bytes.NewReader(payload))
	if err != nil {
		return workflowrunsapi.RunDetail{}, err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return workflowrunsapi.RunDetail{}, err
	}
	return detail, nil
}

func parseNodeRunParameters(values []string) (map[string]string, error) {
	params := make(map[string]string, len(values))
	for _, value := range values {
		name, parameter, found := strings.Cut(value, "=")
		if !found || strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("agent node run: --param must be NAME=VALUE")
		}
		if _, duplicate := params[name]; duplicate {
			return nil, fmt.Errorf("agent node run: duplicate parameter %q", name)
		}
		params[name] = parameter
	}
	return params, nil
}

type NodesRunsCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Exact node version"`
	} `positional-args:"yes"`
	Status string `long:"status" description:"Filter by run status"`
	Limit  int    `long:"limit" default:"100" description:"Maximum runs to return (1-1000)"`
	Json   bool   `long:"json" description:"Print command result as JSON"`
}

func (command *NodesRunsCommand) Execute([]string) error {
	if command.Args.Version <= 0 || command.Limit < 1 || command.Limit > 1000 || command.Status != "" && !agentWorkflowRunStatusValid(command.Status) {
		return fmt.Errorf("agent node runs: invalid version, status, or limit")
	}
	query := url.Values{"limit": {strconv.Itoa(command.Limit)}}
	if command.Status != "" {
		query.Set("status", command.Status)
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(target, http.MethodGet, nodeVersionRunsPath(command.Args.Name, command.Args.Version)+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	var runs []workflowrunsapi.RunSummary
	if err := decodeOrError(response, &runs); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(runs)
	}
	return renderAgentWorkflowRuns(runs)
}

type NodesShowRunCommand struct {
	Args struct {
		Name  string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		RunID string `positional-arg-name:"RUN-ID" required:"true" description:"Durable node run ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *NodesShowRunCommand) Execute([]string) error {
	runID, err := snapshot.ParseWorkflowRunID(command.Args.RunID)
	if err != nil {
		return fmt.Errorf("agent node run: %w", err)
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(target, http.MethodGet, nodeRunPath(command.Args.Name, runID), nil)
	if err != nil {
		return err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return err
	}
	return printAgentWorkflowRunDetail(Fly.Target, detail, command.Json)
}

type NodesCancelRunCommand struct {
	Args struct {
		Name  string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		RunID string `positional-arg-name:"RUN-ID" required:"true" description:"Durable node run ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *NodesCancelRunCommand) Execute([]string) error {
	runID, err := snapshot.ParseWorkflowRunID(command.Args.RunID)
	if err != nil {
		return fmt.Errorf("agent node run: %w", err)
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(target, http.MethodPost, nodeRunPath(command.Args.Name, runID)+"/cancel", nil)
	if err != nil {
		return err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return err
	}
	return printAgentWorkflowRunDetail(Fly.Target, detail, command.Json)
}

func nodeVersionRunsPath(name string, version int) string {
	return "/api/v1/agent/nodes/" + url.PathEscape(name) + "/versions/" + strconv.Itoa(version) + "/runs"
}

func nodeRunPath(name string, runID snapshot.WorkflowRunID) string {
	return "/api/v1/agent/nodes/" + url.PathEscape(name) + "/runs/" + url.PathEscape(runID.String())
}

type NodesConsumersCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Exact node version"`
	} `positional-args:"yes"`
	Cursor string `long:"cursor" description:"Continue after an opaque cursor returned by an earlier page"`
	Limit  int    `long:"limit" default:"100" description:"Maximum consumers to return (1-1000)"`
	Json   bool   `long:"json" description:"Print command result as JSON"`
}

func (command *NodesConsumersCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	consumers, next, err := listNodeConsumers(target, command.Args.Name, command.Args.Version, command.Limit, command.Cursor)
	if err != nil {
		return err
	}
	if next != "" {
		fmt.Fprintf(os.Stderr, "# next cursor: %s\n", next)
	}
	if command.Json {
		return displayhelpers.JsonPrint(consumers)
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "workflow", Color: color.New(color.Bold)},
		{Contents: "version", Color: color.New(color.Bold)},
		{Contents: "live", Color: color.New(color.Bold)},
		{Contents: "instance", Color: color.New(color.Bold)},
	}}
	for _, consumer := range consumers {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: consumer.WorkflowName},
			{Contents: strconv.Itoa(consumer.WorkflowVersion)},
			{Contents: strconv.FormatBool(consumer.Live)},
			{Contents: consumer.Binding.InstanceName},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func listNodeConsumers(target rc.Target, name string, version, limit int, cursor string) ([]nodeupgradesapi.Consumer, string, error) {
	if version <= 0 {
		return nil, "", fmt.Errorf("agent node consumers: version must be positive")
	}
	if limit <= 0 || limit > workflow.MaxVersionPageSize {
		return nil, "", fmt.Errorf("agent node consumers: limit must be between 1 and %d", workflow.MaxVersionPageSize)
	}
	if cursor != "" {
		if err := validateNodeConsumerCursor(cursor); err != nil {
			return nil, "", fmt.Errorf("agent node consumers: invalid cursor")
		}
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	response, err := agentAPIRequest(target, http.MethodGet, nodeVersionPath(name, version)+"/consumers?"+query.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	next := response.Header.Get("X-Next-Cursor")
	var consumers []nodeupgradesapi.Consumer
	if err := decodeOrError(response, &consumers); err != nil {
		return nil, "", err
	}
	if next != "" {
		if err := validateNodeConsumerCursor(next); err != nil {
			return nil, "", fmt.Errorf("server returned invalid node consumer cursor %q", next)
		}
	}
	return consumers, next, nil
}

func validateNodeConsumerCursor(raw string) error {
	if raw == "" || len(raw) > 2048 {
		return fmt.Errorf("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != raw {
		return fmt.Errorf("invalid cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor workflow.NodeConsumerCursor
	if err := decoder.Decode(&cursor); err != nil || cursor.WorkflowDefinitionID <= 0 || strings.TrimSpace(cursor.InstanceName) == "" || cursor.InstanceName != strings.TrimSpace(cursor.InstanceName) {
		return fmt.Errorf("invalid cursor")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid cursor")
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("invalid cursor")
	}
	return nil
}

type NodesUpgradeCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Exact successor node version"`
	} `positional-args:"yes"`
	Workflow []string `long:"workflow" required:"true" description:"Workflow to upgrade (repeatable)"`
	Json     bool     `long:"json" description:"Print command result as JSON"`
}

func (command *NodesUpgradeCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	result, err := upgradeNodeConsumers(target, command.Args.Name, command.Args.Version, command.Workflow)
	if err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(result)
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "workflow", Color: color.New(color.Bold)},
		{Contents: "old", Color: color.New(color.Bold)},
		{Contents: "new", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
		{Contents: "detail", Color: color.New(color.Bold)},
	}}
	for _, item := range result.Workflows {
		detail := item.Error
		if item.Obligations != nil {
			payload, err := json.Marshal(item.Obligations)
			if err != nil {
				return err
			}
			detail = string(payload)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: item.Workflow},
			{Contents: strconv.Itoa(item.OldVersion)},
			{Contents: strconv.Itoa(item.NewVersion)},
			{Contents: string(item.Status)},
			{Contents: detail},
		})
	}
	sort.Sort(table.Data)
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func upgradeNodeConsumers(target rc.Target, name string, version int, workflows []string) (workflow.NodeUpgradeResult, error) {
	if version <= 0 || len(workflows) == 0 {
		return workflow.NodeUpgradeResult{}, fmt.Errorf("agent node upgrade: positive version and at least one workflow are required")
	}
	seen := make(map[string]struct{}, len(workflows))
	for _, selected := range workflows {
		if strings.TrimSpace(selected) == "" || selected != strings.TrimSpace(selected) {
			return workflow.NodeUpgradeResult{}, fmt.Errorf("agent node upgrade: invalid workflow selection")
		}
		if _, duplicate := seen[selected]; duplicate {
			return workflow.NodeUpgradeResult{}, fmt.Errorf("agent node upgrade: duplicate workflow %q", selected)
		}
		seen[selected] = struct{}{}
	}
	payload, err := json.Marshal(struct {
		Workflows []string `json:"workflows"`
	}{Workflows: workflows})
	if err != nil {
		return workflow.NodeUpgradeResult{}, err
	}
	response, err := agentAPIRequestWithType(target, http.MethodPost, nodeVersionPath(name, version)+"/upgrades", "application/json", bytes.NewReader(payload))
	if err != nil {
		return workflow.NodeUpgradeResult{}, err
	}
	var result workflow.NodeUpgradeResult
	if err := decodeOrError(response, &result); err != nil {
		return workflow.NodeUpgradeResult{}, err
	}
	return result, nil
}

func nodeVersionPath(name string, version int) string {
	return "/api/v1/agent/nodes/" + url.PathEscape(name) + "/versions/" + strconv.Itoa(version)
}
