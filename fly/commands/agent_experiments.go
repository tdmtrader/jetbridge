package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

const (
	agentExperimentsPath             = "/api/v1/agent/experiments"
	maximumExperimentDefinitionBytes = 1 << 20
)

// These read models mirror agent/api/experiments without importing the HTTP
// handler package into Fly. Keeping the CLI on the wire contract avoids
// coupling a client binary to ATC storage adapters.
type agentExperimentRecord struct {
	ID          experiment.ID         `json:"id"`
	TeamName    string                `json:"team_name"`
	Definition  experiment.Definition `json:"definition"`
	Revision    int64                 `json:"revision"`
	CreatedBy   string                `json:"created_by"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	StartedAt   *time.Time            `json:"started_at,omitempty"`
	CompletedAt *time.Time            `json:"completed_at,omitempty"`
}

type agentExperimentCell struct {
	ID                       experiment.CellID       `json:"id"`
	ExperimentID             experiment.ID           `json:"experiment_id"`
	FixtureID                snapshot.DatabaseID     `json:"fixture_id"`
	FixtureLabel             string                  `json:"fixture_label"`
	FixtureRole              experiment.FixtureRole  `json:"fixture_role"`
	VariantID                snapshot.DatabaseID     `json:"variant_id"`
	VariantLabel             string                  `json:"variant_label"`
	Repetition               int                     `json:"repetition"`
	Status                   experiment.CellStatus   `json:"status"`
	CandidateWorkflowRunID   *snapshot.WorkflowRunID `json:"candidate_workflow_run_id,omitempty"`
	EvaluatorWorkflowRunID   *snapshot.WorkflowRunID `json:"evaluator_workflow_run_id,omitempty"`
	MeasurementSnapshotID    *snapshot.SnapshotID    `json:"measurement_snapshot_id,omitempty"`
	CandidateFailureCategory string                  `json:"candidate_failure_category,omitempty"`
	CreatedAt                time.Time               `json:"created_at"`
	UpdatedAt                time.Time               `json:"updated_at"`
	CompletedAt              *time.Time              `json:"completed_at,omitempty"`
}

type agentExperimentValidationResult struct {
	Valid         bool  `json:"valid"`
	Revision      int64 `json:"revision"`
	ExpectedCells int   `json:"expected_cells"`
}

// AgentExperimentsCommand manages durable, snapshot-pinned workflow
// experiments. Mutations remain revisioned API operations: the convenience
// add commands fetch the current draft, change one collection, and PUT the
// whole definition with the fetched (or explicitly supplied) revision.
type AgentExperimentsCommand struct {
	Create     ExperimentsCreateCommand     `command:"create" description:"Create an experiment from a complete JSON definition"`
	Update     ExperimentsUpdateCommand     `command:"update" description:"Replace a draft experiment definition at an expected revision"`
	AddVariant ExperimentsAddVariantCommand `command:"add-variant" description:"Add a pinned workflow or function variant to a draft"`
	AddFixture ExperimentsAddFixtureCommand `command:"add-fixture" description:"Add a snapshot fixture to a draft"`
	Validate   ExperimentsValidateCommand   `command:"validate" description:"Run the authoritative start preflight without changing state"`
	Start      ExperimentsStartCommand      `command:"start" description:"Freeze and start an experiment at an expected revision"`
	Cancel     ExperimentsCancelCommand     `command:"cancel" description:"Cancel an experiment at an expected revision"`
	List       ExperimentsListCommand       `command:"list" description:"List experiments"`
	Show       ExperimentsShowCommand       `command:"show" description:"Show one experiment"`
	Cells      ExperimentsCellsCommand      `command:"cells" description:"List experiment cells or show one cell"`
	Scorecard  ExperimentsScorecardCommand  `command:"scorecard" description:"Show experiment distributions and paired comparisons"`
}

type ExperimentsCreateCommand struct {
	Args struct {
		Path string `positional-arg-name:"FILE" required:"true" description:"Complete experiment definition JSON file (use - for stdin)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsCreateCommand) Execute([]string) error {
	definition, err := readAgentExperimentDefinition(command.Args.Path)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executeDefinition(target, os.Stdout, definition)
}

func (command *ExperimentsCreateCommand) execute(target rc.Target, output io.Writer) error {
	definition, err := readAgentExperimentDefinition(command.Args.Path)
	if err != nil {
		return err
	}
	return command.executeDefinition(target, output, definition)
}

func (command *ExperimentsCreateCommand) executeDefinition(target rc.Target, output io.Writer, definition experiment.Definition) error {
	var stored agentExperimentRecord
	if err := agentExperimentJSONRequest(target, http.MethodPost, agentExperimentsPath, definition, &stored); err != nil {
		return err
	}
	return renderAgentExperiment(output, stored, command.Json, Fly.PrintTableHeaders)
}

type ExperimentsUpdateCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
		Path       string `positional-arg-name:"FILE" required:"true" description:"Complete replacement definition JSON file (use - for stdin)"`
	} `positional-args:"yes"`
	Revision int64 `long:"revision" required:"true" description:"Expected positive draft revision"`
	Json     bool  `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsUpdateCommand) Execute([]string) error {
	id, definition, err := command.prepare()
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id, definition)
}

func (command *ExperimentsUpdateCommand) execute(target rc.Target, output io.Writer) error {
	id, definition, err := command.prepare()
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id, definition)
}

func (command *ExperimentsUpdateCommand) prepare() (experiment.ID, experiment.Definition, error) {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return 0, experiment.Definition{}, err
	}
	if command.Revision <= 0 {
		return 0, experiment.Definition{}, fmt.Errorf("experiment revision must be positive")
	}
	definition, err := readAgentExperimentDefinition(command.Args.Path)
	return id, definition, err
}

func (command *ExperimentsUpdateCommand) executePrepared(target rc.Target, output io.Writer, id experiment.ID, definition experiment.Definition) error {
	stored, err := updateAgentExperiment(target, id, command.Revision, definition)
	if err != nil {
		return err
	}
	return renderAgentExperiment(output, stored, command.Json, Fly.PrintTableHeaders)
}

type agentExperimentVariantReference struct {
	Label        string
	WorkflowName string
	Version      int
	FunctionID   string
	// Node is set when the target was spelled label=node:name@version. A node
	// variant selects no function (it is a single leaf) and instead varies
	// the node's own parameters between variants.
	Node bool
}

type ExperimentsAddVariantCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
		Variant    string `positional-arg-name:"VARIANT" required:"true" description:"label=workflow@version, label=workflow@version#function-id, or label=node:name@version"`
	} `positional-args:"yes"`
	Control  bool     `long:"control" description:"Make this variant the explicit control (clears the prior control)"`
	Revision int64    `long:"revision" description:"Expected positive draft revision (default: revision returned by GET)"`
	Param    []string `long:"param" description:"Set a node parameter as NAME=VALUE (repeatable; only valid for a node variant)"`
	Json     bool     `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsAddVariantCommand) Execute([]string) error {
	id, reference, nodeParameters, err := command.prepare()
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id, reference, nodeParameters)
}

func (command *ExperimentsAddVariantCommand) execute(target rc.Target, output io.Writer) error {
	id, reference, nodeParameters, err := command.prepare()
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id, reference, nodeParameters)
}

func (command *ExperimentsAddVariantCommand) prepare() (experiment.ID, agentExperimentVariantReference, map[string]string, error) {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return 0, agentExperimentVariantReference{}, nil, err
	}
	reference, err := parseAgentExperimentVariantReference(command.Args.Variant)
	if err != nil {
		return 0, agentExperimentVariantReference{}, nil, err
	}
	if command.Revision < 0 {
		return 0, agentExperimentVariantReference{}, nil, fmt.Errorf("experiment revision must be positive")
	}
	if !reference.Node && len(command.Param) != 0 {
		return 0, agentExperimentVariantReference{}, nil, fmt.Errorf("variant --param is only valid for a node variant (label=node:name@version); %q is not a node target", command.Args.Variant)
	}
	nodeParameters, err := parseAgentExperimentNodeParameters(command.Param)
	if err != nil {
		return 0, agentExperimentVariantReference{}, nil, err
	}
	return id, reference, nodeParameters, nil
}

func (command *ExperimentsAddVariantCommand) executePrepared(
	target rc.Target,
	output io.Writer,
	id experiment.ID,
	reference agentExperimentVariantReference,
	nodeParameters map[string]string,
) error {
	stored, err := getAgentExperiment(target, id)
	if err != nil {
		return err
	}
	variant, err := resolveAgentExperimentVariant(target, reference, command.Control, nodeParameters)
	if err != nil {
		return err
	}
	if command.Control {
		for index := range stored.Definition.Variants {
			stored.Definition.Variants[index].Control = false
		}
	}
	stored.Definition.Variants = append(stored.Definition.Variants, variant)
	revision := stored.Revision
	if command.Revision > 0 {
		revision = command.Revision
	}
	stored, err = updateAgentExperiment(target, id, revision, stored.Definition)
	if err != nil {
		return err
	}
	return renderAgentExperiment(output, stored, command.Json, Fly.PrintTableHeaders)
}

type ExperimentsAddFixtureCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
		Label      string `positional-arg-name:"LABEL" required:"true" description:"Fixture label"`
	} `positional-args:"yes"`
	Role       string   `long:"role" default:"normal" choice:"normal" choice:"negative-control" description:"Fixture role"`
	Inputs     []string `long:"input" required:"true" description:"Input binding port=snapshot-id (repeatable)"`
	Assertions []string `long:"assert" description:"Negative-control assertion metric=comparator:value[:value] (repeatable)"`
	Revision   int64    `long:"revision" description:"Expected positive draft revision (default: revision returned by GET)"`
	Json       bool     `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsAddFixtureCommand) Execute([]string) error {
	id, fixture, err := command.prepare()
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id, fixture)
}

func (command *ExperimentsAddFixtureCommand) execute(target rc.Target, output io.Writer) error {
	id, fixture, err := command.prepare()
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id, fixture)
}

func (command *ExperimentsAddFixtureCommand) prepare() (experiment.ID, experiment.Fixture, error) {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return 0, experiment.Fixture{}, err
	}
	if command.Revision < 0 {
		return 0, experiment.Fixture{}, fmt.Errorf("experiment revision must be positive")
	}
	role, err := parseAgentExperimentFixtureRole(command.Role)
	if err != nil {
		return 0, experiment.Fixture{}, err
	}
	inputs, err := parseAgentExperimentInputs(command.Inputs)
	if err != nil {
		return 0, experiment.Fixture{}, err
	}
	assertions, err := parseAgentExperimentAssertions(command.Assertions)
	if err != nil {
		return 0, experiment.Fixture{}, err
	}
	return id, experiment.Fixture{
		Label: command.Args.Label, Role: role, Inputs: inputs, Assertions: assertions,
	}, nil
}

func (command *ExperimentsAddFixtureCommand) executePrepared(
	target rc.Target,
	output io.Writer,
	id experiment.ID,
	fixture experiment.Fixture,
) error {
	stored, err := getAgentExperiment(target, id)
	if err != nil {
		return err
	}
	stored.Definition.Fixtures = append(stored.Definition.Fixtures, fixture)
	revision := stored.Revision
	if command.Revision > 0 {
		revision = command.Revision
	}
	stored, err = updateAgentExperiment(target, id, revision, stored.Definition)
	if err != nil {
		return err
	}
	return renderAgentExperiment(output, stored, command.Json, Fly.PrintTableHeaders)
}

type ExperimentsValidateCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
	} `positional-args:"yes"`
	Revision int64 `long:"revision" required:"true" description:"Expected positive draft revision"`
	Json     bool  `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsValidateCommand) Execute([]string) error {
	id, err := prepareAgentExperimentRevision(command.Args.Experiment, command.Revision)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id)
}

func (command *ExperimentsValidateCommand) execute(target rc.Target, output io.Writer) error {
	id, err := prepareAgentExperimentRevision(command.Args.Experiment, command.Revision)
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id)
}

func (command *ExperimentsValidateCommand) executePrepared(target rc.Target, output io.Writer, id experiment.ID) error {
	var result agentExperimentValidationResult
	path := agentExperimentPath(id) + "/validate"
	if err := agentExperimentJSONRequest(target, http.MethodPost, path, map[string]int64{"revision": command.Revision}, &result); err != nil {
		return err
	}
	if command.Json {
		return writeAgentExperimentJSON(output, result)
	}
	table := ui.Table{
		Headers: agentExperimentTableHeaders("valid", "revision", "expected cells"),
		Data: ui.Data{ui.TableRow{
			{Contents: strconv.FormatBool(result.Valid)},
			{Contents: strconv.FormatInt(result.Revision, 10)},
			{Contents: strconv.Itoa(result.ExpectedCells)},
		}},
	}
	return table.Render(output, Fly.PrintTableHeaders)
}

type ExperimentsStartCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
	} `positional-args:"yes"`
	Revision int64 `long:"revision" required:"true" description:"Expected positive draft revision"`
	Json     bool  `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsStartCommand) Execute([]string) error {
	id, err := prepareAgentExperimentRevision(command.Args.Experiment, command.Revision)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return executeAgentExperimentTransition(target, os.Stdout, id, command.Revision, "start", command.Json)
}

func (command *ExperimentsStartCommand) execute(target rc.Target, output io.Writer) error {
	id, err := prepareAgentExperimentRevision(command.Args.Experiment, command.Revision)
	if err != nil {
		return err
	}
	return executeAgentExperimentTransition(target, output, id, command.Revision, "start", command.Json)
}

type ExperimentsCancelCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
	} `positional-args:"yes"`
	Revision int64 `long:"revision" required:"true" description:"Expected positive experiment revision"`
	Json     bool  `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsCancelCommand) Execute([]string) error {
	id, err := prepareAgentExperimentRevision(command.Args.Experiment, command.Revision)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return executeAgentExperimentTransition(target, os.Stdout, id, command.Revision, "cancel", command.Json)
}

func (command *ExperimentsCancelCommand) execute(target rc.Target, output io.Writer) error {
	id, err := prepareAgentExperimentRevision(command.Args.Experiment, command.Revision)
	if err != nil {
		return err
	}
	return executeAgentExperimentTransition(target, output, id, command.Revision, "cancel", command.Json)
}

type ExperimentsListCommand struct {
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsListCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.execute(target, os.Stdout)
}

