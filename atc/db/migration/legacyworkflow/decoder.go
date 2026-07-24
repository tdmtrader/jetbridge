// Package legacyworkflow freezes the workflow source behavior released with
// migration 1773106101. It deliberately has no dependency on the live agent
// workflow or ATC step models so later runtime grammar changes cannot alter
// historical migration replay.
package legacyworkflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

const (
	maxManifestFiles     = 512
	maxManifestFileBytes = 1 << 20
	maxManifestBytes     = 10 << 20
)

var typeRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*/v[1-9][0-9]*$`)

type Metadata struct {
	Name             string
	SchemaVersion    int
	SignatureVersion int
}

type PublicSignature struct {
	SignatureVersion int
	Inputs           []Port
	Outputs          []Port
}

type Port struct {
	Name     string
	Type     string
	Optional bool
}

func (signature PublicSignature) Equal(other PublicSignature) bool {
	if signature.SignatureVersion != other.SignatureVersion ||
		len(signature.Inputs) != len(other.Inputs) ||
		len(signature.Outputs) != len(other.Outputs) {
		return false
	}
	for index := range signature.Inputs {
		if signature.Inputs[index] != other.Inputs[index] {
			return false
		}
	}
	for index := range signature.Outputs {
		if signature.Outputs[index] != other.Outputs[index] {
			return false
		}
	}
	return true
}

func DecodeManifest(files map[string]string) (Metadata, *PublicSignature, error) {
	if err := validateManifest(files); err != nil {
		return Metadata{}, nil, err
	}
	raw, found := files["workflow.yml"]
	if !found {
		return Metadata{}, nil, fmt.Errorf("workflow: manifest has no workflow.yml")
	}
	version, err := decodeSchemaVersion(raw)
	if err != nil {
		return Metadata{}, nil, err
	}

	switch version {
	case 1, 2:
		config, err := decodeLegacy(raw)
		if err != nil {
			return Metadata{}, nil, err
		}
		if err := compileLegacyAssets(files, config); err != nil {
			return Metadata{}, nil, err
		}
		if strings.TrimSpace(config.Name) == "" {
			return Metadata{}, nil, fmt.Errorf("workflow: name is required")
		}
		return Metadata{Name: config.Name, SchemaVersion: config.SchemaVersion}, nil, nil
	case 3:
		return decodeFunction(files, raw)
	default:
		return Metadata{}, nil, fmt.Errorf("workflow: schema_version must be 1, 2, or 3, got %d", version)
	}
}

func validateManifest(files map[string]string) error {
	if len(files) == 0 {
		return fmt.Errorf("workflow: manifest has no files")
	}
	if len(files) > maxManifestFiles {
		return fmt.Errorf("workflow: manifest has %d files (max %d)", len(files), maxManifestFiles)
	}
	total := 0
	for path, content := range files {
		if err := validateManifestPath(path); err != nil {
			return err
		}
		if len(content) > maxManifestFileBytes {
			return fmt.Errorf("workflow: manifest file %q is %d bytes (max %d)", path, len(content), maxManifestFileBytes)
		}
		if !utf8.ValidString(content) {
			return fmt.Errorf("workflow: manifest file %q is not valid UTF-8 (binary assets are out of scope, design §2)", path)
		}
		total += len(content)
	}
	if total > maxManifestBytes {
		return fmt.Errorf("workflow: manifest is %d bytes total (max %d)", total, maxManifestBytes)
	}
	return nil
}

func validateManifestPath(path string) error {
	if path == "" {
		return fmt.Errorf("workflow: manifest contains an empty path")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("workflow: manifest path %q is absolute; paths must be relative", path)
	}
	if strings.Contains(path, `\`) {
		return fmt.Errorf("workflow: manifest path %q contains a backslash; use forward slashes", path)
	}
	for _, segment := range strings.Split(path, "/") {
		switch {
		case segment == "":
			return fmt.Errorf("workflow: manifest path %q contains an empty segment", path)
		case segment == "." || segment == "..":
			return fmt.Errorf("workflow: manifest path %q contains a %q segment", path, segment)
		case strings.HasPrefix(segment, "."):
			return fmt.Errorf("workflow: manifest path %q contains hidden segment %q", path, segment)
		}
	}
	return nil
}

func resolveManifestFile(files map[string]string, path string) (string, error) {
	if err := validateManifestPath(path); err != nil {
		return "", err
	}
	content, found := files[path]
	if !found {
		return "", fmt.Errorf("workflow: manifest file %q is not in the manifest", path)
	}
	return content, nil
}

func decodeSchemaVersion(raw string) (int, error) {
	decoder := yaml.NewDecoder(strings.NewReader(raw))
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

type legacyConfig struct {
	SchemaVersion int    `yaml:"schema_version"`
	Name          string `yaml:"name"`
	Description   string `yaml:"description,omitempty"`
	SpecDelivery  string `yaml:"spec_delivery,omitempty"`

	Defaults         legacyDefaults           `yaml:"defaults,omitempty"`
	Budget           legacyBudget             `yaml:"budget,omitempty"`
	Sidecars         map[string]legacySidecar `yaml:"sidecars,omitempty"`
	Prompts          map[string]string        `yaml:"prompts,omitempty"`
	Schemas          map[string]string        `yaml:"schemas,omitempty"`
	Steps            []legacyStep             `yaml:"steps"`
	HITL             legacyHITL               `yaml:"hitl,omitempty"`
	GatePolicy       legacyGatePolicy         `yaml:"gate_policy,omitempty"`
	Judge            *legacyJudge             `yaml:"judge,omitempty"`
	PromptFiles      map[string]string        `yaml:"prompt_files,omitempty"`
	Skills           []string                 `yaml:"skills,omitempty"`
	SystemPrompt     string                   `yaml:"system_prompt,omitempty"`
	SystemPromptFile string                   `yaml:"system_prompt_file,omitempty"`
	Context          []string                 `yaml:"context,omitempty"`
}

type legacyDefaults struct {
	Model    string `yaml:"model,omitempty"`
	MaxTurns int    `yaml:"max_turns,omitempty"`
}

type legacyBudget struct {
	TicketUSD float64 `yaml:"ticket_usd,omitempty"`
	JudgeUSD  float64 `yaml:"judge_usd,omitempty"`
}

type legacySidecar struct {
	Image     string   `yaml:"image"`
	Role      string   `yaml:"role"`
	Providers []string `yaml:"providers,omitempty"`
}

type legacyStep struct {
	Agent            string   `yaml:"agent,omitempty"`
	Prompt           string   `yaml:"prompt,omitempty"`
	Sidecars         []string `yaml:"sidecars,omitempty"`
	BudgetSliceUSD   float64  `yaml:"budget_slice_usd,omitempty"`
	Model            string   `yaml:"model,omitempty"`
	MaxTurns         int      `yaml:"max_turns,omitempty"`
	Inputs           []string `yaml:"inputs,omitempty"`
	Outputs          []string `yaml:"outputs,omitempty"`
	OutputSchema     string   `yaml:"output_schema,omitempty"`
	Skills           []string `yaml:"skills,omitempty"`
	SystemPrompt     string   `yaml:"system_prompt,omitempty"`
	SystemPromptFile string   `yaml:"system_prompt_file,omitempty"`
	Context          []string `yaml:"context,omitempty"`
	Checkpoint       string   `yaml:"checkpoint,omitempty"`
	OnReject         string   `yaml:"on_reject,omitempty"`
}

type legacyHITL struct {
	AskTimeout        string `yaml:"ask_timeout,omitempty"`
	AskTimeoutSeconds int    `yaml:"ask_timeout_seconds,omitempty"`
}

type legacyGatePolicy struct {
	Gates         []legacyGate `yaml:"gates,omitempty"`
	OnGateFailure string       `yaml:"on_gate_failure,omitempty"`
}

type legacyGate struct {
	Gate    string `yaml:"gate"`
	Scope   string `yaml:"scope"`
	Focus   string `yaml:"focus,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
	Retries int    `yaml:"retries,omitempty"`
}

