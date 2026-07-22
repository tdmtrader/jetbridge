package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/goccy/go-yaml"
)

// Parse parses and eagerly validates a workflow definition
// (phaseconfig-style: any structural problem is an import-time error).
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse workflow definition: %w", err)
	}
	// Hooks are deferred (design 2026-07-17): reject the key rather than
	// silently ignoring it — an author must never believe hook behavior
	// is active when nothing runs it.
	var probe struct {
		Hooks any              `yaml:"hooks"`
		Steps []map[string]any `yaml:"steps"`
	}
	if err := yaml.Unmarshal(raw, &probe); err == nil {
		if probe.Hooks != nil {
			return nil, fmt.Errorf("workflow: hooks are not supported (deferred, design 2026-07-17); remove the hooks key")
		}
		for i, s := range probe.Steps {
			if _, ok := s["hooks"]; ok {
				return nil, fmt.Errorf("workflow: step %d: hooks are not supported (deferred, design 2026-07-17); remove the hooks key", i)
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ParseCompiled dispatches a workflow source document by schema_version and
// returns an explicit tagged value. Parse remains the legacy schema-1/2 API so
// existing ticket-oriented callers cannot accidentally consume a version-3
// function as a zero-valued Config.
func ParseCompiled(raw []byte) (*CompiledDefinition, error) {
	version, err := parseSchemaVersion(raw)
	if err != nil {
		return nil, err
	}

	switch version {
	case 1, 2:
		legacy, err := Parse(raw)
		if err != nil {
			return nil, err
		}
		definition := &CompiledDefinition{
			SchemaVersion: legacy.SchemaVersion,
			Name:          legacy.Name,
			Description:   legacy.Description,
			Legacy:        legacy,
		}
		if err := definition.Validate(); err != nil {
			return nil, err
		}
		return definition, nil
	case 3:
		return parseFunctionDefinition(raw)
	default:
		return nil, fmt.Errorf("workflow: schema_version must be 1, 2, or 3, got %d", version)
	}
}

func parseSchemaVersion(raw []byte) (int, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return 0, fmt.Errorf("parse workflow definition: empty document")
		}
		return 0, fmt.Errorf("parse workflow definition discriminator: %w", err)
	}
	value, found := document["schema_version"]
	if !found {
		return 0, fmt.Errorf("workflow: schema_version is required")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("workflow: schema_version must be an integer")
	}
	var version int
	if err := json.Unmarshal(encoded, &version); err != nil {
		return 0, fmt.Errorf("workflow: schema_version must be an integer")
	}
	return version, nil
}

type functionSource struct {
	SchemaVersion    int                   `json:"schema_version"`
	Name             string                `json:"name"`
	SignatureVersion int                   `json:"signature_version"`
	Description      string                `json:"description,omitempty"`
	Inputs           []snapshot.Port       `json:"inputs"`
	Outputs          []FunctionOutput      `json:"outputs"`
	Capabilities     map[string]Capability `json:"capabilities,omitempty"`
	Resources        any                   `json:"resources,omitempty"`
	ResourceTypes    any                   `json:"resource_types,omitempty"`
	Prototypes       any                   `json:"prototypes,omitempty"`
	VarSources       any                   `json:"var_sources,omitempty"`
	Plan             any                   `json:"plan"`
}

type syntheticFunctionConfig struct {
	VarSources    any                    `yaml:"var_sources,omitempty"`
	Resources     any                    `yaml:"resources,omitempty"`
	ResourceTypes any                    `yaml:"resource_types,omitempty"`
	Prototypes    any                    `yaml:"prototypes,omitempty"`
	Jobs          []syntheticFunctionJob `yaml:"jobs"`
}

type syntheticFunctionJob struct {
	Name string `yaml:"name"`
	Plan any    `yaml:"plan"`
}

func parseFunctionDefinition(raw []byte) (*CompiledDefinition, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse workflow function: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse workflow function: exactly one YAML or JSON document is required")
		}
		return nil, fmt.Errorf("parse workflow function trailing document: %w", err)
	}
	if err := validateFunctionSourceKeys(document); err != nil {
		return nil, err
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("parse workflow function document: %w", err)
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(documentJSON))
	jsonDecoder.DisallowUnknownFields()
	var source functionSource
	if err := jsonDecoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("parse workflow function: %w", err)
	}
	if err := jsonDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse workflow function: unexpected trailing JSON value")
		}
		return nil, fmt.Errorf("parse workflow function trailing JSON: %w", err)
	}
	if source.SchemaVersion != 3 {
		return nil, fmt.Errorf("workflow: function parser requires schema_version 3, got %d", source.SchemaVersion)
	}

	synthetic := syntheticFunctionConfig{
		VarSources:    source.VarSources,
		Resources:     source.Resources,
		ResourceTypes: source.ResourceTypes,
		Prototypes:    source.Prototypes,
		Jobs: []syntheticFunctionJob{{
			Name: "agent-function",
			Plan: source.Plan,
		}},
	}
	payload, err := yaml.Marshal(synthetic)
	if err != nil {
		return nil, fmt.Errorf("workflow: encode ordinary Concourse plan: %w", err)
	}
	var ordinary atc.Config
	if err := atc.UnmarshalConfig(payload, &ordinary); err != nil {
		return nil, fmt.Errorf("workflow: decode ordinary Concourse declarations and plan: %w", err)
	}
	if len(ordinary.Jobs) != 1 {
		return nil, fmt.Errorf("workflow: internal function plan must decode as exactly one job")
	}

	definition := &CompiledDefinition{
		SchemaVersion: source.SchemaVersion,
		Name:          source.Name,
		Description:   source.Description,
		Function: &FunctionConfig{
			SignatureVersion: source.SignatureVersion,
			Inputs:           source.Inputs,
			Outputs:          source.Outputs,
			Capabilities:     source.Capabilities,
			Resources:        ordinary.Resources,
			ResourceTypes:    ordinary.ResourceTypes,
			Prototypes:       ordinary.Prototypes,
			VarSources:       ordinary.VarSources,
			Plan:             ordinary.Jobs[0].PlanSequence,
		},
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return definition, nil
}