func (command *ExperimentsListCommand) execute(target rc.Target, output io.Writer) error {
	var stored []agentExperimentRecord
	seenExperiments := make(map[experiment.ID]struct{})
	err := followAgentHistoryPages("experiment", opaqueAgentHistoryCursor, func(cursor string) (string, bool, error) {
		query := url.Values{"limit": {strconv.Itoa(experiment.MaxListedExperiments)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		response, err := agentAPIRequest(target, http.MethodGet, agentExperimentsPath+"?"+query.Encode(), nil)
		if err != nil {
			return "", false, err
		}
		nextCursor := response.Header.Get("X-Next-Cursor")
		var page []agentExperimentRecord
		if err := decodeOrError(response, &page); err != nil {
			return "", false, err
		}
		if len(page) > experiment.MaxListedExperiments {
			return "", false, fmt.Errorf("server returned too many experiments in one page")
		}
		for _, value := range page {
			if _, duplicate := seenExperiments[value.ID]; duplicate {
				return "", false, fmt.Errorf("server returned duplicate experiment %s across pages", value.ID.String())
			}
			seenExperiments[value.ID] = struct{}{}
			stored = append(stored, value)
		}
		return nextCursor, false, nil
	})
	if err != nil {
		return err
	}
	return renderAgentExperiments(output, stored, command.Json, Fly.PrintTableHeaders)
}

type ExperimentsShowCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsShowCommand) Execute([]string) error {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id)
}

func (command *ExperimentsShowCommand) execute(target rc.Target, output io.Writer) error {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id)
}

func (command *ExperimentsShowCommand) executePrepared(target rc.Target, output io.Writer, id experiment.ID) error {
	stored, err := getAgentExperiment(target, id)
	if err != nil {
		return err
	}
	return renderAgentExperiment(output, stored, command.Json, Fly.PrintTableHeaders)
}

type ExperimentsCellsCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
		Cell       string `positional-arg-name:"CELL" description:"Optional quoted-decimal cell ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsCellsCommand) Execute([]string) error {
	id, cellID, err := command.prepare()
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id, cellID)
}

func (command *ExperimentsCellsCommand) execute(target rc.Target, output io.Writer) error {
	id, cellID, err := command.prepare()
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id, cellID)
}

func (command *ExperimentsCellsCommand) prepare() (experiment.ID, *experiment.CellID, error) {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return 0, nil, err
	}
	if command.Args.Cell == "" {
		return id, nil, nil
	}
	cellID, err := parseAgentExperimentCellID(command.Args.Cell)
	if err != nil {
		return 0, nil, err
	}
	return id, &cellID, nil
}

func (command *ExperimentsCellsCommand) executePrepared(
	target rc.Target,
	output io.Writer,
	id experiment.ID,
	cellID *experiment.CellID,
) error {
	path := agentExperimentPath(id) + "/cells"
	if cellID != nil {
		path += "/" + cellID.String()
	}
	response, err := agentAPIRequest(target, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if cellID != nil {
		var cell agentExperimentCell
		if err := decodeOrError(response, &cell); err != nil {
			return err
		}
		return renderAgentExperimentCell(output, cell, command.Json, Fly.PrintTableHeaders)
	}
	var cells []agentExperimentCell
	if err := decodeOrError(response, &cells); err != nil {
		return err
	}
	return renderAgentExperimentCells(output, cells, command.Json, Fly.PrintTableHeaders)
}

type ExperimentsScorecardCommand struct {
	Args struct {
		Experiment string `positional-arg-name:"EXPERIMENT" required:"true" description:"Quoted-decimal experiment ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *ExperimentsScorecardCommand) Execute([]string) error {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return command.executePrepared(target, os.Stdout, id)
}

func (command *ExperimentsScorecardCommand) execute(target rc.Target, output io.Writer) error {
	id, err := parseAgentExperimentID(command.Args.Experiment)
	if err != nil {
		return err
	}
	return command.executePrepared(target, output, id)
}

func (command *ExperimentsScorecardCommand) executePrepared(target rc.Target, output io.Writer, id experiment.ID) error {
	response, err := agentAPIRequest(target, http.MethodGet, agentExperimentPath(id)+"/scorecard", nil)
	if err != nil {
		return err
	}
	var scorecard experiment.Scorecard
	if err := decodeOrError(response, &scorecard); err != nil {
		return err
	}
	return renderAgentExperimentScorecard(output, scorecard, command.Json, Fly.PrintTableHeaders)
}

func parseAgentExperimentID(raw string) (experiment.ID, error) {
	value, err := parseAgentExperimentPositiveID(raw, "experiment ID")
	return experiment.ID(value), err
}

func parseAgentExperimentCellID(raw string) (experiment.CellID, error) {
	value, err := parseAgentExperimentPositiveID(raw, "experiment cell ID")
	return experiment.CellID(value), err
}

func parseAgentExperimentPositiveID(raw, name string) (int64, error) {
	if raw == "" || raw[0] == '+' || (len(raw) > 1 && raw[0] == '0') {
		return 0, fmt.Errorf("%s must be a canonical positive decimal", name)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a canonical positive decimal", name)
	}
	return value, nil
}