type legacyJudge struct {
	Rubric        []legacyRubricDimension `yaml:"rubric"`
	PassThreshold float64                 `yaml:"pass_threshold"`
}

type legacyRubricDimension struct {
	Name     string  `yaml:"name"`
	Weight   float64 `yaml:"weight"`
	Guidance string  `yaml:"guidance,omitempty"`
}

func decodeLegacy(raw string) (*legacyConfig, error) {
	var config legacyConfig
	if err := yaml.Unmarshal([]byte(raw), &config); err != nil {
		return nil, fmt.Errorf("parse workflow definition: %w", err)
	}
	var probe struct {
		Hooks any              `yaml:"hooks"`
		Steps []map[string]any `yaml:"steps"`
	}
	if err := yaml.Unmarshal([]byte(raw), &probe); err == nil {
		if probe.Hooks != nil {
			return nil, fmt.Errorf("workflow: hooks are not supported (deferred, design 2026-07-17); remove the hooks key")
		}
		for index, step := range probe.Steps {
			if _, found := step["hooks"]; found {
				return nil, fmt.Errorf("workflow: step %d: hooks are not supported (deferred, design 2026-07-17); remove the hooks key", index)
			}
		}
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

func (config *legacyConfig) sourceFormatField() string {
	switch {
	case len(config.PromptFiles) > 0:
		return "prompt_files"
	case len(config.Skills) > 0:
		return "skills"
	case config.SystemPrompt != "" || config.SystemPromptFile != "":
		return "system_prompt"
	case len(config.Context) > 0:
		return "context"
	}
	for _, step := range config.Steps {
		name := step.Agent
		if name == "" {
			name = step.Checkpoint
		}
		switch {
		case len(step.Skills) > 0:
			return fmt.Sprintf("step %q skills", name)
		case step.SystemPrompt != "" || step.SystemPromptFile != "":
			return fmt.Sprintf("step %q system_prompt", name)
		case len(step.Context) > 0:
			return fmt.Sprintf("step %q context", name)
		}
	}
	return ""
}

func (config *legacyConfig) validate() error {
	if config.SchemaVersion != 1 && config.SchemaVersion != 2 {
		return fmt.Errorf("workflow: schema_version must be 1 or 2, got %d", config.SchemaVersion)
	}
	if config.SchemaVersion == 1 {
		if field := config.sourceFormatField(); field != "" {
			return fmt.Errorf("workflow: %s requires schema_version: 2", field)
		}
	}
	if config.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	switch config.SpecDelivery {
	case "", "mcp", "files":
	default:
		return fmt.Errorf("workflow: spec_delivery must be mcp or files, got %q", config.SpecDelivery)
	}
	if config.Budget.TicketUSD < 0 {
		return fmt.Errorf("workflow: budget.ticket_usd must be >= 0")
	}
	if config.Budget.JudgeUSD < 0 {
		return fmt.Errorf("workflow: budget.judge_usd must be >= 0")
	}

	validSidecarRoles := map[string]bool{"dev": true, "platform": true, "gateway": true, "custom": true}
	for name, sidecar := range config.Sidecars {
		if sidecar.Image == "" {
			return fmt.Errorf("workflow: sidecar %q: image is required", name)
		}
		last := sidecar.Image[strings.LastIndex(sidecar.Image, "/")+1:]
		if !strings.Contains(last, ":") {
			return fmt.Errorf("workflow: sidecar %q: image %q must carry a pinned ':<version>' tag", name, sidecar.Image)
		}
		if !validSidecarRoles[sidecar.Role] {
			return fmt.Errorf("workflow: sidecar %q: role must be one of dev|platform|gateway|custom, got %q", name, sidecar.Role)
		}
		if len(sidecar.Providers) > 0 && sidecar.Role != "gateway" {
			return fmt.Errorf("workflow: sidecar %q: providers is only valid for role gateway", name)
		}
	}

	for key, body := range config.Prompts {
		if err := validatePromptTemplate(key, body); err != nil {
			return err
		}
	}
	for key, path := range config.PromptFiles {
		if _, duplicate := config.Prompts[key]; duplicate {
			return fmt.Errorf("workflow: prompt %q is defined both inline (prompts) and as a file (prompt_files)", key)
		}
		if path == "" {
			return fmt.Errorf("workflow: prompt_files %q: path is required", key)
		}
	}
	if config.SystemPrompt != "" && config.SystemPromptFile != "" {
		return fmt.Errorf("workflow: system_prompt and system_prompt_file are mutually exclusive")
	}
	if err := validateSkillList("skills", config.Skills); err != nil {
		return err
	}
	for index, path := range config.Context {
		if path == "" {
			return fmt.Errorf("workflow: context[%d]: path is required", index)
		}
	}

	if len(config.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}
	seen := map[string]bool{}
	produced := map[string]bool{"repo": true, "ticket": true, "skills": true}
	for index, step := range config.Steps {
		isAgent := step.Agent != ""
		isCheckpoint := step.Checkpoint != ""
		if isAgent == isCheckpoint {
			return fmt.Errorf("workflow: step %d: exactly one of 'agent' or 'checkpoint' is required", index)
		}
		if isAgent {
			if seen[step.Agent] {
				return fmt.Errorf("workflow: step %d: duplicate step name %q", index, step.Agent)
			}
			seen[step.Agent] = true
			if step.Prompt == "" {
				return fmt.Errorf("workflow: agent step %q: prompt is required", step.Agent)
			}
			_, inline := config.Prompts[step.Prompt]
			_, fromFile := config.PromptFiles[step.Prompt]
			if !inline && !fromFile {
				return fmt.Errorf("workflow: agent step %q: unknown prompt %q", step.Agent, step.Prompt)
			}
			for _, name := range step.Sidecars {
				if _, found := config.Sidecars[name]; !found {
					return fmt.Errorf("workflow: agent step %q: unknown sidecar %q", step.Agent, name)
				}
			}
			if step.BudgetSliceUSD < 0 {
				return fmt.Errorf("workflow: agent step %q: budget_slice_usd must be >= 0", step.Agent)
			}
			if step.MaxTurns < 0 {
				return fmt.Errorf("workflow: agent step %q: max_turns must be >= 0", step.Agent)
			}
			for _, input := range step.Inputs {
				if !produced[input] {
					return fmt.Errorf("workflow: agent step %q: input %q is not produced by an earlier step", step.Agent, input)
				}
			}
			for _, output := range step.Outputs {
				produced[output] = true
			}
			if step.OutputSchema != "" {
				if _, found := config.Schemas[step.OutputSchema]; !found {
					return fmt.Errorf("workflow: agent step %q: output_schema %q has no entry in the top-level schemas map", step.Agent, step.OutputSchema)
				}
			}
			if step.OnReject != "" {
				return fmt.Errorf("workflow: agent step %q: on_reject is a checkpoint-only field", step.Agent)
			}
			if step.SystemPrompt != "" && step.SystemPromptFile != "" {
				return fmt.Errorf("workflow: agent step %q: system_prompt and system_prompt_file are mutually exclusive", step.Agent)
			}
			if err := validateSkillList(fmt.Sprintf("agent step %q skills", step.Agent), step.Skills); err != nil {
				return err
			}
			for contextIndex, path := range step.Context {
				if path == "" {
					return fmt.Errorf("workflow: agent step %q: context[%d]: path is required", step.Agent, contextIndex)
				}
			}
			continue
		}

		if seen[step.Checkpoint] {
			return fmt.Errorf("workflow: step %d: duplicate step name %q", index, step.Checkpoint)
		}
		seen[step.Checkpoint] = true
		switch step.OnReject {
		case "", "fail", "send_back":
		default:
			return fmt.Errorf("workflow: checkpoint %q: on_reject must be fail or send_back, got %q", step.Checkpoint, step.OnReject)
		}
		if step.Prompt != "" || len(step.Sidecars) > 0 || step.BudgetSliceUSD != 0 || step.Model != "" ||
			step.MaxTurns != 0 || len(step.Inputs) > 0 || len(step.Outputs) > 0 || step.OutputSchema != "" ||
			len(step.Skills) > 0 || step.SystemPrompt != "" || step.SystemPromptFile != "" || len(step.Context) > 0 {
			return fmt.Errorf("workflow: checkpoint %q: agent-step fields are not allowed on a checkpoint", step.Checkpoint)
		}
		if _, found := config.Sidecars["platform"]; !found {
			return fmt.Errorf("workflow: checkpoint %q requires a %q sidecar in the workflow definition (dispatch's F36 render guard, mirrored at import)", step.Checkpoint, "platform")
		}
	}
	if !produced["workspace"] {
		return fmt.Errorf("workflow: no step outputs %q — the implicit harvest step consumes it, so every run of this definition would fail at harvest", "workspace")
	}

	switch config.HITL.AskTimeout {
	case "", "park", "default", "fail":
	default:
		return fmt.Errorf("workflow: hitl.ask_timeout must be park, default, or fail, got %q", config.HITL.AskTimeout)
	}
	if config.HITL.AskTimeoutSeconds < 0 {
		return fmt.Errorf("workflow: hitl.ask_timeout_seconds must be >= 0")
	}
	if (config.HITL.AskTimeout == "default" || config.HITL.AskTimeout == "fail") &&
		config.HITL.AskTimeoutSeconds <= 0 {
		return fmt.Errorf("workflow: hitl.ask_timeout %q requires ask_timeout_seconds > 0 (got %d) — otherwise the ask never times out and the run parks forever", config.HITL.AskTimeout, config.HITL.AskTimeoutSeconds)
	}

	validGates := map[string]bool{"build": true, "test": true, "lint": true}
	validGateScopes := map[string]bool{"affected": true, "full": true, "affected_then_full": true}
	for index, gate := range config.GatePolicy.Gates {
		if !validGates[gate.Gate] {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: gate must be build|test|lint, got %q", index, gate.Gate)
		}
		if !validGateScopes[gate.Scope] {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: scope must be affected|full|affected_then_full, got %q", index, gate.Scope)
		}
		if gate.Timeout != "" {
			if _, err := time.ParseDuration(gate.Timeout); err != nil {
				return fmt.Errorf("workflow: gate_policy.gates[%d]: invalid timeout %q", index, gate.Timeout)
			}
		}
		if gate.Retries < 0 || gate.Retries > 2 {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: retries must be 0-2, got %d", index, gate.Retries)
		}
	}
	if len(config.GatePolicy.Gates) > 0 && config.GatePolicy.OnGateFailure != "needs_review" {
		return fmt.Errorf("workflow: gate_policy.on_gate_failure must be needs_review (only v1 value), got %q", config.GatePolicy.OnGateFailure)
	}
	return config.validateJudge()
}

func (config *legacyConfig) validateJudge() error {
	if config.Judge == nil {
		return nil
	}
	if len(config.Judge.Rubric) == 0 {
		return fmt.Errorf("workflow: judge.rubric must have at least one dimension")
	}
	dimensions := map[string]bool{}
	for _, dimension := range config.Judge.Rubric {
		if dimension.Name == "" {
			return fmt.Errorf("workflow: judge.rubric: dimension name is required")
		}
		if dimensions[dimension.Name] {
			return fmt.Errorf("workflow: judge.rubric: duplicate dimension %q", dimension.Name)
		}
		dimensions[dimension.Name] = true
		if dimension.Weight <= 0 {
			return fmt.Errorf("workflow: judge.rubric %q: weight must be > 0", dimension.Name)
		}
	}
	if config.Judge.PassThreshold < 0 || config.Judge.PassThreshold > 10 {
		return fmt.Errorf("workflow: judge.pass_threshold must be within [0,10]")
	}
	return nil
}

func compileLegacyAssets(files map[string]string, config *legacyConfig) error {
	for key, path := range config.PromptFiles {
		content, found := files[path]
		if !found {
			return fmt.Errorf("workflow: prompt_files %q: %q is not in the manifest", key, path)
		}
		if err := validatePromptTemplate(key, content); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if config.SystemPromptFile != "" {
		if _, found := files[config.SystemPromptFile]; !found {
			return fmt.Errorf("workflow: system_prompt_file %q is not in the manifest", config.SystemPromptFile)
		}
	}

	skillNames := append([]string(nil), config.Skills...)
	contextPaths := append([]string(nil), config.Context...)
	for _, step := range config.Steps {
		if step.SystemPromptFile != "" {
			if _, found := files[step.SystemPromptFile]; !found {
				return fmt.Errorf("workflow: agent step %q: system_prompt_file %q is not in the manifest", step.Agent, step.SystemPromptFile)
			}
		}
		skillNames = append(skillNames, step.Skills...)
		contextPaths = append(contextPaths, step.Context...)
	}
	for _, path := range contextPaths {
		if _, found := files[path]; !found {
			return fmt.Errorf("workflow: context file %q is not in the manifest", path)
		}
	}
	for _, name := range skillNames {
		root := "skills/" + name + "/SKILL.md"
		if _, found := files[root]; !found {
			return fmt.Errorf("workflow: skill %q: %s is not in the manifest", name, root)
		}
	}
	return nil
}

var nilRenderContext = struct {
	Ticket map[string]any
	Spec   *struct{}
	Tasks  []map[string]any
	Params map[string]any
}{
	Ticket: map[string]any{},
	Tasks:  []map[string]any{},
	Params: map[string]any{},
}

func validatePromptTemplate(key, body string) error {
	parsed, err := template.New(key).Parse(body)
	if err != nil {
		return fmt.Errorf("workflow: prompt %q: invalid Go text/template: %w", key, err)
	}
	if err := parsed.Execute(io.Discard, nilRenderContext); err != nil {
		return fmt.Errorf("workflow: prompt %q: does not render against a spec-less ticket: %w", key, err)
	}
	return nil
}

func validateSkillList(where string, names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("workflow: %s: skill name is required", where)
		}
		if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
			return fmt.Errorf("workflow: %s: skill name %q must be a bare directory name under skills/", where, name)
		}
		if seen[name] {
			return fmt.Errorf("workflow: %s: duplicate skill %q", where, name)
		}
		seen[name] = true
	}
	return nil
}