func validateFunctionSourceKeys(document any) error {
	root, ok := document.(map[string]any)
	if !ok {
		return nil // the typed JSON pass reports the shape error
	}
	if err := rejectObjectKeys(root, "workflow", []string{
		"schema_version", "name", "signature_version", "description", "inputs", "outputs",
		"capabilities", "resources", "resource_types", "prototypes", "var_sources", "plan",
	}); err != nil {
		return err
	}
	if inputs, ok := root["inputs"].([]any); ok {
		for index, input := range inputs {
			if object, ok := input.(map[string]any); ok {
				if err := rejectObjectKeys(object, fmt.Sprintf("workflow.inputs[%d]", index), []string{"name", "type", "optional", "description"}); err != nil {
					return err
				}
			}
		}
	}
	if outputs, ok := root["outputs"].([]any); ok {
		for index, output := range outputs {
			if object, ok := output.(map[string]any); ok {
				if err := rejectObjectKeys(object, fmt.Sprintf("workflow.outputs[%d]", index), []string{"name", "type", "optional", "description", "from"}); err != nil {
					return err
				}
			}
		}
	}
	if plan, found := root["plan"]; found {
		if err := validateFunctionPlanSource(plan, "workflow.plan"); err != nil {
			return err
		}
	}
	capabilities, ok := root["capabilities"].(map[string]any)
	if !ok {
		return nil
	}
	for name, value := range capabilities {
		capability, ok := value.(map[string]any)
		if !ok {
			continue
		}
		path := fmt.Sprintf("workflow.capabilities[%q]", name)
		if err := rejectObjectKeys(capability, path, []string{"contract", "sidecar"}); err != nil {
			return err
		}
		if sidecar, ok := capability["sidecar"].(map[string]any); ok {
			if err := validateSidecarSourceKeys(sidecar, path+".sidecar"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSidecarSourceKeys(sidecar map[string]any, path string) error {
	return validateInlineSidecarSource(sidecar, path)
}

func rejectObjectKeys(object map[string]any, path string, allowedFields []string) error {
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	unknown := make([]string, 0)
	for field := range object {
		if _, found := allowed[field]; !found {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("workflow: %s: unknown field %q", path, unknown[0])
}

// validateFunctionPlanSource closes the ATC-owned object shapes before the
// ordinary Concourse decoder runs. Step.UnmarshalJSON deliberately preserves
// compatibility by recording unknown step-envelope fields, and several
// nested configs use encoding/json, which otherwise drops unknown fields (and
// matches field names case-insensitively). Version 3 is strict, so it audits
// those raw objects while leaving provider-owned maps such as resource params,
// image sources, vars, and across values opaque.
func validateFunctionPlanSource(plan any, path string) error {
	steps, ok := plan.([]any)
	if !ok {
		return nil // the ordinary ATC pass reports the shape error
	}
	for index, step := range steps {
		if err := validateFunctionStepSource(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateFunctionStepSource(value any, path string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return nil // the ordinary ATC pass reports the shape error
	}

	if nested, found := step["try"]; found {
		if err := validateFunctionStepSource(nested, path+".try"); err != nil {
			return err
		}
	}
	if nested, found := step["do"]; found {
		if err := validateFunctionStepListSource(nested, path+".do"); err != nil {
			return err
		}
	}
	if nested, found := step["in_parallel"]; found {
		if err := validateInParallelSource(nested, path+".in_parallel"); err != nil {
			return err
		}
	}
	for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if nested, found := step[hook]; found {
			if err := validateFunctionStepSource(nested, path+"."+hook); err != nil {
				return err
			}
		}
	}

	if across, found := step["across"]; found {
		if err := validateObjectListSource(across, path+".across", []string{"var", "values", "max_in_flight"}); err != nil {
			return err
		}
	}

	if _, isTask := step["task"]; isTask {
		if config, found := step["config"]; found {
			if err := validateTaskConfigSource(config, path+".config"); err != nil {
				return err
			}
		}
	}

	for _, field := range []string{"container_limits", "container_requests"} {
		if limits, found := step[field]; found {
			if err := validateObjectSource(limits, path+"."+field, []string{"cpu", "memory", "ephemeral_storage"}); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"input_types", "output_types"} {
		if configs, found := step[field]; found {
			if err := validateSnapshotTypeMapSource(configs, path+"."+field, field == "output_types"); err != nil {
				return err
			}
		}
	}
	if sidecars, found := step["sidecars"]; found {
		if err := validateSidecarListSource(sidecars, path+".sidecars"); err != nil {
			return err
		}
	}
	if devMCP, found := step["dev_mcp"]; found {
		if err := validateInlineSidecarSource(devMCP, path+".dev_mcp"); err != nil {
			return err
		}
	}
	if policy, found := step["gate_policy"]; found {
		if err := validateGatePolicySource(policy, path+".gate_policy"); err != nil {
			return err
		}
	}
	if judge, found := step["judge"]; found {
		if err := validateJudgeSource(judge, path+".judge"); err != nil {
			return err
		}
	}

	return nil
}

func validateFunctionStepListSource(value any, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, step := range steps {
		if err := validateFunctionStepSource(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateInParallelSource(value any, path string) error {
	switch config := value.(type) {
	case []any:
		return validateFunctionStepListSource(config, path)
	case map[string]any:
		if err := rejectObjectKeys(config, path, []string{"steps", "limit", "fail_fast"}); err != nil {
			return err
		}
		if steps, found := config["steps"]; found {
			return validateFunctionStepListSource(steps, path+".steps")
		}
	}
	return nil
}

func validateTaskConfigSource(value any, path string) error {
	config, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(config, path, []string{
		"platform", "rootfs_uri", "image_resource", "container_limits", "container_requests",
		"params", "run", "inputs", "outputs", "caches", "scratch_paths",
	}); err != nil {
		return err
	}
	if image, found := config["image_resource"]; found {
		if err := validateObjectSource(image, path+".image_resource", []string{"name", "type", "source", "version", "params", "tags"}); err != nil {
			return err
		}
	}
	for _, field := range []string{"container_limits", "container_requests"} {
		if limits, found := config[field]; found {
			if err := validateObjectSource(limits, path+"."+field, []string{"cpu", "memory", "ephemeral_storage"}); err != nil {
				return err
			}
		}
	}
	if run, found := config["run"]; found {
		if err := validateObjectSource(run, path+".run", []string{"path", "args", "dir", "user"}); err != nil {
			return err
		}
	}
	for _, list := range []struct {
		field   string
		allowed []string
	}{
		{field: "inputs", allowed: []string{"name", "path", "optional"}},
		{field: "outputs", allowed: []string{"name", "path"}},
		{field: "caches", allowed: []string{"path"}},
		{field: "scratch_paths", allowed: []string{"path"}},
	} {
		if entries, found := config[list.field]; found {
			if err := validateObjectListSource(entries, path+"."+list.field, list.allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSnapshotTypeMapSource(value any, path string, output bool) error {
	configs, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if output {
		for name, value := range configs {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("workflow: %s[%q]: output type must be a type-reference string", path, name)
			}
		}
		return nil
	}

	for name, value := range configs {
		if err := validateObjectSource(value, fmt.Sprintf("%s[%q]", path, name), []string{"type", "optional"}); err != nil {
			return err
		}
	}
	return nil
}

func validateSidecarListSource(value any, path string) error {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, entry := range entries {
		if err := validateInlineSidecarSource(entry, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateInlineSidecarSource(value any, path string) error {
	sidecar, ok := value.(map[string]any)
	if !ok {
		return nil // string file references and typed-shape errors go to ATC
	}
	if err := rejectObjectKeys(sidecar, path, []string{
		"name", "image", "command", "args", "env", "ports", "resources", "workingDir", "image_artifact",
	}); err != nil {
		return err
	}
	if env, found := sidecar["env"]; found {
		if err := validateObjectListSource(env, path+".env", []string{"name", "value"}); err != nil {
			return err
		}
	}
	if ports, found := sidecar["ports"]; found {
		if err := validateObjectListSource(ports, path+".ports", []string{"containerPort", "protocol"}); err != nil {
			return err
		}
	}
	resources, ok := sidecar["resources"].(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(resources, path+".resources", []string{"requests", "limits"}); err != nil {
		return err
	}
	for _, field := range []string{"requests", "limits"} {
		if quantities, found := resources[field]; found {
			if err := validateObjectSource(quantities, path+".resources."+field, []string{"cpu", "memory"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateGatePolicySource(value any, path string) error {
	policy, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(policy, path, []string{"gates", "on_gate_failure"}); err != nil {
		return err
	}
	if gates, found := policy["gates"]; found {
		return validateObjectListSource(gates, path+".gates", []string{"gate", "scope", "focus", "timeout", "retries"})
	}
	return nil
}

func validateJudgeSource(value any, path string) error {
	judge, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(judge, path, []string{"rubric", "pass_threshold", "model", "budget_usd"}); err != nil {
		return err
	}
	if rubric, found := judge["rubric"]; found {
		return validateObjectListSource(rubric, path+".rubric", []string{"name", "weight", "guidance"})
	}
	return nil
}

func validateObjectSource(value any, path string, allowed []string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return rejectObjectKeys(object, path, allowed)
}

func validateObjectListSource(value any, path string, allowed []string) error {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		if err := rejectObjectKeys(object, entryPath, allowed); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownPlanFields(steps []atc.Step) error {
	for index := range steps {
		if err := rejectUnknownStepFields(steps[index], fmt.Sprintf("plan[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownStepFields(step atc.Step, path string) error {
	if len(step.UnknownFields) > 0 {
		fields := make([]string, 0, len(step.UnknownFields))
		for field := range step.UnknownFields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		return fmt.Errorf("workflow: %s: unknown step field %q", path, fields[0])
	}
	return rejectUnknownStepConfigFields(step.Config, path)
}

func rejectUnknownStepConfigFields(config atc.StepConfig, path string) error {
	switch step := config.(type) {
	case *atc.TryStep:
		return rejectUnknownStepFields(step.Step, path+".try")
	case *atc.DoStep:
		for index := range step.Steps {
			if err := rejectUnknownStepFields(step.Steps[index], fmt.Sprintf("%s.do[%d]", path, index)); err != nil {
				return err
			}
		}
	case *atc.InParallelStep:
		for index := range step.Config.Steps {
			if err := rejectUnknownStepFields(step.Config.Steps[index], fmt.Sprintf("%s.in_parallel[%d]", path, index)); err != nil {
				return err
			}
		}
	case *atc.AcrossStep:
		return rejectUnknownStepConfigFields(step.Step, path+".across")
	case *atc.RetryStep:
		return rejectUnknownStepConfigFields(step.Step, path+".attempts")
	case *atc.TimeoutStep:
		return rejectUnknownStepConfigFields(step.Step, path+".timeout")
	case *atc.OnSuccessStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_success")
	case *atc.OnFailureStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_failure")
	case *atc.OnAbortStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_abort")
	case *atc.OnErrorStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_error")
	case *atc.EnsureStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".ensure")
	}
	return nil
}

var validSidecarRoles = map[string]bool{"dev": true, "platform": true, "gateway": true, "custom": true}
var validGates = map[string]bool{"build": true, "test": true, "lint": true}
var validGateScopes = map[string]bool{"affected": true, "full": true, "affected_then_full": true}

// nilRenderContext mirrors the frozen §6.2 render context (.Ticket .Spec
// .Tasks .Params) in its dispatch-time ground state: .Spec nil and .Tasks
// empty — always true at render, which happens BEFORE any agent step can
// submit a spec (contracts §6.2 nil-safety, 2026-07-09). .Ticket and .Params
// are tolerant maps so envelope/params field access renders "<no value>"
// instead of erroring (the envelope is always present at render; only a
// nil-deref is the import-blocking bug). Validation-only mirror — dispatch
// owns the real context type.
var nilRenderContext = struct {
	Ticket map[string]any
	Spec   *struct{}
	Tasks  []map[string]any
	Params map[string]any
}{
	Ticket: map[string]any{},
	Spec:   nil,
	Tasks:  []map[string]any{},
	Params: map[string]any{},
}

// Validate checks the §6 grammar rules. Unknown YAML keys are ignored
// (forward compatibility); known keys are strictly checked.
func (c *Config) Validate() error {
	if c.SchemaVersion != 1 && c.SchemaVersion != 2 {
		return fmt.Errorf("workflow: schema_version must be 1 or 2, got %d", c.SchemaVersion)
	}
	if c.SchemaVersion == 1 {
		if field := c.SourceFormatField(); field != "" {
			return fmt.Errorf("workflow: %s requires schema_version: 2", field)
		}
	}
	if c.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	switch c.SpecDelivery {
	case "", "mcp", "files":
		// "" ⇒ mcp (the default); dispatch's renderer treats the empty
		// string identically to "mcp".
	default:
		return fmt.Errorf("workflow: spec_delivery must be mcp or files, got %q", c.SpecDelivery)
	}
	if c.Budget.TicketUSD < 0 {
		return fmt.Errorf("workflow: budget.ticket_usd must be >= 0")
	}
	if c.Budget.JudgeUSD < 0 {
		return fmt.Errorf("workflow: budget.judge_usd must be >= 0")
	}

	for name, sc := range c.Sidecars {
		if sc.Image == "" {
			return fmt.Errorf("workflow: sidecar %q: image is required", name)
		}
		// The tag must be pinned: the segment after the final '/' must
		// contain ':' (handles registry:port hosts correctly).
		last := sc.Image[strings.LastIndex(sc.Image, "/")+1:]
		if !strings.Contains(last, ":") {
			return fmt.Errorf("workflow: sidecar %q: image %q must carry a pinned ':<version>' tag", name, sc.Image)
		}
		if !validSidecarRoles[sc.Role] {
			return fmt.Errorf("workflow: sidecar %q: role must be one of dev|platform|gateway|custom, got %q", name, sc.Role)
		}
		if len(sc.Providers) > 0 && sc.Role != "gateway" {
			return fmt.Errorf("workflow: sidecar %q: providers is only valid for role gateway", name)
		}
	}

	for key, body := range c.Prompts {
		if err := validatePromptTemplate(key, body); err != nil {
			return err
		}
	}

	for key, path := range c.PromptFiles {
		if _, dup := c.Prompts[key]; dup {
			return fmt.Errorf("workflow: prompt %q is defined both inline (prompts) and as a file (prompt_files)", key)
		}
		if path == "" {
			return fmt.Errorf("workflow: prompt_files %q: path is required", key)
		}
	}
	if c.SystemPrompt != "" && c.SystemPromptFile != "" {
		return fmt.Errorf("workflow: system_prompt and system_prompt_file are mutually exclusive")
	}
	if err := validateSkillList("skills", c.Skills); err != nil {
		return err
	}
	for i, p := range c.Context {
		if p == "" {
			return fmt.Errorf("workflow: context[%d]: path is required", i)
		}
	}

	if len(c.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}
	seen := map[string]bool{}
	// "repo", "ticket", and "skills" are renderer-provided reserved
	// artifacts: the ticket's git checkout, the spec.md/plan.md
	// files-delivery input (dispatch manual-dispatch slice addendum,
	// 2026-07-17), and the materialized skill trees (design 2026-07-17
	// §4). Steps may consume them without an earlier producer.
	produced := map[string]bool{"repo": true, "ticket": true, "skills": true}
	for i, s := range c.Steps {
		isAgent := s.Agent != ""
		isCheckpoint := s.Checkpoint != ""
		if isAgent == isCheckpoint {
			return fmt.Errorf("workflow: step %d: exactly one of 'agent' or 'checkpoint' is required", i)
		}
		if isAgent {
			if seen[s.Agent] {
				return fmt.Errorf("workflow: step %d: duplicate step name %q", i, s.Agent)
			}
			seen[s.Agent] = true
			if s.Prompt == "" {
				return fmt.Errorf("workflow: agent step %q: prompt is required", s.Agent)
			}
			_, inline := c.Prompts[s.Prompt]
			_, fromFile := c.PromptFiles[s.Prompt]
			if !inline && !fromFile {
				return fmt.Errorf("workflow: agent step %q: unknown prompt %q", s.Agent, s.Prompt)
			}
			for _, name := range s.Sidecars {
				if _, ok := c.Sidecars[name]; !ok {
					return fmt.Errorf("workflow: agent step %q: unknown sidecar %q", s.Agent, name)
				}
			}
			if s.BudgetSliceUSD < 0 {
				return fmt.Errorf("workflow: agent step %q: budget_slice_usd must be >= 0", s.Agent)
			}
			if s.MaxTurns < 0 {
				return fmt.Errorf("workflow: agent step %q: max_turns must be >= 0", s.Agent)
			}
			for _, in := range s.Inputs {
				if !produced[in] {
					return fmt.Errorf("workflow: agent step %q: input %q is not produced by an earlier step", s.Agent, in)
				}
			}
			for _, out := range s.Outputs {
				produced[out] = true
			}
			if s.OutputSchema != "" {
				if _, ok := c.Schemas[s.OutputSchema]; !ok {
					return fmt.Errorf("workflow: agent step %q: output_schema %q has no entry in the top-level schemas map", s.Agent, s.OutputSchema)
				}
			}
			if s.OnReject != "" {
				return fmt.Errorf("workflow: agent step %q: on_reject is a checkpoint-only field", s.Agent)
			}
			if s.SystemPrompt != "" && s.SystemPromptFile != "" {
				return fmt.Errorf("workflow: agent step %q: system_prompt and system_prompt_file are mutually exclusive", s.Agent)
			}
			if err := validateSkillList(fmt.Sprintf("agent step %q skills", s.Agent), s.Skills); err != nil {
				return err
			}
			for i, p := range s.Context {
				if p == "" {
					return fmt.Errorf("workflow: agent step %q: context[%d]: path is required", s.Agent, i)
				}
			}
		} else {
			if seen[s.Checkpoint] {
				return fmt.Errorf("workflow: step %d: duplicate step name %q", i, s.Checkpoint)
			}
			seen[s.Checkpoint] = true
			switch s.OnReject {
			case "", "fail", "send_back":
			default:
				return fmt.Errorf("workflow: checkpoint %q: on_reject must be fail or send_back, got %q", s.Checkpoint, s.OnReject)
			}
			if s.Prompt != "" || len(s.Sidecars) > 0 || s.BudgetSliceUSD != 0 || s.Model != "" ||
				s.MaxTurns != 0 || len(s.Inputs) > 0 || len(s.Outputs) > 0 || s.OutputSchema != "" ||
				len(s.Skills) > 0 || s.SystemPrompt != "" || s.SystemPromptFile != "" || len(s.Context) > 0 {
				return fmt.Errorf("workflow: checkpoint %q: agent-step fields are not allowed on a checkpoint", s.Checkpoint)
			}
			// E6b (FLOWS S5, 2026-07-09): import mirror of dispatch's F36
			// render-time guard — the rendered checkpoint task mounts the
			// definition's "platform" sidecar (the fixed role key); without
			// that entry every dispatch errors at render. Fail at import.
			if _, ok := c.Sidecars["platform"]; !ok {
				return fmt.Errorf("workflow: checkpoint %q requires a %q sidecar in the workflow definition (dispatch's F36 render guard, mirrored at import)", s.Checkpoint, "platform")
			}
		}
	}

	// E6a (FLOWS S4, 2026-07-09): the implicit terminal harvest step consumes
	// the "workspace" artifact (11-dispatch hard-codes it as harvest's input,
	// for push and advisory shapes alike). A definition in which no step
	// outputs it would import clean and then fail every run at harvest —
	// close the validate-clean/run-broken gap here.
	if !produced["workspace"] {
		return fmt.Errorf("workflow: no step outputs %q — the implicit harvest step consumes it, so every run of this definition would fail at harvest", "workspace")
	}

	switch c.HITL.AskTimeout {
	case "", "park", "default", "fail":
	default:
		return fmt.Errorf("workflow: hitl.ask_timeout must be park, default, or fail, got %q", c.HITL.AskTimeout)
	}
	if c.HITL.AskTimeoutSeconds < 0 {
		return fmt.Errorf("workflow: hitl.ask_timeout_seconds must be >= 0")
	}
	// A default/fail timeout policy needs a positive deadline to act on: with
	// ask_timeout_seconds <= 0 the ask never times out, so the policy never
	// fires and the run parks forever anyway — the opposite of what the author
	// asked for. Reject the incoherent combination loudly at import instead.
	if (c.HITL.AskTimeout == "default" || c.HITL.AskTimeout == "fail") && c.HITL.AskTimeoutSeconds <= 0 {
		return fmt.Errorf("workflow: hitl.ask_timeout %q requires ask_timeout_seconds > 0 (got %d) — otherwise the ask never times out and the run parks forever", c.HITL.AskTimeout, c.HITL.AskTimeoutSeconds)
	}

	for i, g := range c.GatePolicy.Gates {
		if !validGates[g.Gate] {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: gate must be build|test|lint, got %q", i, g.Gate)
		}
		if !validGateScopes[g.Scope] {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: scope must be affected|full|affected_then_full, got %q", i, g.Scope)
		}
		if g.Timeout != "" {
			if _, err := time.ParseDuration(g.Timeout); err != nil {
				return fmt.Errorf("workflow: gate_policy.gates[%d]: invalid timeout %q", i, g.Timeout)
			}
		}
		if g.Retries < 0 || g.Retries > 2 {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: retries must be 0-2, got %d", i, g.Retries)
		}
	}
	if len(c.GatePolicy.Gates) > 0 && c.GatePolicy.OnGateFailure != "needs_review" {
		return fmt.Errorf("workflow: gate_policy.on_gate_failure must be needs_review (only v1 value), got %q", c.GatePolicy.OnGateFailure)
	}

	if err := c.validateJudge(); err != nil {
		return err
	}

	return nil
}

// validatePromptTemplate is the §6.2 import gate for one prompt body:
// it must parse as a Go text/template and render against the spec-less
// dispatch ground state. Shared by Validate (inline prompts) and
// Compile (prompt_files content, validated after resolution).
//
// E2b (contracts §6.2 nil-safety, 2026-07-09): executing against the
// spec-less context every dispatch render sees means a bare
// `.Spec.<field>` deref fails HERE with a clear error instead of
// failing the dispatch of every ticket that runs this definition.
func validatePromptTemplate(key, body string) error {
	tmpl, err := template.New(key).Parse(body)
	if err != nil {
		return fmt.Errorf("workflow: prompt %q: invalid Go text/template: %w", key, err)
	}
	if err := tmpl.Execute(io.Discard, nilRenderContext); err != nil {
		return fmt.Errorf("workflow: prompt %q: does not render against a spec-less ticket (.Spec is nil and .Tasks is empty at every dispatch render — guard with {{if .Spec}} or read via platform-mcp read_ticket/list_tasks): %w", key, err)
	}
	return nil
}

func validateSkillList(where string, names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" {
			return fmt.Errorf("workflow: %s: skill name is required", where)
		}
		if strings.ContainsAny(n, `/\`) || strings.HasPrefix(n, ".") {
			return fmt.Errorf("workflow: %s: skill name %q must be a bare directory name under skills/", where, n)
		}
		if seen[n] {
			return fmt.Errorf("workflow: %s: duplicate skill %q", where, n)
		}
		seen[n] = true
	}
	return nil
}

func (c *Config) validateJudge() error {
	if c.Judge != nil {
		if len(c.Judge.Rubric) == 0 {
			return fmt.Errorf("workflow: judge.rubric must have at least one dimension")
		}
		dims := map[string]bool{}
		for _, d := range c.Judge.Rubric {
			if d.Name == "" {
				return fmt.Errorf("workflow: judge.rubric: dimension name is required")
			}
			if dims[d.Name] {
				return fmt.Errorf("workflow: judge.rubric: duplicate dimension %q", d.Name)
			}
			dims[d.Name] = true
			if d.Weight <= 0 {
				return fmt.Errorf("workflow: judge.rubric %q: weight must be > 0", d.Name)
			}
		}
		if c.Judge.PassThreshold < 0 || c.Judge.PassThreshold > 10 {
			return fmt.Errorf("workflow: judge.pass_threshold must be within [0,10]")
		}
	}

	return nil
}