func parseAgentExperimentVariantReference(raw string) (agentExperimentVariantReference, error) {
	if strings.Count(raw, "=") != 1 {
		return agentExperimentVariantReference{}, fmt.Errorf("variant must be label=workflow@version, label=workflow@version#function-id, or label=node:name@version")
	}
	label, target, _ := strings.Cut(raw, "=")
	if strings.TrimSpace(label) == "" || label != strings.TrimSpace(label) {
		return agentExperimentVariantReference{}, fmt.Errorf("variant label is required")
	}
	isNode := false
	if rest, found := strings.CutPrefix(target, "node:"); found {
		isNode = true
		target = rest
		if strings.Contains(target, "#") {
			return agentExperimentVariantReference{}, fmt.Errorf("variant node target must not select a function: a node is one leaf, so label=node:name@version#function-id is meaningless")
		}
	}
	if strings.Count(target, "#") > 1 {
		return agentExperimentVariantReference{}, fmt.Errorf("variant function target must contain one function ID")
	}
	workflowVersion, functionID, hasFunction := strings.Cut(target, "#")
	if hasFunction && strings.TrimSpace(functionID) == "" {
		return agentExperimentVariantReference{}, fmt.Errorf("variant function ID is required after #")
	}
	if strings.Count(workflowVersion, "@") != 1 {
		return agentExperimentVariantReference{}, fmt.Errorf("variant target must pin workflow@version")
	}
	workflowName, rawVersion, _ := strings.Cut(workflowVersion, "@")
	if strings.TrimSpace(workflowName) == "" || workflowName != strings.TrimSpace(workflowName) {
		return agentExperimentVariantReference{}, fmt.Errorf("variant workflow name is required")
	}
	if rawVersion == "" || rawVersion[0] == '+' || (len(rawVersion) > 1 && rawVersion[0] == '0') {
		return agentExperimentVariantReference{}, fmt.Errorf("variant workflow version must be a canonical positive integer")
	}
	version64, err := strconv.ParseInt(rawVersion, 10, 32)
	if err != nil || version64 <= 0 {
		return agentExperimentVariantReference{}, fmt.Errorf("variant workflow version must be a canonical positive integer")
	}
	return agentExperimentVariantReference{
		Label: label, WorkflowName: workflowName, Version: int(version64), FunctionID: functionID, Node: isNode,
	}, nil
}

// parseAgentExperimentNodeParameters parses repeatable --param NAME=VALUE
// flags for a node variant. Unlike the looser `agent nodes run --param`
// parser, this rejects an empty value outright: a node variant is meant to
// pin an exact, comparable configuration, so a blank value almost certainly
// means the caller forgot the "=value" half rather than intending an empty
// string.
func parseAgentExperimentNodeParameters(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	params := make(map[string]string, len(values))
	for _, value := range values {
		name, parameter, found := strings.Cut(value, "=")
		if !found || strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("variant --param %q must be NAME=VALUE with a non-empty name", value)
		}
		if parameter == "" {
			return nil, fmt.Errorf("variant --param %q must have a non-empty value", value)
		}
		if _, duplicate := params[name]; duplicate {
			return nil, fmt.Errorf("variant --param %q was specified more than once", name)
		}
		params[name] = parameter
	}
	return params, nil
}

func parseAgentExperimentFixtureRole(raw string) (experiment.FixtureRole, error) {
	switch raw {
	case "", "normal":
		return experiment.FixtureNormal, nil
	case "negative-control":
		return experiment.FixtureNegativeControl, nil
	default:
		return "", fmt.Errorf("fixture role must be normal or negative-control")
	}
}

func parseAgentExperimentInputs(values []string) (map[string]snapshot.SnapshotID, error) {
	inputs := make(map[string]snapshot.SnapshotID, len(values))
	for _, value := range values {
		if strings.Count(value, "=") != 1 {
			return nil, fmt.Errorf("experiment input %q must be port=snapshot-id", value)
		}
		port, rawID, _ := strings.Cut(value, "=")
		if strings.TrimSpace(port) == "" || port != strings.TrimSpace(port) {
			return nil, fmt.Errorf("experiment input %q has an invalid port", value)
		}
		if _, found := inputs[port]; found {
			return nil, fmt.Errorf("experiment input port %q was specified more than once", port)
		}
		id, err := snapshot.ParseSnapshotID(rawID)
		if err != nil {
			return nil, fmt.Errorf("experiment input %q: %w", port, err)
		}
		inputs[port] = id
	}
	return inputs, nil
}