type functionSource struct {
	SchemaVersion    int                           `json:"schema_version"`
	Name             string                        `json:"name"`
	SignatureVersion int                           `json:"signature_version"`
	Description      string                        `json:"description,omitempty"`
	Inputs           []functionPort                `json:"inputs"`
	Outputs          []functionOutput              `json:"outputs"`
	Capabilities     map[string]functionCapability `json:"capabilities,omitempty"`
	Resources        any                           `json:"resources,omitempty"`
	ResourceTypes    any                           `json:"resource_types,omitempty"`
	Prototypes       any                           `json:"prototypes,omitempty"`
	VarSources       any                           `json:"var_sources,omitempty"`
	Plan             any                           `json:"plan"`
}

type functionPort struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Optional    bool   `json:"optional,omitempty"`
	Description string `json:"description,omitempty"`
}

type functionOutput struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Optional    bool   `json:"optional,omitempty"`
	Description string `json:"description,omitempty"`
	From        string `json:"from"`
}

type functionCapability struct {
	Contract string          `json:"contract"`
	Sidecar  functionSidecar `json:"sidecar"`
}

type functionSidecar struct {
	Name          string                    `json:"name"`
	Image         string                    `json:"image,omitempty"`
	Command       []string                  `json:"command,omitempty"`
	Args          []string                  `json:"args,omitempty"`
	Env           []functionSidecarEnv      `json:"env,omitempty"`
	Ports         []functionSidecarPort     `json:"ports,omitempty"`
	Resources     *functionSidecarResources `json:"resources,omitempty"`
	WorkingDir    string                    `json:"workingDir,omitempty"`
	ImageArtifact string                    `json:"image_artifact,omitempty"`
}

type functionSidecarEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type functionSidecarPort struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

type functionSidecarResources struct {
	Requests functionSidecarResourceList `json:"requests,omitempty"`
	Limits   functionSidecarResourceList `json:"limits,omitempty"`
}

type functionSidecarResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

func decodeFunction(files map[string]string, raw string) (Metadata, *PublicSignature, error) {
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(raw)))
	var document any
	if err := decoder.Decode(&document); err != nil {
		return Metadata{}, nil, fmt.Errorf("parse workflow function: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Metadata{}, nil, fmt.Errorf("parse workflow function: exactly one YAML or JSON document is required")
		}
		return Metadata{}, nil, fmt.Errorf("parse workflow function trailing document: %w", err)
	}
	if err := validateFunctionSourceKeys(document); err != nil {
		return Metadata{}, nil, err
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("parse workflow function document: %w", err)
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(documentJSON))
	jsonDecoder.DisallowUnknownFields()
	var source functionSource
	if err := jsonDecoder.Decode(&source); err != nil {
		return Metadata{}, nil, fmt.Errorf("parse workflow function: %w", err)
	}
	if err := jsonDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Metadata{}, nil, fmt.Errorf("parse workflow function: unexpected trailing JSON value")
		}
		return Metadata{}, nil, fmt.Errorf("parse workflow function trailing JSON: %w", err)
	}
	if source.SchemaVersion != 3 {
		return Metadata{}, nil, fmt.Errorf("workflow: function parser requires schema_version 3, got %d", source.SchemaVersion)
	}
	if strings.TrimSpace(source.Name) == "" {
		return Metadata{}, nil, fmt.Errorf("workflow: name is required")
	}
	if source.SignatureVersion <= 0 {
		return Metadata{}, nil, fmt.Errorf("workflow: signature_version must be a positive integer, got %d", source.SignatureVersion)
	}
	if err := validateFunctionPorts(source.Inputs, "inputs"); err != nil {
		return Metadata{}, nil, err
	}
	if err := validateFunctionOutputs(source.Outputs); err != nil {
		return Metadata{}, nil, err
	}
	plan, ok := source.Plan.([]any)
	if !ok || len(plan) == 0 {
		return Metadata{}, nil, fmt.Errorf("workflow: plan must contain at least one step")
	}
	if err := validateCapabilityCatalog(source.Capabilities); err != nil {
		return Metadata{}, nil, err
	}
	if err := validateFunctionAssets(files, plan, source.Capabilities); err != nil {
		return Metadata{}, nil, err
	}

	metadata := Metadata{
		Name:             source.Name,
		SchemaVersion:    source.SchemaVersion,
		SignatureVersion: source.SignatureVersion,
	}
	signature := &PublicSignature{
		SignatureVersion: source.SignatureVersion,
		Inputs:           make([]Port, len(source.Inputs)),
		Outputs:          make([]Port, len(source.Outputs)),
	}
	for index, port := range source.Inputs {
		signature.Inputs[index] = Port{Name: port.Name, Type: port.Type, Optional: port.Optional}
	}
	for index, output := range source.Outputs {
		signature.Outputs[index] = Port{Name: output.Name, Type: output.Type, Optional: output.Optional}
	}
	return metadata, signature, nil
}