func parseAgentExperimentAssertions(values []string) ([]experiment.Assertion, error) {
	assertions := make([]experiment.Assertion, 0, len(values))
	metrics := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.Count(value, "=") != 1 {
			return nil, fmt.Errorf("experiment assertion %q must be metric=comparator:value[:value]", value)
		}
		metric, expression, _ := strings.Cut(value, "=")
		if strings.TrimSpace(metric) == "" || metric != strings.TrimSpace(metric) {
			return nil, fmt.Errorf("experiment assertion metric is required")
		}
		if _, found := metrics[metric]; found {
			return nil, fmt.Errorf("experiment assertion metric %q was specified more than once", metric)
		}
		parts := strings.Split(expression, ":")
		if len(parts) < 2 {
			return nil, fmt.Errorf("experiment assertion %q has no threshold", metric)
		}
		assertion := experiment.Assertion{Metric: metric, Comparator: experiment.Comparator(parts[0])}
		for _, rawThreshold := range parts[1:] {
			threshold, err := strconv.ParseFloat(rawThreshold, 64)
			if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
				return nil, fmt.Errorf("experiment assertion %q threshold %q must be finite", metric, rawThreshold)
			}
			assertion.Thresholds = append(assertion.Thresholds, threshold)
		}
		if err := assertion.Validate(); err != nil {
			return nil, err
		}
		metrics[metric] = struct{}{}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func readAgentExperimentDefinition(path string) (experiment.Definition, error) {
	var source io.Reader
	var file *os.File
	if path == "-" {
		source = os.Stdin
	} else {
		opened, err := os.Open(path)
		if err != nil {
			return experiment.Definition{}, err
		}
		file = opened
		defer file.Close()
		source = file
	}
	payload, err := io.ReadAll(io.LimitReader(source, maximumExperimentDefinitionBytes+1))
	if err != nil {
		return experiment.Definition{}, err
	}
	if len(payload) > maximumExperimentDefinitionBytes {
		return experiment.Definition{}, fmt.Errorf("experiment definition exceeds %d bytes", maximumExperimentDefinitionBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var definition experiment.Definition
	if err := decoder.Decode(&definition); err != nil {
		return experiment.Definition{}, fmt.Errorf("decode experiment definition: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return experiment.Definition{}, fmt.Errorf("experiment definition must contain one JSON value")
	}
	return definition, nil
}

func resolveAgentExperimentVariant(
	target rc.Target,
	reference agentExperimentVariantReference,
	control bool,
	nodeParameters map[string]string,
) (experiment.Variant, error) {
	if reference.Node {
		return resolveAgentExperimentNodeVariant(target, reference, control, nodeParameters)
	}
	path := "/api/v1/agent/workflows/" + url.PathEscape(reference.WorkflowName) + "/versions/" + strconv.Itoa(reference.Version)
	response, err := agentAPIRequest(target, http.MethodGet, path, nil)
	if err != nil {
		return experiment.Variant{}, err
	}
	var definition workflow.Definition
	if err := decodeOrError(response, &definition); err != nil {
		return experiment.Variant{}, err
	}
	var signature workflow.PublicSignature
	variantTarget := experiment.Target{
		Kind: experiment.TargetWorkflow, WorkflowName: definition.Name,
		DefinitionID: int64(definition.ID), Version: definition.Version,
	}
	if reference.FunctionID == "" {
		signature, err = definition.Compiled.PublicSignature()
	} else {
		extracted, extractErr := workflow.ExtractFunctionTarget(definition, reference.FunctionID)
		if extractErr != nil {
			return experiment.Variant{}, extractErr
		}
		signature = extracted.Signature
		variantTarget.Kind = experiment.TargetFunction
		variantTarget.FunctionID = reference.FunctionID
	}
	if err != nil {
		return experiment.Variant{}, err
	}
	hash, err := experiment.HashSignature(signature)
	if err != nil {
		return experiment.Variant{}, err
	}
	return experiment.Variant{
		Label: reference.Label, Control: control, Target: variantTarget, SignatureHash: hash,
	}, nil
}

// resolveAgentExperimentNodeVariant fetches the exact reusable node version
// and mirrors the durable binder's executableNodeDefinition: instantiate the
// node's one leaf with the caller's parameters (defaults apply to the rest)
// to derive the same public signature the server will bind against. This is
// required, not cosmetic — Definition.Validate rejects a variant whose
// signature_hash does not match the experiment's frozen signature.
func resolveAgentExperimentNodeVariant(
	target rc.Target,
	reference agentExperimentVariantReference,
	control bool,
	nodeParameters map[string]string,
) (experiment.Variant, error) {
	path := "/api/v1/agent/nodes/" + url.PathEscape(reference.WorkflowName) + "/versions/" + strconv.Itoa(reference.Version)
	response, err := agentAPIRequest(target, http.MethodGet, path, nil)
	if err != nil {
		return experiment.Variant{}, err
	}
	var node workflow.NodeDefinition
	if err := decodeOrError(response, &node); err != nil {
		return experiment.Variant{}, err
	}
	function, err := node.Compiled.Instantiate(nodeParameters)
	if err != nil {
		return experiment.Variant{}, err
	}
	compiled := workflow.CompiledDefinition{SchemaVersion: 3, Name: node.Name, Function: function}
	signature, err := compiled.PublicSignature()
	if err != nil {
		return experiment.Variant{}, err
	}
	hash, err := experiment.HashSignature(signature)
	if err != nil {
		return experiment.Variant{}, err
	}
	return experiment.Variant{
		Label: reference.Label, Control: control,
		Target: experiment.Target{
			Kind: experiment.TargetNode, WorkflowName: node.Name,
			DefinitionID: int64(node.ID), Version: node.Version,
			NodeParameters: nodeParameters,
		},
		SignatureHash: hash,
	}, nil
}

func prepareAgentExperimentRevision(rawID string, revision int64) (experiment.ID, error) {
	id, err := parseAgentExperimentID(rawID)
	if err != nil {
		return 0, err
	}
	if revision <= 0 {
		return 0, fmt.Errorf("experiment revision must be positive")
	}
	return id, nil
}

func executeAgentExperimentTransition(
	target rc.Target,
	output io.Writer,
	id experiment.ID,
	revision int64,
	action string,
	jsonOutput bool,
) error {
	var stored agentExperimentRecord
	if err := agentExperimentJSONRequest(
		target,
		http.MethodPost,
		agentExperimentPath(id)+"/"+action,
		map[string]int64{"revision": revision},
		&stored,
	); err != nil {
		return err
	}
	return renderAgentExperiment(output, stored, jsonOutput, Fly.PrintTableHeaders)
}

func getAgentExperiment(target rc.Target, id experiment.ID) (agentExperimentRecord, error) {
	response, err := agentAPIRequest(target, http.MethodGet, agentExperimentPath(id), nil)
	if err != nil {
		return agentExperimentRecord{}, err
	}
	var stored agentExperimentRecord
	if err := decodeOrError(response, &stored); err != nil {
		return agentExperimentRecord{}, err
	}
	return stored, nil
}

func updateAgentExperiment(
	target rc.Target,
	id experiment.ID,
	revision int64,
	definition experiment.Definition,
) (agentExperimentRecord, error) {
	request := struct {
		Revision   int64                 `json:"revision"`
		Definition experiment.Definition `json:"definition"`
	}{Revision: revision, Definition: definition}
	var stored agentExperimentRecord
	if err := agentExperimentJSONRequest(target, http.MethodPut, agentExperimentPath(id), request, &stored); err != nil {
		return agentExperimentRecord{}, err
	}
	return stored, nil
}

func agentExperimentJSONRequest(target rc.Target, method, path string, value, destination any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(target, method, path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return decodeOrError(response, destination)
}

func agentExperimentPath(id experiment.ID) string {
	return agentExperimentsPath + "/" + id.String()
}

func renderAgentExperiment(
	output io.Writer,
	value agentExperimentRecord,
	jsonOutput bool,
	printHeaders bool,
) error {
	if jsonOutput {
		return writeAgentExperimentJSON(output, value)
	}
	return renderAgentExperiments(output, []agentExperimentRecord{value}, false, printHeaders)
}

func renderAgentExperiments(
	output io.Writer,
	values []agentExperimentRecord,
	jsonOutput bool,
	printHeaders bool,
) error {
	if jsonOutput {
		return writeAgentExperimentJSON(output, values)
	}
	table := ui.Table{Headers: agentExperimentTableHeaders(
		"id", "name", "state", "revision", "variants", "fixtures", "repetitions", "expected cells", "updated",
	)}
	for _, stored := range values {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: stored.ID.String()},
			{Contents: stored.Definition.Name},
			{Contents: string(stored.Definition.State)},
			{Contents: strconv.FormatInt(stored.Revision, 10)},
			{Contents: strconv.Itoa(len(stored.Definition.Variants))},
			{Contents: strconv.Itoa(len(stored.Definition.Fixtures))},
			{Contents: strconv.Itoa(stored.Definition.Repetitions)},
			{Contents: strconv.Itoa(stored.Definition.ExpectedCells())},
			{Contents: formatAgentExperimentTime(stored.UpdatedAt)},
		})
	}
	sort.Sort(table.Data)
	return table.Render(output, printHeaders)
}

func renderAgentExperimentCell(
	output io.Writer,
	value agentExperimentCell,
	jsonOutput bool,
	printHeaders bool,
) error {
	if jsonOutput {
		return writeAgentExperimentJSON(output, value)
	}
	return renderAgentExperimentCells(output, []agentExperimentCell{value}, false, printHeaders)
}

func renderAgentExperimentCells(
	output io.Writer,
	values []agentExperimentCell,
	jsonOutput bool,
	printHeaders bool,
) error {
	if jsonOutput {
		return writeAgentExperimentJSON(output, values)
	}
	table := ui.Table{Headers: agentExperimentTableHeaders(
		"id", "fixture", "role", "variant", "rep", "status", "candidate run", "evaluator run", "measurement", "failure",
	)}
	for _, cell := range values {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: cell.ID.String()},
			{Contents: cell.FixtureLabel},
			{Contents: string(cell.FixtureRole)},
			{Contents: cell.VariantLabel},
			{Contents: strconv.Itoa(cell.Repetition)},
			{Contents: string(cell.Status)},
			{Contents: agentExperimentWorkflowRunID(cell.CandidateWorkflowRunID)},
			{Contents: agentExperimentWorkflowRunID(cell.EvaluatorWorkflowRunID)},
			{Contents: agentExperimentSnapshotID(cell.MeasurementSnapshotID)},
			{Contents: cell.CandidateFailureCategory},
		})
	}
	sort.Sort(table.Data)
	return table.Render(output, printHeaders)
}