func validateFunctionPorts(ports []functionPort, field string) error {
	seen := map[string]struct{}{}
	for index, port := range ports {
		if strings.TrimSpace(port.Name) == "" {
			return fmt.Errorf("workflow: %s[%d]: port name is required", field, index)
		}
		if !typeRefPattern.MatchString(port.Type) {
			return fmt.Errorf("workflow: %s[%d]: port %q has invalid type reference %q", field, index, port.Name, port.Type)
		}
		if _, duplicate := seen[port.Name]; duplicate {
			return fmt.Errorf("workflow: %s: duplicate port %q", field, port.Name)
		}
		seen[port.Name] = struct{}{}
	}
	return nil
}

func validateFunctionOutputs(outputs []functionOutput) error {
	ports := make([]functionPort, len(outputs))
	for index, output := range outputs {
		ports[index] = functionPort{
			Name:        output.Name,
			Type:        output.Type,
			Optional:    output.Optional,
			Description: output.Description,
		}
		if strings.TrimSpace(output.From) == "" {
			return fmt.Errorf("workflow: output %q: from is required", output.Name)
		}
	}
	return validateFunctionPorts(ports, "outputs")
}

func validateCapabilityCatalog(catalog map[string]functionCapability) error {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	seenSidecars := map[string]string{}
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("workflow: capability name is required")
		}
		capability := catalog[name]
		if !typeRefPattern.MatchString(capability.Contract) {
			return fmt.Errorf("workflow: capability %q: invalid contract %q", name, capability.Contract)
		}
		if err := validateCapabilitySidecar(capability.Sidecar); err != nil {
			return fmt.Errorf("workflow: capability %q: %w", name, err)
		}
		if previous, duplicate := seenSidecars[capability.Sidecar.Name]; duplicate {
			return fmt.Errorf("workflow: capability %q: sidecar name %q is also declared by capability %q", name, capability.Sidecar.Name, previous)
		}
		seenSidecars[capability.Sidecar.Name] = name
	}
	return nil
}