func renderAgentExperimentScorecard(
	output io.Writer,
	scorecard experiment.Scorecard,
	jsonOutput bool,
	printHeaders bool,
) error {
	if jsonOutput {
		return writeAgentExperimentJSON(output, scorecard)
	}
	table := ui.Table{Headers: agentExperimentTableHeaders(
		"variant", "metric", "n", "mean", "median", "stddev", "range", "valid", "paired", "mean delta", "95% interval", "recommendation", "winner", "failed conditions",
	)}
	variants := make([]string, 0, len(scorecard.Variants))
	for variant := range scorecard.Variants {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	for _, variant := range variants {
		score := scorecard.Variants[variant]
		comparisons := scorecard.Comparisons[variant]
		distributions := agentExperimentScoreDistributions(score)
		metricSet := make(map[string]struct{}, len(distributions)+len(comparisons))
		metrics := make([]string, 0, len(distributions)+len(comparisons))
		for metric := range distributions {
			metricSet[metric] = struct{}{}
			metrics = append(metrics, metric)
		}
		for metric := range comparisons {
			if _, found := metricSet[metric]; !found {
				metrics = append(metrics, metric)
			}
		}
		sort.Strings(metrics)
		if len(metrics) == 0 {
			table.Data = append(table.Data, ui.TableRow{
				{Contents: variant}, {Contents: "-"}, {Contents: "0"}, {Contents: "-"}, {Contents: "-"}, {Contents: "-"}, {Contents: "-"},
				{Contents: agentExperimentValidCells(score)}, {Contents: "-"}, {Contents: "-"}, {Contents: "-"},
				{Contents: "no measurements"}, {Contents: controlMarker(variant, scorecard.Control)}, {Contents: ""},
			})
			continue
		}
		for _, metric := range metrics {
			comparison := comparisons[metric]
			distribution := distributions[metric]
			paired := ""
			meanDelta := ""
			interval := ""
			recommendation := ""
			if comparison.Metric != "" {
				paired = strconv.Itoa(comparison.PairedCount)
				meanDelta = formatAgentExperimentFloat(comparison.MeanDelta)
				interval = "[" + formatAgentExperimentFloat(comparison.ConfidenceLow) + ", " + formatAgentExperimentFloat(comparison.ConfidenceHigh) + "]"
				recommendation = string(comparison.Recommendation)
			}
			table.Data = append(table.Data, ui.TableRow{
				{Contents: variant},
				{Contents: metric},
				{Contents: strconv.Itoa(distribution.Count)},
				{Contents: formatAgentExperimentFloat(distribution.Mean)},
				{Contents: formatAgentExperimentFloat(distribution.Median)},
				{Contents: formatAgentExperimentFloat(distribution.StdDev)},
				{Contents: "[" + formatAgentExperimentFloat(distribution.Min) + ", " + formatAgentExperimentFloat(distribution.Max) + "]"},
				{Contents: agentExperimentValidCells(score)},
				{Contents: paired},
				{Contents: meanDelta},
				{Contents: interval},
				{Contents: recommendation},
				{Contents: comparison.Winner},
				{Contents: strings.Join(comparison.FailedConditions, "; ")},
			})
		}
	}
	return table.Render(output, printHeaders)
}

func agentExperimentScoreDistributions(score experiment.VariantScore) map[string]experiment.Distribution {
	distributions := make(map[string]experiment.Distribution, len(score.Metrics)+4)
	for name, distribution := range score.Metrics {
		distributions[name] = distribution
	}
	for name, distribution := range map[string]experiment.Distribution{
		"cost_usd":            score.CostUSD,
		"latency_seconds":     score.LatencySeconds,
		"tokens":              score.Tokens,
		"human_interventions": score.HumanInterventions,
	} {
		if distribution.Count > 0 {
			distributions[name] = distribution
		}
	}
	return distributions
}

func agentExperimentValidCells(score experiment.VariantScore) string {
	return fmt.Sprintf(
		"%d/%d (%.3g), %d budget-skipped",
		score.ValidCells, score.ExpectedCells, score.ValidCoverage, score.BudgetSkipped,
	)
}

func writeAgentExperimentJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func agentExperimentTableHeaders(values ...string) ui.TableRow {
	row := make(ui.TableRow, len(values))
	for index, value := range values {
		row[index] = ui.TableCell{Contents: value, Color: color.New(color.Bold)}
	}
	return row
}

func agentExperimentWorkflowRunID(id *snapshot.WorkflowRunID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func agentExperimentSnapshotID(id *snapshot.SnapshotID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func formatAgentExperimentTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatAgentExperimentFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func controlMarker(variant, control string) string {
	if variant == control {
		return "control"
	}
	return ""
}