func validateCapabilitySidecar(sidecar functionSidecar) error {
	var problems []string
	if sidecar.Name == "" {
		problems = append(problems, "missing 'name'")
	}
	if sidecar.Image == "" && sidecar.ImageArtifact == "" {
		problems = append(problems, "missing 'image' or 'image_artifact'")
	}
	if sidecar.Image != "" && sidecar.ImageArtifact != "" {
		problems = append(problems, "cannot specify both 'image' and 'image_artifact'")
	}
	for _, port := range sidecar.Ports {
		switch port.Protocol {
		case "", "TCP", "UDP", "SCTP":
		default:
			problems = append(problems, fmt.Sprintf("invalid port protocol %q (must be TCP, UDP, or SCTP)", port.Protocol))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid sidecar configuration: %s", strings.Join(problems, ", "))
	}
	if sidecar.Name == "main" || sidecar.Name == "artifact-helper" {
		return fmt.Errorf("invalid capability sidecar: reserved container name %q", sidecar.Name)
	}
	if sidecar.ImageArtifact != "" {
		return fmt.Errorf("invalid capability sidecar: image_artifact is not allowed")
	}
	const algorithm = "sha256:"
	at := strings.LastIndexByte(sidecar.Image, '@')
	if at <= 0 || len(sidecar.Image)-(at+1) != len(algorithm)+64 || !strings.HasPrefix(sidecar.Image[at+1:], algorithm) {
		return fmt.Errorf("invalid capability sidecar: image must be an OCI reference pinned to an exact sha256 digest")
	}
	for _, character := range sidecar.Image[at+1+len(algorithm):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return fmt.Errorf("invalid capability sidecar: image digest must contain 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func validateFunctionSourceKeys(document any) error {
	root, ok := document.(map[string]any)
	if !ok {
		return nil
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
			if err := validateInlineSidecarSource(sidecar, path+".sidecar"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFunctionPlanSource(plan any, path string) error {
	steps, ok := plan.([]any)
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

func validateFunctionStepSource(value any, path string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if _, isAgent := step["agent"]; isAgent {
		if err := validateAgentAssetSourcePresence(step, path); err != nil {
			return err
		}
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
		if err := validateObjectSource(policy, path+".gate_policy", []string{"gates", "on_gate_failure"}); err != nil {
			return err
		}
	}
	if judge, found := step["judge"]; found {
		if err := validateObjectSource(judge, path+".judge", []string{"rubric", "pass_threshold", "model", "budget_usd"}); err != nil {
			return err
		}
	}
	return nil
}

func validateAgentAssetSourcePresence(step map[string]any, path string) error {
	identity := rawAgentIdentity(step)
	_, promptPresent := step["prompt"]
	_, promptFilePresent := step["prompt_file"]
	if promptPresent && promptFilePresent {
		return fmt.Errorf("workflow: %s: %s: prompt and prompt_file are mutually exclusive", path, identity)
	}
	if promptFilePresent {
		if err := validateNonemptyStringSourceField(step["prompt_file"], "prompt_file"); err != nil {
			return fmt.Errorf("workflow: %s: %s: %w", path, identity, err)
		}
	}
	_, systemPromptPresent := step["system_prompt"]
	_, systemPromptFilePresent := step["system_prompt_file"]
	if systemPromptPresent && systemPromptFilePresent {
		return fmt.Errorf("workflow: %s: %s: system_prompt and system_prompt_file are mutually exclusive", path, identity)
	}
	if systemPromptFilePresent {
		if err := validateNonemptyStringSourceField(step["system_prompt_file"], "system_prompt_file"); err != nil {
			return fmt.Errorf("workflow: %s: %s: %w", path, identity, err)
		}
	}
	return nil
}

func rawAgentIdentity(step map[string]any) string {
	name, _ := step["agent"].(string)
	identity := fmt.Sprintf("agent %q", name)
	if functionID, ok := step["function_id"].(string); ok && functionID != "" {
		identity += fmt.Sprintf(" (function_id %q)", functionID)
	}
	return identity
}

func validateNonemptyStringSourceField(value any, field string) error {
	text, ok := value.(string)
	if !ok || text == "" {
		return fmt.Errorf("%s must be a nonempty string", field)
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
		return nil
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
		if err := rejectObjectKeys(object, fmt.Sprintf("%s[%d]", path, index), allowed); err != nil {
			return err
		}
	}
	return nil
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

func validateFunctionAssets(files map[string]string, plan []any, catalog map[string]functionCapability) error {
	for index, step := range plan {
		if err := validateFunctionStepAssets(files, step, catalog, fmt.Sprintf("workflow.plan[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateFunctionStepAssets(files map[string]string, value any, catalog map[string]functionCapability, path string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("workflow: %s: step must be an object", path)
	}
	if _, isAgent := step["agent"]; isAgent {
		if err := validateFunctionAgentAssets(files, step, catalog, path); err != nil {
			return err
		}
	}
	if nested, found := step["try"]; found {
		if err := validateFunctionStepAssets(files, nested, catalog, path+".try"); err != nil {
			return err
		}
	}
	if nested, found := step["do"]; found {
		if err := validateFunctionStepAssetList(files, nested, catalog, path+".do"); err != nil {
			return err
		}
	}
	if nested, found := step["in_parallel"]; found {
		switch config := nested.(type) {
		case []any:
			if err := validateFunctionStepAssetList(files, config, catalog, path+".in_parallel"); err != nil {
				return err
			}
		case map[string]any:
			if steps, found := config["steps"]; found {
				if err := validateFunctionStepAssetList(files, steps, catalog, path+".in_parallel.steps"); err != nil {
					return err
				}
			}
		}
	}
	for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if nested, found := step[hook]; found {
			if err := validateFunctionStepAssets(files, nested, catalog, path+"."+hook); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFunctionStepAssetList(files map[string]string, value any, catalog map[string]functionCapability, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, step := range steps {
		if err := validateFunctionStepAssets(files, step, catalog, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateFunctionAgentAssets(files map[string]string, step map[string]any, catalog map[string]functionCapability, path string) error {
	identity := rawAgentIdentity(step)
	name, ok := step["agent"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("workflow: %s: agent name is required", path)
	}
	if sidecars, found := step["sidecars"]; found {
		if entries, ok := sidecars.([]any); !ok || len(entries) > 0 {
			return fmt.Errorf("workflow: %s: direct sidecars are not allowed in version 3; declare a named capability", identity)
		}
	}

	prompt, _ := step["prompt"].(string)
	if promptFile, found := step["prompt_file"]; found {
		path, _ := promptFile.(string)
		content, err := resolveManifestFile(files, path)
		if err != nil {
			return fmt.Errorf("workflow: %s: prompt_file %q: %w", identity, path, err)
		}
		prompt = content
	}
	if prompt == "" {
		return fmt.Errorf("workflow: %s: prompt is required", identity)
	}
	if systemPath, found := step["system_prompt_file"]; found {
		path, _ := systemPath.(string)
		if _, err := resolveManifestFile(files, path); err != nil {
			return fmt.Errorf("workflow: %s: system_prompt_file %q: %w", identity, path, err)
		}
	}
	if context, found := step["context"]; found && context != "" {
		return fmt.Errorf("workflow: %s: context is compiled-only; use context_files", identity)
	}
	if paths, found := step["context_files"]; found {
		for _, path := range stringList(paths) {
			if _, err := resolveManifestFile(files, path); err != nil {
				return fmt.Errorf("workflow: %s: context_file %q: %w", identity, path, err)
			}
		}
	}
	if skills, found := step["skills"]; found {
		names := stringList(skills)
		if err := validateFunctionSkillNames(names); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		for _, name := range names {
			if _, err := resolveManifestFile(files, "skills/"+name+"/SKILL.md"); err != nil {
				return fmt.Errorf("workflow: %s: skill %q: %w", identity, name, err)
			}
		}
	}
	if selected, found := step["capabilities"]; found {
		seen := map[string]struct{}{}
		for _, name := range stringList(selected) {
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("workflow: %s: duplicate capability reference %q", identity, name)
			}
			seen[name] = struct{}{}
			if _, found := catalog[name]; !found {
				return fmt.Errorf("workflow: %s: unknown capability %q", identity, name)
			}
		}
	}
	return nil
}

func validateFunctionSkillNames(names []string) error {
	seen := map[string]struct{}{}
	for _, name := range names {
		switch {
		case name == "":
			return fmt.Errorf("skill name is required")
		case strings.HasPrefix(name, "."):
			return fmt.Errorf("skill name %q must not be dot-prefixed", name)
		case strings.ContainsAny(name, `/\`):
			return fmt.Errorf("skill name %q must be a bare directory name", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate skill %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func stringList(value any) []string {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		text, ok := entry.(string)
		if ok {
			result = append(result, text)
		}
	}
	return result
}
