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
	pathpkg "path"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	containername "github.com/google/go-containerregistry/pkg/name"
)

const (
	maxManifestFiles     = 512
	maxManifestFileBytes = 1 << 20
	maxManifestBytes     = 10 << 20
)

var (
	typeRefPattern           = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*/v[1-9][0-9]*$`)
	agentOutputNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	frozenIdentifierPattern  = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]*$`)
	frozenNumericIdentifier  = regexp.MustCompile(`^\d+$`)
	frozenMemoryLimitPattern = regexp.MustCompile(`(?i)^([0-9]+)(([KMG])(i)?B?)?$`)
	frozenSourcedVarPattern  = regexp.MustCompile(`\(\(([-/.\w\pL]+):[-/.:@"\w\pL]+\)\)`)
)

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
	if err := validateFrozenOrdinaryTypes(&source, plan); err != nil {
		return Metadata{}, nil, fmt.Errorf("workflow: invalid Concourse config: %w", err)
	}
	if err := validateFunctionAssets(files, plan, source.Capabilities); err != nil {
		return Metadata{}, nil, err
	}
	if err := validateFunctionSemantics(&source, plan); err != nil {
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
	if _, err := containername.NewDigest(sidecar.Image, containername.StrictValidation); err != nil {
		return fmt.Errorf("invalid capability sidecar: image is not a valid OCI digest reference: %w", err)
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
	for _, declaration := range []struct {
		field   string
		allowed []string
	}{
		{
			field: "resources",
			allowed: []string{
				"name", "old_name", "public", "webhook_token", "type", "source", "check_every",
				"check_timeout", "tags", "version", "icon", "expose_build_created_by",
			},
		},
		{
			field: "resource_types",
			allowed: []string{
				"name", "type", "image", "source", "defaults", "privileged", "check_every", "tags", "params",
			},
		},
		{
			field: "prototypes",
			allowed: []string{
				"name", "type", "source", "defaults", "privileged", "check_every", "tags", "params",
			},
		},
		{field: "var_sources", allowed: []string{"name", "type", "config"}},
	} {
		if value, found := root[declaration.field]; found {
			if err := validateObjectListSource(value, "workflow."+declaration.field, declaration.allowed); err != nil {
				return err
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
	if err := validateFunctionStepEnvelopeSource(step, path); err != nil {
		return err
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

var functionStepCoreFields = map[string][]string{
	"agent": {
		"agent", "function_id", "prompt", "prompt_file", "system_prompt_file", "context_files",
		"model", "max_turns", "budget_slice_usd", "output_schema", "system_prompt", "context",
		"skills", "sidecars", "inputs", "outputs", "capabilities", "input_types", "output_types",
		"env", "timeout", "container_limits", "container_requests",
	},
	"harvest": {
		"harvest", "workspace", "repo", "target_branch", "ticket_id", "pipeline_run_id", "branch",
		"push", "env", "dev_mcp", "gate_policy", "judge", "timeout",
	},
	"run": {
		"run", "type", "params", "privileged", "tags", "container_limits", "container_requests", "timeout",
	},
	"task": {
		"task", "function_id", "privileged", "hermetic", "file", "container_limits",
		"container_requests", "config", "params", "vars", "tags", "input_mapping", "output_mapping",
		"input_types", "output_types", "image", "timeout", "sidecars",
	},
	"put": {
		"put", "resource", "params", "inputs", "tags", "get_params", "timeout", "no_get",
	},
	"get": {
		"get", "resource", "version", "params", "passed", "trigger", "tags", "timeout", "skip_download",
	},
	"set_pipeline": {"set_pipeline", "file", "team", "vars", "var_files", "instance_vars"},
	"load_var":     {"load_var", "file", "format", "reveal"},
	"try":          {"try"},
	"do":           {"do"},
	"in_parallel":  {"in_parallel"},
}

var functionStepWrapperFields = []string{
	"ensure", "on_error", "on_abort", "on_failure", "on_success",
	"across", "attempts", "timeout",
}

func validateFunctionStepEnvelopeSource(step map[string]any, path string) error {
	core := ""
	for _, name := range []string{
		"agent", "harvest", "run", "task", "put", "get",
		"set_pipeline", "load_var", "try", "do", "in_parallel",
	} {
		if _, found := step[name]; !found {
			continue
		}
		if core != "" {
			return fmt.Errorf("workflow: %s: multiple core step fields %q and %q", path, core, name)
		}
		core = name
	}
	if core == "" {
		return fmt.Errorf("workflow: %s: no core step type declared", path)
	}

	allowed := append([]string{}, functionStepCoreFields[core]...)
	allowed = append(allowed, functionStepWrapperFields...)
	if _, found := step["across"]; found {
		allowed = append(allowed, "fail_fast")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	unknown := make([]string, 0)
	for field := range step {
		if _, found := allowedSet[field]; !found {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("workflow: %s: unknown step field %q", path, unknown[0])
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

type functionAssetValidator struct {
	files              map[string]string
	catalog            map[string]functionCapability
	manifestPaths      []string
	skillTrees         map[string][]string
	capabilityBytes    map[string]int
	selectedSkillPaths map[string]struct{}
	compiledAssetBytes int
	compiledSkillBytes int
}

func validateFunctionAssets(files map[string]string, plan []any, catalog map[string]functionCapability) error {
	validator := &functionAssetValidator{
		files:              files,
		catalog:            catalog,
		manifestPaths:      make([]string, 0, len(files)),
		skillTrees:         make(map[string][]string),
		capabilityBytes:    make(map[string]int, len(catalog)),
		selectedSkillPaths: make(map[string]struct{}),
	}
	for path := range files {
		validator.manifestPaths = append(validator.manifestPaths, path)
	}
	sort.Strings(validator.manifestPaths)
	for name, capability := range catalog {
		canonical, err := json.Marshal(capability.Sidecar)
		if err != nil {
			return fmt.Errorf("workflow: capability %q: canonicalize sidecar: %w", name, err)
		}
		validator.capabilityBytes[name] = len(canonical)
	}

	for index, step := range plan {
		if err := validator.validateStep(step, fmt.Sprintf("workflow.plan[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func (validator *functionAssetValidator) validateStep(value any, path string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("workflow: %s: step must be an object", path)
	}
	if _, isAgent := step["agent"]; isAgent {
		if err := validator.validateAgent(step, path); err != nil {
			return err
		}
	}
	if nested, found := step["try"]; found {
		if err := validator.validateStep(nested, path+".try"); err != nil {
			return err
		}
	}
	if nested, found := step["do"]; found {
		if err := validator.validateStepList(nested, path+".do"); err != nil {
			return err
		}
	}
	if nested, found := step["in_parallel"]; found {
		switch config := nested.(type) {
		case []any:
			if err := validator.validateStepList(config, path+".in_parallel"); err != nil {
				return err
			}
		case map[string]any:
			if steps, found := config["steps"]; found {
				if err := validator.validateStepList(steps, path+".in_parallel.steps"); err != nil {
					return err
				}
			}
		}
	}
	for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if nested, found := step[hook]; found {
			if err := validator.validateStep(nested, path+"."+hook); err != nil {
				return err
			}
		}
	}
	return nil
}

func (validator *functionAssetValidator) validateStepList(value any, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, step := range steps {
		if err := validator.validateStep(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func (validator *functionAssetValidator) validateAgent(step map[string]any, path string) error {
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
		content, err := resolveManifestFile(validator.files, path)
		if err != nil {
			return fmt.Errorf("workflow: %s: prompt_file %q: %w", identity, path, err)
		}
		prompt = content
	}
	if prompt == "" {
		return fmt.Errorf("workflow: %s: prompt is required", identity)
	}
	if err := validator.addCompiledAssetBytes(len(prompt), identity, "prompt"); err != nil {
		return err
	}

	systemPrompt, _ := step["system_prompt"].(string)
	if systemPath, found := step["system_prompt_file"]; found {
		path, _ := systemPath.(string)
		content, err := resolveManifestFile(validator.files, path)
		if err != nil {
			return fmt.Errorf("workflow: %s: system_prompt_file %q: %w", identity, path, err)
		}
		systemPrompt = content
	}
	if err := validator.addCompiledAssetBytes(len(systemPrompt), identity, "system_prompt"); err != nil {
		return err
	}
	if context, found := step["context"]; found && context != "" {
		return fmt.Errorf("workflow: %s: context is compiled-only; use context_files", identity)
	}
	if paths, found := step["context_files"]; found {
		seenContext := make(map[string]struct{})
		for _, path := range stringList(paths) {
			if _, duplicate := seenContext[path]; duplicate {
				continue
			}
			content, err := resolveManifestFile(validator.files, path)
			if err != nil {
				return fmt.Errorf("workflow: %s: context_file %q: %w", identity, path, err)
			}
			seenContext[path] = struct{}{}
			for _, partBytes := range []int{len("## "), len(path), len("\n\n"), len(content), len("\n\n")} {
				if err := validator.addCompiledAssetBytes(partBytes, identity, "context"); err != nil {
					return err
				}
			}
		}
	}
	if skills, found := step["skills"]; found {
		names := stringList(skills)
		if err := validateFunctionSkillNames(names); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		for _, name := range names {
			tree, err := validator.skillTree(name)
			if err != nil {
				return fmt.Errorf("workflow: %s: skill %q: %w", identity, name, err)
			}
			for _, path := range tree {
				if _, selected := validator.selectedSkillPaths[path]; selected {
					continue
				}
				content := validator.files[path]
				if err := validator.addCompiledSkillBytes(len(content), identity, path); err != nil {
					return err
				}
				if err := validator.addCompiledAssetBytes(len(content), identity, "skill "+path); err != nil {
					return err
				}
				validator.selectedSkillPaths[path] = struct{}{}
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
			if _, found := validator.catalog[name]; !found {
				return fmt.Errorf("workflow: %s: unknown capability %q", identity, name)
			}
			if err := validator.addCompiledAssetBytes(validator.capabilityBytes[name], identity, "capability "+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (validator *functionAssetValidator) skillTree(name string) ([]string, error) {
	if tree, found := validator.skillTrees[name]; found {
		return tree, nil
	}
	prefix := "skills/" + name + "/"
	if _, err := resolveManifestFile(validator.files, prefix+"SKILL.md"); err != nil {
		return nil, err
	}
	tree := make([]string, 0)
	for _, path := range validator.manifestPaths {
		if strings.HasPrefix(path, prefix) {
			tree = append(tree, path)
		}
	}
	validator.skillTrees[name] = tree
	return tree, nil
}

func (validator *functionAssetValidator) addCompiledAssetBytes(amount int, identity, asset string) error {
	if amount > maxManifestBytes-validator.compiledAssetBytes {
		return fmt.Errorf("workflow: %s: compiled assets exceed %d bytes while adding %s", identity, maxManifestBytes, asset)
	}
	validator.compiledAssetBytes += amount
	return nil
}

func (validator *functionAssetValidator) addCompiledSkillBytes(amount int, identity, path string) error {
	const maxCompiledSkillBytes = 512 << 10
	if amount > maxCompiledSkillBytes-validator.compiledSkillBytes {
		return fmt.Errorf("workflow: %s: compiled skills exceed %d bytes while adding %q", identity, maxCompiledSkillBytes, path)
	}
	validator.compiledSkillBytes += amount
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
		switch text := entry.(type) {
		case string:
			result = append(result, text)
		case nil:
			result = append(result, "")
		}
	}
	return result
}

type frozenSnapshotPresence uint8

const (
	frozenSnapshotGuaranteed frozenSnapshotPresence = iota
	frozenSnapshotConditional
)

type frozenSnapshotProducer struct {
	key  string
	path string
}

type frozenSnapshotBinding struct {
	typ       string
	presence  frozenSnapshotPresence
	typed     bool
	ambiguous bool
	producer  *frozenSnapshotProducer
	writePath string
}

type frozenSnapshotEnvironment map[string]frozenSnapshotBinding

type frozenSnapshotFlow struct {
	env         frozenSnapshotEnvironment
	produced    map[string]frozenSnapshotBinding
	mayProduced map[string]frozenSnapshotBinding
	allProduced map[string]frozenSnapshotBinding
}

type frozenSnapshotDeclaration struct {
	Type     string
	Optional bool
}

type frozenFlowChecker struct {
	functionIDs map[string]string
	acrossDepth int
}

func validateFunctionSemantics(source *functionSource, plan []any) error {
	entry := make(frozenSnapshotEnvironment, len(source.Inputs))
	for index, input := range source.Inputs {
		presence := frozenSnapshotGuaranteed
		if input.Optional {
			presence = frozenSnapshotConditional
		}
		entry[input.Name] = frozenSnapshotBinding{
			typ:       input.Type,
			presence:  presence,
			typed:     true,
			writePath: fmt.Sprintf("inputs[%d]", index),
		}
	}

	checker := &frozenFlowChecker{functionIDs: make(map[string]string)}
	result, err := checker.checkSequence(plan, entry, "plan")
	if err != nil {
		return err
	}
	selected := make(map[string]string, len(source.Outputs))
	for index, output := range source.Outputs {
		binding, found := result.env[output.From]
		if !found {
			return fmt.Errorf("workflow: outputs[%d] %q: source %q is unavailable (use before produce)", index, output.Name, output.From)
		}
		if !binding.typed {
			if binding.ambiguous {
				return fmt.Errorf("workflow: outputs[%d] %q: source %q has ambiguous possible writes at %s", index, output.Name, output.From, binding.writePath)
			}
			return fmt.Errorf("workflow: outputs[%d] %q: source %q is occupied by an untyped producer at %s", index, output.Name, output.From, binding.writePath)
		}
		if binding.typ != output.Type {
			return fmt.Errorf("workflow: outputs[%d] %q: source %q type mismatch: have %s, require %s", index, output.Name, output.From, binding.typ, output.Type)
		}
		if !output.Optional && binding.presence == frozenSnapshotConditional {
			return fmt.Errorf("workflow: outputs[%d] %q: required public output cannot use conditional source %q", index, output.Name, output.From)
		}
		if binding.producer == nil {
			return fmt.Errorf("workflow: outputs[%d] %q: source %q has no concrete typed producer; public input pass-through is unsupported", index, output.Name, output.From)
		}
		if previous, found := selected[binding.producer.key]; found {
			return fmt.Errorf("workflow: outputs[%d] %q and public output %q select the same internal output %q from %s", index, output.Name, previous, output.From, binding.producer.path)
		}
		selected[binding.producer.key] = output.Name
	}

	return validateFrozenOrdinaryFunction(source, plan)
}

func (checker *frozenFlowChecker) checkSequence(steps []any, entry frozenSnapshotEnvironment, path string) (frozenSnapshotFlow, error) {
	env := cloneFrozenEnvironment(entry)
	produced := make(map[string]frozenSnapshotBinding)
	mayProduced := make(map[string]frozenSnapshotBinding)
	allProduced := make(map[string]frozenSnapshotBinding)
	for index, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s[%d]: step config is required", path, index)
		}
		child, err := checker.checkStepFrom(step, env, fmt.Sprintf("%s[%d]", path, index), 0)
		if err != nil {
			return frozenSnapshotFlow{}, err
		}
		env = child.env
		mergeFrozenProduced(produced, child.produced)
		mergeFrozenPossible(mayProduced, child.mayProduced)
		mergeFrozenPossible(allProduced, child.allProduced)
	}
	return frozenSnapshotFlow{env: env, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

var frozenStepPrecedence = []string{
	"ensure", "on_error", "on_abort", "on_failure", "on_success",
	"across", "attempts", "agent", "harvest", "run", "task", "put", "get",
	"timeout", "set_pipeline", "load_var", "try", "do", "in_parallel",
}

func (checker *frozenFlowChecker) checkStepFrom(
	step map[string]any,
	entry frozenSnapshotEnvironment,
	path string,
	start int,
) (frozenSnapshotFlow, error) {
	for index := start; index < len(frozenStepPrecedence); index++ {
		field := frozenStepPrecedence[index]
		value, found := step[field]
		if !found {
			continue
		}
		switch field {
		case "ensure":
			main, err := checker.checkStepFrom(step, entry, path, index+1)
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			hookEntry := conservativeFrozenEnsureEnvironment(entry, main)
			hook, err := checker.checkNestedStep(value, hookEntry, path+".ensure")
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			outgoing := applyFrozenSuccessfulFlow(main.env, hook)
			produced := cloneFrozenProduced(main.produced)
			mergeFrozenProduced(produced, hook.produced)
			mayProduced := cloneFrozenProduced(main.mayProduced)
			mergeFrozenPossible(mayProduced, hook.mayProduced)
			allProduced := cloneFrozenProduced(main.allProduced)
			mergeFrozenPossible(allProduced, hook.allProduced)
			return frozenSnapshotFlow{env: outgoing, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
		case "on_error", "on_abort", "on_failure":
			main, err := checker.checkStepFrom(step, entry, path, index+1)
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			hook, err := checker.checkNestedStep(value, cloneFrozenEnvironment(entry), path+"."+field)
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			mergeFrozenPossible(main.allProduced, hook.allProduced)
			return main, nil
		case "on_success":
			main, err := checker.checkStepFrom(step, entry, path, index+1)
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			hook, err := checker.checkNestedStep(value, main.env, path+".on_success")
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			mergeFrozenProduced(main.produced, hook.produced)
			mergeFrozenPossible(main.mayProduced, hook.mayProduced)
			mergeFrozenPossible(main.allProduced, hook.allProduced)
			main.env = hook.env
			return main, nil
		case "across":
			checker.acrossDepth++
			_, err := checker.checkStepFrom(step, cloneFrozenEnvironment(entry), path+".across", index+1)
			checker.acrossDepth--
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			return emptyFrozenFlow(entry), nil
		case "attempts":
			child, err := checker.checkStepFrom(step, entry, path+".attempts", index+1)
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			child.allProduced = cloneFrozenProduced(child.mayProduced)
			return child, nil
		case "timeout":
			return checker.checkStepFrom(step, entry, path+".timeout", index+1)
		case "agent", "task":
			return checker.checkFrozenLeaf(field, step, entry, path)
		case "get":
			name, _ := value.(string)
			return writeFrozenUntyped(entry, name, path+".get("+name+")"), nil
		case "put", "run", "harvest", "set_pipeline", "load_var":
			return emptyFrozenFlow(entry), nil
		case "try":
			child, err := checker.checkNestedStep(value, cloneFrozenEnvironment(entry), path+".try")
			if err != nil {
				return frozenSnapshotFlow{}, err
			}
			return conditionalFrozenTryFlow(entry, child), nil
		case "do":
			steps, ok := value.([]any)
			if !ok {
				return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s.do: steps must be a list", path)
			}
			return checker.checkSequence(steps, entry, path+".do")
		case "in_parallel":
			steps, err := frozenParallelSteps(value)
			if err != nil {
				return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s.in_parallel: %w", path, err)
			}
			return checker.checkParallel(steps, entry, path)
		}
	}
	return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: step config is required", path)
}

func (checker *frozenFlowChecker) checkNestedStep(value any, entry frozenSnapshotEnvironment, path string) (frozenSnapshotFlow, error) {
	step, ok := value.(map[string]any)
	if !ok {
		return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: step config is required", path)
	}
	return checker.checkStepFrom(step, entry, path, 0)
}

func frozenParallelSteps(value any) ([]any, error) {
	switch config := value.(type) {
	case []any:
		return config, nil
	case map[string]any:
		steps, ok := config["steps"].([]any)
		if !ok {
			return nil, fmt.Errorf("steps must be a list")
		}
		return steps, nil
	default:
		return nil, fmt.Errorf("configuration must be a list or object")
	}
}

func (checker *frozenFlowChecker) checkParallel(steps []any, entry frozenSnapshotEnvironment, path string) (frozenSnapshotFlow, error) {
	branches := make([]frozenSnapshotFlow, len(steps))
	producerBranch := make(map[string]int)
	for index, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s.in_parallel.steps[%d]: step config is required", path, index)
		}
		branch, err := checker.checkStepFrom(step, cloneFrozenEnvironment(entry), fmt.Sprintf("%s.in_parallel.steps[%d]", path, index), 0)
		if err != nil {
			return frozenSnapshotFlow{}, err
		}
		branches[index] = branch
		for _, name := range sortedFrozenBindingKeys(branch.mayProduced) {
			if previous, found := producerBranch[name]; found {
				return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: parallel branches both produce %q (branches %d and %d)", path, name, previous, index)
			}
			producerBranch[name] = index
		}
	}

	env := cloneFrozenEnvironment(entry)
	produced := make(map[string]frozenSnapshotBinding)
	mayProduced := make(map[string]frozenSnapshotBinding)
	allProduced := make(map[string]frozenSnapshotBinding)
	names := make([]string, 0, len(producerBranch))
	for name := range producerBranch {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		branch := producerBranch[name]
		if binding, found := branches[branch].env[name]; found {
			env[name] = binding
		}
		if binding, found := branches[branch].produced[name]; found {
			produced[name] = binding
		}
		mayProduced[name] = branches[branch].mayProduced[name]
	}
	for index := range branches {
		mergeFrozenPossible(allProduced, branches[index].allProduced)
	}
	return frozenSnapshotFlow{env: env, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

func (checker *frozenFlowChecker) checkFrozenLeaf(
	kind string,
	step map[string]any,
	entry frozenSnapshotEnvironment,
	path string,
) (frozenSnapshotFlow, error) {
	displayName, _ := step[kind].(string)
	functionID, _ := step["function_id"].(string)
	ordinaryInputs := stringList(step["inputs"])
	ordinaryOutputs := stringList(step["outputs"])
	if kind == "task" {
		ordinaryInputs, ordinaryOutputs = frozenTaskArtifactNames(step)
	}
	typedInputs, err := frozenSnapshotInputs(step["input_types"])
	if err != nil {
		return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s.%s(%q): %w", path, kind, displayName, err)
	}
	typedOutputs, err := frozenSnapshotOutputs(step["output_types"])
	if err != nil {
		return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s.%s(%q): %w", path, kind, displayName, err)
	}

	identity := fmt.Sprintf("%s.%s(%q)", path, kind, displayName)
	typedNode := functionID != "" || len(typedInputs) > 0 || len(typedOutputs) > 0
	if typedNode {
		if strings.TrimSpace(functionID) == "" {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: typed node requires a nonblank authored function_id", identity)
		}
		if previous, found := checker.functionIDs[functionID]; found {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: duplicate function_id %q; first declared at %s", identity, functionID, previous)
		}
		checker.functionIDs[functionID] = identity
	}

	for _, name := range sortedUniqueFrozenStrings(ordinaryInputs) {
		binding, found := entry[name]
		if !found || !binding.typed {
			continue
		}
		if _, declared := typedInputs[name]; !declared {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: known typed artifact %q is consumed without input_types declaration", identity, name)
		}
	}
	for _, name := range sortedFrozenDeclarationKeys(typedInputs) {
		declaration := typedInputs[name]
		binding, found := entry[name]
		if !found {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: input %q is unavailable (use before produce or undeclared workflow input)", identity, name)
		}
		if !binding.typed {
			if binding.ambiguous {
				return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: input %q has ambiguous possible writes at %s", identity, name, binding.writePath)
			}
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: input %q is occupied by an untyped producer at %s", identity, name, binding.writePath)
		}
		if binding.typ != declaration.Type {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: input %q type mismatch: have %s, require %s", identity, name, binding.typ, declaration.Type)
		}
		if !declaration.Optional && binding.presence == frozenSnapshotConditional {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: required input %q cannot use a conditional binding", identity, name)
		}
	}

	env := cloneFrozenEnvironment(entry)
	produced := make(map[string]frozenSnapshotBinding)
	for _, name := range sortedUniqueFrozenStrings(ordinaryOutputs) {
		if _, typed := typedOutputs[name]; typed {
			continue
		}
		binding := frozenSnapshotBinding{presence: frozenSnapshotGuaranteed, writePath: identity}
		env[name] = binding
		produced[name] = binding
	}
	for _, name := range sortedFrozenDeclarationKeys(typedOutputs) {
		declaration := typedOutputs[name]
		if checker.acrossDepth > 0 {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: typed output %q is not allowed inside across local scope", identity, name)
		}
		if _, found := env[name]; found && declaration.Optional {
			return frozenSnapshotFlow{}, fmt.Errorf("workflow: %s: optional output %q shadows an existing artifact; its concrete producer would be path-dependent", identity, name)
		}
		presence := frozenSnapshotGuaranteed
		if declaration.Optional {
			presence = frozenSnapshotConditional
		}
		producer := &frozenSnapshotProducer{
			key:  kind + "\x00" + identity + "\x00" + functionID + "\x00" + name,
			path: identity,
		}
		binding := frozenSnapshotBinding{
			typ:       declaration.Type,
			presence:  presence,
			typed:     true,
			producer:  producer,
			writePath: identity,
		}
		env[name] = binding
		produced[name] = binding
	}
	return frozenSnapshotFlow{
		env:         env,
		produced:    produced,
		mayProduced: cloneFrozenProduced(produced),
		allProduced: cloneFrozenProduced(produced),
	}, nil
}

func frozenSnapshotInputs(value any) (map[string]frozenSnapshotDeclaration, error) {
	if value == nil {
		return nil, nil
	}
	configs, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input_types must be an object")
	}
	result := make(map[string]frozenSnapshotDeclaration, len(configs))
	for name, raw := range configs {
		config, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input_types[%q] must be an object", name)
		}
		typ, ok := config["type"].(string)
		if !ok || !typeRefPattern.MatchString(typ) {
			return nil, fmt.Errorf("input_types[%q] has invalid type reference", name)
		}
		optional := false
		if rawOptional, found := config["optional"]; found {
			var ok bool
			optional, ok = rawOptional.(bool)
			if !ok {
				return nil, fmt.Errorf("input_types[%q].optional must be a boolean", name)
			}
		}
		result[name] = frozenSnapshotDeclaration{Type: typ, Optional: optional}
	}
	return result, nil
}

func frozenSnapshotOutputs(value any) (map[string]frozenSnapshotDeclaration, error) {
	if value == nil {
		return nil, nil
	}
	configs, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("output_types must be an object")
	}
	result := make(map[string]frozenSnapshotDeclaration, len(configs))
	for name, raw := range configs {
		typ, ok := raw.(string)
		if !ok || !typeRefPattern.MatchString(typ) {
			return nil, fmt.Errorf("output_types[%q] has invalid type reference", name)
		}
		result[name] = frozenSnapshotDeclaration{Type: typ}
	}
	return result, nil
}

func frozenTaskArtifactNames(step map[string]any) ([]string, []string) {
	config, _ := step["config"].(map[string]any)
	inputs := frozenNamedObjectList(config["inputs"])
	outputs := frozenNamedObjectList(config["outputs"])
	inputMapping := frozenStringMap(step["input_mapping"])
	outputMapping := frozenStringMap(step["output_mapping"])
	for index, name := range inputs {
		if mapped, found := inputMapping[name]; found {
			inputs[index] = mapped
		}
	}
	for index, name := range outputs {
		if mapped, found := outputMapping[name]; found {
			outputs[index] = mapped
		}
	}
	return inputs, outputs
}

func frozenNamedObjectList(value any) []string {
	entries, _ := value.([]any)
	result := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		result = append(result, name)
	}
	return result
}

func frozenStringMap(value any) map[string]string {
	entries, _ := value.(map[string]any)
	result := make(map[string]string, len(entries))
	for key, raw := range entries {
		if text, ok := raw.(string); ok {
			result[key] = text
		} else if raw == nil {
			result[key] = ""
		}
	}
	return result
}

func writeFrozenUntyped(entry frozenSnapshotEnvironment, name, path string) frozenSnapshotFlow {
	env := cloneFrozenEnvironment(entry)
	binding := frozenSnapshotBinding{presence: frozenSnapshotGuaranteed, writePath: path}
	env[name] = binding
	writes := map[string]frozenSnapshotBinding{name: binding}
	return frozenSnapshotFlow{
		env:         env,
		produced:    writes,
		mayProduced: cloneFrozenProduced(writes),
		allProduced: cloneFrozenProduced(writes),
	}
}

func emptyFrozenFlow(entry frozenSnapshotEnvironment) frozenSnapshotFlow {
	return frozenSnapshotFlow{
		env:         cloneFrozenEnvironment(entry),
		produced:    make(map[string]frozenSnapshotBinding),
		mayProduced: make(map[string]frozenSnapshotBinding),
		allProduced: make(map[string]frozenSnapshotBinding),
	}
}

func conditionalFrozenTryFlow(entry frozenSnapshotEnvironment, child frozenSnapshotFlow) frozenSnapshotFlow {
	env := cloneFrozenEnvironment(entry)
	for _, name := range sortedFrozenBindingKeys(child.allProduced) {
		previous, existed := entry[name]
		if !existed {
			continue
		}
		env[name] = mergeFrozenBindingAlternatives(previous, child.allProduced[name])
	}
	return frozenSnapshotFlow{
		env:         env,
		produced:    make(map[string]frozenSnapshotBinding),
		mayProduced: cloneFrozenProduced(child.allProduced),
		allProduced: cloneFrozenProduced(child.allProduced),
	}
}

func conservativeFrozenEnsureEnvironment(entry frozenSnapshotEnvironment, main frozenSnapshotFlow) frozenSnapshotEnvironment {
	env := cloneFrozenEnvironment(entry)
	for _, name := range sortedFrozenBindingKeys(main.allProduced) {
		write := main.allProduced[name]
		previous, existed := entry[name]
		if !existed {
			write.presence = frozenSnapshotConditional
			env[name] = write
			continue
		}
		env[name] = mergeFrozenBindingAlternatives(previous, write)
	}
	return env
}

func applyFrozenSuccessfulFlow(base frozenSnapshotEnvironment, flow frozenSnapshotFlow) frozenSnapshotEnvironment {
	env := cloneFrozenEnvironment(base)
	for _, name := range sortedFrozenBindingKeys(flow.mayProduced) {
		if _, found := flow.produced[name]; found {
			if final, found := flow.env[name]; found {
				env[name] = final
			} else {
				env[name] = flow.produced[name]
			}
			continue
		}
		previous, existed := env[name]
		if existed {
			env[name] = mergeFrozenBindingAlternatives(previous, flow.mayProduced[name])
		}
	}
	return env
}

func mergeFrozenBindingAlternatives(left, right frozenSnapshotBinding) frozenSnapshotBinding {
	merged := frozenSnapshotBinding{
		presence:  frozenSnapshotGuaranteed,
		ambiguous: true,
		writePath: joinFrozenWritePaths(left.writePath, right.writePath),
	}
	if left.presence == frozenSnapshotConditional || right.presence == frozenSnapshotConditional {
		merged.presence = frozenSnapshotConditional
	}
	if left.typed && right.typed && left.typ == right.typ {
		merged.typed = true
		merged.typ = left.typ
		if left.producer == right.producer {
			merged.producer = left.producer
		}
	}
	return merged
}

func joinFrozenWritePaths(left, right string) string {
	switch {
	case left == "":
		return right
	case right == "" || left == right:
		return left
	default:
		return left + " or " + right
	}
}

func cloneFrozenEnvironment(source frozenSnapshotEnvironment) frozenSnapshotEnvironment {
	clone := make(frozenSnapshotEnvironment, len(source))
	for name, binding := range source {
		clone[name] = binding
	}
	return clone
}

func cloneFrozenProduced(source map[string]frozenSnapshotBinding) map[string]frozenSnapshotBinding {
	clone := make(map[string]frozenSnapshotBinding, len(source))
	for name, binding := range source {
		clone[name] = binding
	}
	return clone
}

func mergeFrozenProduced(destination, source map[string]frozenSnapshotBinding) {
	for _, name := range sortedFrozenBindingKeys(source) {
		destination[name] = source[name]
	}
}

func mergeFrozenPossible(destination, source map[string]frozenSnapshotBinding) {
	for _, name := range sortedFrozenBindingKeys(source) {
		binding := source[name]
		if previous, found := destination[name]; found {
			binding = mergeFrozenBindingAlternatives(previous, binding)
		}
		destination[name] = binding
	}
}

func sortedFrozenBindingKeys(values map[string]frozenSnapshotBinding) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedFrozenDeclarationKeys(values map[string]frozenSnapshotDeclaration) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedUniqueFrozenStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type frozenTypeValidator func(any, string) error

var frozenOrdinaryDeclarationTypeFields = map[string]map[string]frozenTypeValidator{
	"resources": {
		"name": frozenStringType, "old_name": frozenStringType,
		"public": frozenBoolType, "webhook_token": frozenStringType,
		"type": frozenStringType, "source": frozenObjectType,
		"check_every": frozenCheckEveryType, "check_timeout": frozenStringType,
		"tags": frozenStringListType, "version": frozenStringMapType,
		"icon": frozenStringType, "expose_build_created_by": frozenBoolType,
	},
	"resource_types": {
		"name": frozenStringType, "type": frozenStringType, "image": frozenStringType,
		"source": frozenObjectType, "defaults": frozenObjectType,
		"privileged": frozenBoolType, "check_every": frozenCheckEveryType,
		"tags": frozenStringListType, "params": frozenObjectType,
	},
	"prototypes": {
		"name": frozenStringType, "type": frozenStringType,
		"source": frozenObjectType, "defaults": frozenObjectType,
		"privileged": frozenBoolType, "check_every": frozenCheckEveryType,
		"tags": frozenStringListType, "params": frozenObjectType,
	},
	"var_sources": {
		"name": frozenStringType, "type": frozenStringType, "config": frozenAnyType,
	},
}

func validateFrozenOrdinaryTypes(source *functionSource, plan []any) error {
	declarations := map[string]any{
		"resources": source.Resources, "resource_types": source.ResourceTypes,
		"prototypes": source.Prototypes, "var_sources": source.VarSources,
	}
	for _, name := range []string{"resources", "resource_types", "prototypes", "var_sources"} {
		if err := validateFrozenTypedObjectList(declarations[name], name, frozenOrdinaryDeclarationTypeFields[name]); err != nil {
			return err
		}
	}
	for index, raw := range plan {
		step, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("plan[%d] must be an object", index)
		}
		if err := validateFrozenStepTypes(step, fmt.Sprintf("jobs.entry.plan[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

var (
	frozenOrdinaryStepTypeFields    map[string]map[string]frozenTypeValidator
	frozenOrdinaryWrapperTypeFields map[string]frozenTypeValidator
)

func init() {
	frozenOrdinaryStepTypeFields = map[string]map[string]frozenTypeValidator{
		"agent": {
			"agent": frozenStringType, "function_id": frozenStringType,
			"prompt": frozenStringType, "prompt_file": frozenStringType,
			"system_prompt_file": frozenStringType, "context_files": frozenStringListType,
			"model": frozenStringType, "max_turns": frozenIntType,
			"budget_slice_usd": frozenNumberType, "output_schema": frozenStringType,
			"system_prompt": frozenStringType, "context": frozenStringType,
			"skills": frozenStringListType, "sidecars": frozenSidecarListType,
			"inputs": frozenStringListType, "outputs": frozenStringListType,
			"capabilities": frozenStringListType, "input_types": frozenSnapshotInputMapType,
			"output_types": frozenSnapshotOutputMapType, "env": frozenObjectType,
			"timeout": frozenStringType, "container_limits": frozenContainerLimitsType,
			"container_requests": frozenContainerLimitsType,
		},
		"harvest": {
			"harvest": frozenStringType, "workspace": frozenStringType,
			"repo": frozenStringType, "target_branch": frozenStringType,
			"ticket_id": frozenIntType, "pipeline_run_id": frozenIntType,
			"branch": frozenStringType, "push": frozenBoolType,
			"env": frozenObjectType, "dev_mcp": frozenSidecarType,
			"gate_policy": frozenGatePolicyType, "judge": frozenJudgeType,
			"timeout": frozenStringType,
		},
		"run": {
			"run": frozenStringType, "type": frozenStringType,
			"params": frozenObjectType, "privileged": frozenBoolType,
			"tags": frozenStringListType, "container_limits": frozenContainerLimitsType,
			"container_requests": frozenContainerLimitsType, "timeout": frozenStringType,
		},
		"task": {
			"task": frozenStringType, "function_id": frozenStringType,
			"privileged": frozenBoolType, "hermetic": frozenBoolType,
			"file": frozenStringType, "container_limits": frozenContainerLimitsType,
			"container_requests": frozenContainerLimitsType, "config": frozenTaskConfigType,
			"params": frozenObjectType, "vars": frozenObjectType,
			"tags": frozenStringListType, "input_mapping": frozenStringMapType,
			"output_mapping": frozenStringMapType, "input_types": frozenSnapshotInputMapType,
			"output_types": frozenSnapshotOutputMapType, "image": frozenStringType,
			"timeout": frozenStringType, "sidecars": frozenSidecarListType,
		},
		"put": {
			"put": frozenStringType, "resource": frozenStringType,
			"params": frozenObjectType, "inputs": frozenPutInputsType,
			"tags": frozenStringListType, "get_params": frozenObjectType,
			"timeout": frozenStringType, "no_get": frozenBoolType,
		},
		"get": {
			"get": frozenStringType, "resource": frozenStringType,
			"version": frozenVersionConfigType, "params": frozenObjectType,
			"passed": frozenStringListType, "trigger": frozenBoolType,
			"tags": frozenStringListType, "timeout": frozenStringType,
			"skip_download": frozenBoolType,
		},
		"set_pipeline": {
			"set_pipeline": frozenStringType, "file": frozenStringType,
			"team": frozenStringType, "vars": frozenObjectType,
			"var_files": frozenStringListType, "instance_vars": frozenObjectType,
		},
		"load_var": {
			"load_var": frozenStringType, "file": frozenStringType,
			"format": frozenStringType, "reveal": frozenBoolType,
		},
		"try":         {"try": frozenStepObjectType},
		"do":          {"do": frozenStepListType},
		"in_parallel": {"in_parallel": frozenInParallelType},
	}

	frozenOrdinaryWrapperTypeFields = map[string]frozenTypeValidator{
		"ensure": frozenStepObjectType, "on_error": frozenStepObjectType,
		"on_abort": frozenStepObjectType, "on_failure": frozenStepObjectType,
		"on_success": frozenStepObjectType, "across": frozenAcrossType,
		"attempts": frozenIntType, "timeout": frozenStringType,
	}
}

func validateFrozenStepTypes(step map[string]any, stepPath string) error {
	core := ""
	for _, name := range []string{
		"agent", "harvest", "run", "task", "put", "get",
		"set_pipeline", "load_var", "try", "do", "in_parallel",
	} {
		if _, found := step[name]; found {
			core = name
			break
		}
	}
	if fields := frozenOrdinaryStepTypeFields[core]; fields != nil {
		if err := validateFrozenTypedFields(step, stepPath, fields); err != nil {
			return err
		}
	}
	for name, validator := range frozenOrdinaryWrapperTypeFields {
		if value, found := step[name]; found {
			if err := validator(value, stepPath+"."+name); err != nil {
				return err
			}
		}
	}
	if value, found := step["fail_fast"]; found {
		if err := frozenBoolType(value, stepPath+".fail_fast"); err != nil {
			return err
		}
	}

	if nested, found := step["try"]; found {
		if err := validateFrozenTypedNestedStep(nested, stepPath+".try"); err != nil {
			return err
		}
	}
	if nested, found := step["do"]; found {
		if err := validateFrozenTypedStepList(nested, stepPath+".do"); err != nil {
			return err
		}
	}
	if nested, found := step["in_parallel"]; found {
		if err := validateFrozenTypedParallelSteps(nested, stepPath+".in_parallel"); err != nil {
			return err
		}
	}
	for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if nested, found := step[hook]; found {
			if err := validateFrozenTypedNestedStep(nested, stepPath+"."+hook); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFrozenTypedFields(value map[string]any, objectPath string, fields map[string]frozenTypeValidator) error {
	for name, validator := range fields {
		if field, found := value[name]; found {
			if err := validator(field, objectPath+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFrozenTypedObjectList(value any, listPath string, fields map[string]frozenTypeValidator) error {
	if value == nil {
		return nil
	}
	entries, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list", listPath)
	}
	for index, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s[%d] must be an object", listPath, index)
		}
		if err := validateFrozenTypedFields(entry, fmt.Sprintf("%s[%d]", listPath, index), fields); err != nil {
			return err
		}
	}
	return nil
}

func frozenAnyType(any, string) error { return nil }

func frozenStringType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(string); !ok {
		return fmt.Errorf("%s must be a string", fieldPath)
	}
	return nil
}

func frozenBoolType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("%s must be a boolean", fieldPath)
	}
	return nil
}

func frozenIntType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := frozenInt(value); !ok {
		return fmt.Errorf("%s must be an integer", fieldPath)
	}
	return nil
}

func frozenNumberType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := frozenFloat(value); !ok {
		return fmt.Errorf("%s must be a number", fieldPath)
	}
	return nil
}

func frozenObjectType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	return nil
}

func frozenStringListType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	entries, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list of strings", fieldPath)
	}
	for index, entry := range entries {
		if _, ok := entry.(string); !ok && entry != nil {
			return fmt.Errorf("%s[%d] must be a string", fieldPath, index)
		}
	}
	return nil
}

func frozenStringMapType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	entries, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	for key, entry := range entries {
		if _, ok := entry.(string); !ok && entry != nil {
			return fmt.Errorf("%s[%q] must be a string", fieldPath, key)
		}
	}
	return nil
}

func frozenCheckEveryType(value any, fieldPath string) error {
	if err := frozenStringType(value, fieldPath); err != nil || value == nil {
		return err
	}
	text := value.(string)
	if text == "" || text == "never" {
		return nil
	}
	if _, err := time.ParseDuration(text); err != nil {
		return fmt.Errorf("%s must be a duration or \"never\"", fieldPath)
	}
	return nil
}

func frozenVersionConfigType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(string); ok {
		return nil
	}
	return frozenStringMapType(value, fieldPath)
}

func frozenPutInputsType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(string); ok {
		return nil
	}
	return frozenStringListType(value, fieldPath)
}

func frozenContainerLimitsType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	limits, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	if cpu, found := limits["cpu"]; found && cpu != nil {
		if err := frozenNumberType(cpu, fieldPath+".cpu"); err != nil {
			return err
		}
	}
	for _, name := range []string{"memory", "ephemeral_storage"} {
		quantity, found := limits[name]
		if !found || quantity == nil {
			continue
		}
		if text, stringValue := quantity.(string); stringValue {
			if !frozenMemoryLimitPattern.MatchString(text) {
				return fmt.Errorf("%s.%s must be a memory quantity", fieldPath, name)
			}
			continue
		}
		if _, numeric := frozenFloat(quantity); numeric {
			continue
		}
		// The released custom unmarshaler had no default switch branch. Preserve
		// its acceptance of other JSON values rather than tightening history.
	}
	return nil
}

func frozenSnapshotInputMapType(value any, fieldPath string) error {
	_, err := frozenSnapshotInputs(value)
	if err != nil {
		return fmt.Errorf("%s: %w", fieldPath, err)
	}
	return nil
}

func frozenSnapshotOutputMapType(value any, fieldPath string) error {
	_, err := frozenSnapshotOutputs(value)
	if err != nil {
		return fmt.Errorf("%s: %w", fieldPath, err)
	}
	return nil
}

func frozenTaskConfigType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	config, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	if err := validateFrozenTypedFields(config, fieldPath, map[string]frozenTypeValidator{
		"platform": frozenStringType, "rootfs_uri": frozenStringType,
		"image_resource":     frozenImageResourceType,
		"container_limits":   frozenContainerLimitsType,
		"container_requests": frozenContainerLimitsType,
		"params":             frozenObjectType, "run": frozenTaskRunType,
		"inputs": frozenTaskInputsType, "outputs": frozenTaskOutputsType,
		"caches": frozenTaskPathsType, "scratch_paths": frozenTaskPathsType,
	}); err != nil {
		return err
	}
	return nil
}

func frozenImageResourceType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	image, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	return validateFrozenTypedFields(image, fieldPath, map[string]frozenTypeValidator{
		"name": frozenStringType, "type": frozenStringType,
		"source": frozenObjectType, "version": frozenStringMapType,
		"params": frozenObjectType, "tags": frozenStringListType,
	})
}

func frozenTaskRunType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	run, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	return validateFrozenTypedFields(run, fieldPath, map[string]frozenTypeValidator{
		"path": frozenStringType, "args": frozenStringListType,
		"dir": frozenStringType, "user": frozenStringType,
	})
}

func frozenTaskInputsType(value any, fieldPath string) error {
	return validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"name": frozenStringType, "path": frozenStringType, "optional": frozenBoolType,
	})
}

func frozenTaskOutputsType(value any, fieldPath string) error {
	return validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"name": frozenStringType, "path": frozenStringType,
	})
}

func frozenTaskPathsType(value any, fieldPath string) error {
	return validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"path": frozenStringType,
	})
}

func frozenSidecarListType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	entries, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list", fieldPath)
	}
	for index, entry := range entries {
		entryPath := fmt.Sprintf("%s[%d]", fieldPath, index)
		if err := frozenSidecarType(entry, entryPath); err != nil {
			return err
		}
	}
	return nil
}

func frozenSidecarType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(string); ok {
		return nil
	}
	sidecar, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a string or object", fieldPath)
	}
	if err := validateFrozenTypedFields(sidecar, fieldPath, map[string]frozenTypeValidator{
		"name": frozenStringType, "image": frozenStringType,
		"command": frozenStringListType, "args": frozenStringListType,
		"env": frozenSidecarEnvType, "ports": frozenSidecarPortsType,
		"resources": frozenSidecarResourcesType, "workingDir": frozenStringType,
		"image_artifact": frozenStringType,
	}); err != nil {
		return err
	}
	name, _ := sidecar["name"].(string)
	image, _ := sidecar["image"].(string)
	artifact, _ := sidecar["image_artifact"].(string)
	switch {
	case name == "":
		return fmt.Errorf("%s: invalid sidecar configuration: missing 'name'", fieldPath)
	case image == "" && artifact == "":
		return fmt.Errorf("%s: invalid sidecar configuration: missing 'image' or 'image_artifact'", fieldPath)
	case image != "" && artifact != "":
		return fmt.Errorf("%s: invalid sidecar configuration: cannot specify both 'image' and 'image_artifact'", fieldPath)
	case name == "main" || name == "artifact-helper":
		return fmt.Errorf("%s: reserved container name %q", fieldPath, name)
	}
	return nil
}

func frozenSidecarEnvType(value any, fieldPath string) error {
	return validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"name": frozenStringType, "value": frozenStringType,
	})
}

func frozenSidecarPortsType(value any, fieldPath string) error {
	if err := validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"containerPort": frozenIntType, "protocol": frozenStringType,
	}); err != nil {
		return err
	}
	entries, _ := value.([]any)
	for index, raw := range entries {
		entry, _ := raw.(map[string]any)
		protocol, _ := entry["protocol"].(string)
		switch protocol {
		case "", "TCP", "UDP", "SCTP":
		default:
			return fmt.Errorf("%s[%d].protocol is invalid", fieldPath, index)
		}
	}
	return nil
}

func frozenSidecarResourcesType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	resources, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	for _, name := range []string{"requests", "limits"} {
		raw, found := resources[name]
		if !found || raw == nil {
			continue
		}
		quantities, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.%s must be an object", fieldPath, name)
		}
		if err := validateFrozenTypedFields(quantities, fieldPath+"."+name, map[string]frozenTypeValidator{
			"cpu": frozenStringType, "memory": frozenStringType,
		}); err != nil {
			return err
		}
	}
	return nil
}

func frozenGatePolicyType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	policy, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	return validateFrozenTypedFields(policy, fieldPath, map[string]frozenTypeValidator{
		"gates": frozenGatesType, "on_gate_failure": frozenStringType,
	})
}

func frozenGatesType(value any, fieldPath string) error {
	return validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"gate": frozenStringType, "scope": frozenStringType,
		"focus": frozenStringType, "timeout": frozenStringType,
		"retries": frozenIntType,
	})
}

func frozenJudgeType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	judge, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	return validateFrozenTypedFields(judge, fieldPath, map[string]frozenTypeValidator{
		"rubric": frozenRubricType, "pass_threshold": frozenNumberType,
		"model": frozenStringType, "budget_usd": frozenNumberType,
	})
}

func frozenRubricType(value any, fieldPath string) error {
	return validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"name": frozenStringType, "weight": frozenNumberType, "guidance": frozenStringType,
	})
}

func frozenAcrossType(value any, fieldPath string) error {
	if err := validateFrozenTypedObjectList(value, fieldPath, map[string]frozenTypeValidator{
		"var": frozenStringType, "values": frozenAnyType, "max_in_flight": frozenMaxInFlightType,
	}); err != nil {
		return err
	}
	return nil
}

func frozenMaxInFlightType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		if text != "all" {
			return fmt.Errorf("%s must be \"all\" or an integer", fieldPath)
		}
		return nil
	}
	return frozenIntType(value, fieldPath)
}

func frozenStepObjectType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%s must be an object", fieldPath)
	}
	return nil
}

func frozenStepListType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.([]any); !ok {
		return fmt.Errorf("%s must be a list", fieldPath)
	}
	return validateFrozenTypedStepList(value, fieldPath)
}

func frozenInParallelType(value any, fieldPath string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.([]any); ok {
		return validateFrozenTypedStepList(value, fieldPath)
	}
	config, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a list or object", fieldPath)
	}
	return validateFrozenTypedFields(config, fieldPath, map[string]frozenTypeValidator{
		"steps": frozenStepListType, "limit": frozenIntType, "fail_fast": frozenBoolType,
	})
}

func validateFrozenTypedNestedStep(value any, stepPath string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", stepPath)
	}
	return validateFrozenStepTypes(step, stepPath)
}

func validateFrozenTypedStepList(value any, listPath string) error {
	steps, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list", listPath)
	}
	for index, raw := range steps {
		if err := validateFrozenTypedNestedStep(raw, fmt.Sprintf("%s[%d]", listPath, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateFrozenTypedParallelSteps(value any, parallelPath string) error {
	switch config := value.(type) {
	case []any:
		return validateFrozenTypedStepList(config, parallelPath)
	case map[string]any:
		if steps, found := config["steps"]; found {
			return validateFrozenTypedStepList(steps, parallelPath+".steps")
		}
	}
	return nil
}

type frozenOrdinaryDeclaration struct {
	name  string
	typ   string
	image string
}

type frozenOrdinaryValidator struct {
	resources     map[string]frozenOrdinaryDeclaration
	resourceTypes map[string]frozenOrdinaryDeclaration
	prototypes    map[string]frozenOrdinaryDeclaration
	usedResources map[string]struct{}
	seenGetNames  map[string]struct{}
}

func validateFrozenOrdinaryFunction(source *functionSource, plan []any) error {
	validator := &frozenOrdinaryValidator{
		resources:     make(map[string]frozenOrdinaryDeclaration),
		resourceTypes: make(map[string]frozenOrdinaryDeclaration),
		prototypes:    make(map[string]frozenOrdinaryDeclaration),
		usedResources: make(map[string]struct{}),
		seenGetNames:  make(map[string]struct{}),
	}
	if err := validator.decodeDeclarations(source); err != nil {
		return fmt.Errorf("workflow: invalid Concourse config: %w", err)
	}
	for index, value := range plan {
		step, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("workflow: invalid Concourse config: plan[%d] must be an object", index)
		}
		if err := validator.validateStep(step, fmt.Sprintf("jobs.entry.plan[%d]", index)); err != nil {
			return fmt.Errorf("workflow: invalid Concourse config: %w", err)
		}
	}
	for name := range validator.resources {
		if _, used := validator.usedResources[name]; !used {
			return fmt.Errorf("workflow: invalid Concourse config: resource %q is not used", name)
		}
	}
	return nil
}

func (validator *frozenOrdinaryValidator) decodeDeclarations(source *functionSource) error {
	resourceTypes, err := frozenDeclarationList(source.ResourceTypes, "resource_types")
	if err != nil {
		return err
	}
	for index, declaration := range resourceTypes {
		if declaration.name == "" {
			return fmt.Errorf("resource_types[%d] has no name", index)
		}
		raw := frozenObjectListEntry(source.ResourceTypes, index)
		image, _ := raw["image"].(string)
		if image != "" && declaration.typ != "" {
			return fmt.Errorf("resource_types.%s cannot specify both 'image' and 'type'", declaration.name)
		}
		if image == "" && declaration.typ == "" {
			return fmt.Errorf("resource_types.%s has no type", declaration.name)
		}
		if _, duplicate := validator.resourceTypes[declaration.name]; duplicate {
			return fmt.Errorf("duplicate resource type %q", declaration.name)
		}
		declaration.image, _ = raw["image"].(string)
		validator.resourceTypes[declaration.name] = declaration
	}

	prototypes, err := frozenDeclarationList(source.Prototypes, "prototypes")
	if err != nil {
		return err
	}
	for index, declaration := range prototypes {
		if declaration.name == "" {
			return fmt.Errorf("prototypes[%d] has no name", index)
		}
		if declaration.typ == "" {
			return fmt.Errorf("prototypes.%s has no type", declaration.name)
		}
		if _, duplicate := validator.prototypes[declaration.name]; duplicate {
			return fmt.Errorf("duplicate prototype %q", declaration.name)
		}
		if _, duplicate := validator.resourceTypes[declaration.name]; duplicate {
			return fmt.Errorf("resource type and prototype have the same name %q", declaration.name)
		}
		validator.prototypes[declaration.name] = declaration
	}

	resources, err := frozenDeclarationList(source.Resources, "resources")
	if err != nil {
		return err
	}
	for index, declaration := range resources {
		if declaration.name == "" {
			return fmt.Errorf("resources[%d] has no name", index)
		}
		if declaration.typ == "" {
			return fmt.Errorf("resources.%s has no type", declaration.name)
		}
		if _, duplicate := validator.resources[declaration.name]; duplicate {
			return fmt.Errorf("duplicate resource %q", declaration.name)
		}
		validator.resources[declaration.name] = declaration
	}

	if err := validateFrozenVarSources(source.VarSources); err != nil {
		return err
	}
	return nil
}

func frozenDeclarationList(value any, field string) ([]frozenOrdinaryDeclaration, error) {
	if value == nil {
		return nil, nil
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", field)
	}
	result := make([]frozenOrdinaryDeclaration, 0, len(entries))
	for index, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object", field, index)
		}
		name, _ := entry["name"].(string)
		typ, _ := entry["type"].(string)
		result = append(result, frozenOrdinaryDeclaration{name: name, typ: typ})
	}
	return result, nil
}

func frozenObjectListEntry(value any, index int) map[string]any {
	entries, _ := value.([]any)
	if index < 0 || index >= len(entries) {
		return nil
	}
	entry, _ := entries[index].(map[string]any)
	return entry
}

func validateFrozenVarSources(value any) error {
	if value == nil {
		return nil
	}
	entries, ok := value.([]any)
	if !ok {
		return fmt.Errorf("var_sources must be a list")
	}
	seen := make(map[string]struct{})
	names := make([]string, 0, len(entries))
	dependencies := make(map[string]map[string]struct{}, len(entries))
	for index, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("var_sources[%d] must be an object", index)
		}
		name, _ := entry["name"].(string)
		typ, _ := entry["type"].(string)
		if name == "" {
			return fmt.Errorf("var_sources[%d] has no name", index)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate variable source %q", name)
		}
		seen[name] = struct{}{}
		switch typ {
		case "vault", "dummy", "ssm", "secretsmanager", "idtoken":
		default:
			return fmt.Errorf("unknown credential manager type: %s", typ)
		}
		if typ == "dummy" {
			config, ok := entry["config"].(map[string]any)
			if !ok {
				return fmt.Errorf("failed to create credential manager %s: invalid dummy credential manager config", name)
			}
			if _, ok := config["vars"].(map[string]any); !ok {
				return fmt.Errorf("failed to create credential manager %s: invalid vars config", name)
			}
		}
		names = append(names, name)
		encoded, err := json.Marshal(entry["config"])
		if err != nil {
			return fmt.Errorf("variable source %s config: %w", name, err)
		}
		dependencies[name] = make(map[string]struct{})
		for _, match := range frozenSourcedVarPattern.FindAllStringSubmatch(string(encoded), -1) {
			if len(match) > 1 {
				dependencies[name][match[1]] = struct{}{}
			}
		}
	}
	resolved := make(map[string]struct{}, len(names))
	for {
		progress := false
		for _, name := range names {
			if _, done := resolved[name]; done {
				continue
			}
			ready := true
			for dependency := range dependencies[name] {
				if _, found := resolved[dependency]; !found {
					ready = false
					break
				}
			}
			if ready {
				resolved[name] = struct{}{}
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	if len(resolved) != len(names) {
		pending := make([]string, 0, len(names)-len(resolved))
		for _, name := range names {
			if _, found := resolved[name]; !found {
				pending = append(pending, name)
			}
		}
		return fmt.Errorf("could not resolve inter-dependent var sources: %s", strings.Join(pending, ", "))
	}
	return nil
}

func (validator *frozenOrdinaryValidator) validateStep(step map[string]any, path string) error {
	core := ""
	for _, name := range []string{
		"agent", "harvest", "run", "task", "put", "get",
		"set_pipeline", "load_var", "try", "do", "in_parallel",
	} {
		if _, found := step[name]; found {
			core = name
			break
		}
	}
	if attempts, found := step["attempts"]; found {
		value, ok := frozenInt(attempts)
		if !ok || value <= 0 {
			return fmt.Errorf("%s.attempts must be greater than 0", path)
		}
	}
	if timeout, found := step["timeout"]; found && frozenTimeoutIsWrapper(core) {
		text, ok := timeout.(string)
		if !ok {
			return fmt.Errorf("%s.timeout must be a duration string", path)
		}
		if _, err := time.ParseDuration(text); err != nil {
			return fmt.Errorf("%s.timeout invalid duration %q", path, text)
		}
	}
	if across, found := step["across"]; found {
		entries, ok := across.([]any)
		if !ok || len(entries) == 0 {
			return fmt.Errorf("%s.across has no vars specified", path)
		}
		for index, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.across[%d] must be an object", path, index)
			}
			name, _ := entry["var"].(string)
			if name == "" {
				return fmt.Errorf("%s.across[%d].var is required", path, index)
			}
			if rawLimit, found := entry["max_in_flight"]; found {
				switch limit := rawLimit.(type) {
				case string:
					if limit != "all" {
						return fmt.Errorf("%s.across[%d].max_in_flight is invalid", path, index)
					}
				default:
					numeric, ok := frozenInt(rawLimit)
					if !ok || numeric <= 0 {
						return fmt.Errorf("%s.across[%d].max_in_flight must be greater than 0", path, index)
					}
				}
			}
		}
	}

	switch core {
	case "agent":
		if err := validator.validateAgent(step, path); err != nil {
			return err
		}
	case "task":
		if err := validator.validateTask(step, path); err != nil {
			return err
		}
	case "get", "put":
		name, _ := step[core].(string)
		if name == "" {
			return fmt.Errorf("%s.%s has an empty name", path, core)
		}
		resource, _ := step["resource"].(string)
		if resource == "" {
			resource = name
		}
		if _, found := validator.resources[resource]; !found {
			return fmt.Errorf("%s.%s(%s): unknown resource %q", path, core, name, resource)
		}
		validator.usedResources[resource] = struct{}{}
		if core == "get" {
			if skip, _ := step["skip_download"].(bool); skip {
				resourceType := validator.resources[resource].typ
				isImage := resourceType == "registry-image"
				if declaration, found := validator.resourceTypes[resourceType]; found {
					isImage = isImage || declaration.image != ""
				}
				if !isImage {
					return fmt.Errorf("%s.get(%s): skip_download is only valid for image resources", path, name)
				}
			}
			if _, duplicate := validator.seenGetNames[name]; duplicate {
				return fmt.Errorf("%s.get(%s): repeated name", path, name)
			}
			validator.seenGetNames[name] = struct{}{}
			for _, jobGlob := range stringList(step["passed"]) {
				matched, _ := pathpkg.Match(jobGlob, "entry")
				if !matched {
					return fmt.Errorf("%s.get(%s).passed: no matching job(s) for %q", path, name, jobGlob)
				}
			}
		}
	case "run":
		message, _ := step["run"].(string)
		typ, _ := step["type"].(string)
		if message == "" {
			return fmt.Errorf("%s.run has an empty message", path)
		}
		if !frozenIdentifierPattern.MatchString(message) || frozenNumericIdentifier.MatchString(message) {
			return fmt.Errorf("%s.run(%s): invalid identifier", path, message)
		}
		if _, found := validator.prototypes[typ]; !found {
			return fmt.Errorf("%s.run(%s): unknown prototype %q", path, message, typ)
		}
	case "harvest":
		if err := validateFrozenHarvest(step, path); err != nil {
			return err
		}
	case "set_pipeline":
		if name, _ := step["set_pipeline"].(string); name == "" {
			return fmt.Errorf("%s.set_pipeline has an empty name", path)
		}
		if file, _ := step["file"].(string); file == "" {
			return fmt.Errorf("%s.set_pipeline: no file specified", path)
		}
	case "load_var":
		if name, _ := step["load_var"].(string); name == "" {
			return fmt.Errorf("%s.load_var has an empty name", path)
		}
		if file, _ := step["file"].(string); file == "" {
			return fmt.Errorf("%s.load_var: no file specified", path)
		}
	case "try":
		if err := validator.validateNestedStep(step["try"], path+".try"); err != nil {
			return err
		}
	case "do":
		if err := validator.validateStepList(step["do"], path+".do"); err != nil {
			return err
		}
	case "in_parallel":
		steps, err := frozenParallelSteps(step["in_parallel"])
		if err != nil {
			return fmt.Errorf("%s.in_parallel: %w", path, err)
		}
		for index, raw := range steps {
			nested, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.in_parallel.steps[%d] must be an object", path, index)
			}
			if err := validator.validateStep(nested, fmt.Sprintf("%s.in_parallel.steps[%d]", path, index)); err != nil {
				return err
			}
		}
	}

	for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if nested, found := step[hook]; found {
			if err := validator.validateNestedStep(nested, path+"."+hook); err != nil {
				return err
			}
		}
	}
	return nil
}

func (validator *frozenOrdinaryValidator) validateNestedStep(value any, path string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", path)
	}
	return validator.validateStep(step, path)
}

func (validator *frozenOrdinaryValidator) validateStepList(value any, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list", path)
	}
	for index, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s[%d] must be an object", path, index)
		}
		if err := validator.validateStep(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func (validator *frozenOrdinaryValidator) validateAgent(step map[string]any, path string) error {
	name, _ := step["agent"].(string)
	if name == "" {
		return fmt.Errorf("%s.agent has an empty name", path)
	}
	prompt, _ := step["prompt"].(string)
	promptFile, _ := step["prompt_file"].(string)
	if prompt == "" && promptFile == "" {
		return fmt.Errorf("%s.agent(%s): must specify either `prompt:` or `prompt_file:`", path, name)
	}
	if prompt != "" && promptFile != "" {
		return fmt.Errorf("%s.agent(%s): must specify one of `prompt:` or `prompt_file:`, not both", path, name)
	}
	if budget, found := frozenFloat(step["budget_slice_usd"]); found && budget < 0 {
		return fmt.Errorf("%s.agent(%s): budget_slice_usd must not be negative", path, name)
	}
	if turns, found := frozenInt(step["max_turns"]); found && turns < 0 {
		return fmt.Errorf("%s.agent(%s): max_turns must not be negative", path, name)
	}
	inputs := stringList(step["inputs"])
	outputs := stringList(step["outputs"])
	typedInputs, err := frozenSnapshotInputs(step["input_types"])
	if err != nil {
		return err
	}
	typedOutputs, err := frozenSnapshotOutputs(step["output_types"])
	if err != nil {
		return err
	}
	if err := validateFrozenSnapshotMembership(typedInputs, inputs, "input_types", "declared agent input"); err != nil {
		return fmt.Errorf("%s.agent(%s): %w", path, name, err)
	}
	if err := validateFrozenSnapshotMembership(typedOutputs, outputs, "output_types", "declared agent output"); err != nil {
		return fmt.Errorf("%s.agent(%s): %w", path, name, err)
	}
	seenOutputs := make(map[string]struct{})
	seenEnv := make(map[string]string)
	for _, output := range outputs {
		if _, duplicate := seenOutputs[output]; duplicate {
			return fmt.Errorf("%s.agent(%s): duplicate agent output %q", path, name, output)
		}
		seenOutputs[output] = struct{}{}
		if output == "flight" {
			return fmt.Errorf("%s.agent(%s): output name 'flight' is reserved for the flight recorder", path, name)
		}
		if !agentOutputNamePattern.MatchString(output) {
			return fmt.Errorf("%s.agent(%s): output name %q must contain only letters, digits, '-' and '_'", path, name, output)
		}
		mangled := strings.ToUpper(strings.ReplaceAll(output, "-", "_"))
		if mangled == "SCHEMA" {
			return fmt.Errorf("%s.agent(%s): output name %q collides with the reserved AGENT_OUTPUT_SCHEMA env var", path, name, output)
		}
		if previous, duplicate := seenEnv[mangled]; duplicate {
			return fmt.Errorf("%s.agent(%s): output names %q and %q collide after AGENT_OUTPUT env-name mangling", path, name, previous, output)
		}
		seenEnv[mangled] = output
	}
	seenSidecars := make(map[string]struct{})
	if sidecars, ok := step["sidecars"].([]any); ok {
		for _, raw := range sidecars {
			sidecar, inline := raw.(map[string]any)
			if !inline {
				continue
			}
			sidecarName, _ := sidecar["name"].(string)
			if _, duplicate := seenSidecars[sidecarName]; duplicate {
				return fmt.Errorf("%s.agent(%s): duplicate sidecar name %q", path, name, sidecarName)
			}
			seenSidecars[sidecarName] = struct{}{}
		}
	}
	if env, ok := step["env"].(map[string]any); ok {
		for envName, raw := range env {
			value, isString := raw.(string)
			if !isString {
				continue
			}
			if match := frozenSourcedVarPattern.FindStringSubmatch(value); len(match) > 1 {
				return fmt.Errorf("%s.agent(%s): env %s references var source %q: agent env is static-only", path, name, envName, match[1])
			}
		}
	}
	return nil
}

func frozenTimeoutIsWrapper(core string) bool {
	switch core {
	case "set_pipeline", "load_var", "try", "do", "in_parallel":
		return true
	default:
		return false
	}
}

func (validator *frozenOrdinaryValidator) validateTask(step map[string]any, path string) error {
	name, _ := step["task"].(string)
	if name == "" {
		return fmt.Errorf("%s.task has an empty name", path)
	}
	config, hasConfig := step["config"].(map[string]any)
	file, _ := step["file"].(string)
	if !hasConfig && file == "" {
		return fmt.Errorf("%s.task(%s): must specify either `file:` or `config:`", path, name)
	}
	if hasConfig && file != "" {
		return fmt.Errorf("%s.task(%s): must specify one of `file:` or `config:`, not both", path, name)
	}
	if hasConfig {
		platform, _ := config["platform"].(string)
		if platform == "" {
			return fmt.Errorf("%s.task(%s).config: missing 'platform'", path, name)
		}
		for _, field := range []string{"inputs", "outputs"} {
			entries, _ := config[field].([]any)
			for index, raw := range entries {
				entry, _ := raw.(map[string]any)
				if itemName, _ := entry["name"].(string); itemName == "" {
					return fmt.Errorf("%s.task(%s).config.%s[%d] is missing a name", path, name, field, index)
				}
			}
		}
		inputs, outputs := frozenTaskArtifactNames(step)
		typedInputs, err := frozenSnapshotInputs(step["input_types"])
		if err != nil {
			return err
		}
		typedOutputs, err := frozenSnapshotOutputs(step["output_types"])
		if err != nil {
			return err
		}
		if err := validateFrozenSnapshotMembership(typedInputs, inputs, "input_types", "effective task input"); err != nil {
			return fmt.Errorf("%s.task(%s): %w", path, name, err)
		}
		if err := validateFrozenSnapshotMembership(typedOutputs, outputs, "output_types", "effective task output"); err != nil {
			return fmt.Errorf("%s.task(%s): %w", path, name, err)
		}
		if len(typedOutputs) > 0 {
			seen := make(map[string]struct{}, len(outputs))
			for _, output := range outputs {
				if _, duplicate := seen[output]; duplicate {
					return fmt.Errorf("%s.task(%s): duplicate effective task output %q", path, name, output)
				}
				seen[output] = struct{}{}
			}
		}
	}
	return nil
}

func validateFrozenSnapshotMembership(
	declarations map[string]frozenSnapshotDeclaration,
	members []string,
	field string,
	memberKind string,
) error {
	set := make(map[string]struct{}, len(members))
	for _, member := range members {
		set[member] = struct{}{}
	}
	for name := range declarations {
		if name == "" {
			return fmt.Errorf("%s key must not be empty", field)
		}
		if _, found := set[name]; !found {
			article := "a"
			if strings.HasPrefix(memberKind, "effective ") {
				article = "an"
			}
			return fmt.Errorf("%s[%q] does not name %s %s", field, name, article, memberKind)
		}
	}
	return nil
}

func validateFrozenHarvest(step map[string]any, path string) error {
	name, _ := step["harvest"].(string)
	if name == "" {
		return fmt.Errorf("%s.harvest has an empty name", path)
	}
	if workspace, _ := step["workspace"].(string); workspace == "" {
		return fmt.Errorf("%s.harvest(%s): must specify `workspace:`", path, name)
	}
	if repo, _ := step["repo"].(string); repo == "" {
		return fmt.Errorf("%s.harvest(%s): must specify `repo:`", path, name)
	}
	push, _ := step["push"].(bool)
	branch, _ := step["branch"].(string)
	if push && branch == "" {
		return fmt.Errorf("%s.harvest(%s): `push: true` requires `branch:`", path, name)
	}
	policy, _ := step["gate_policy"].(map[string]any)
	gates, _ := policy["gates"].([]any)
	if len(gates) > 0 {
		if _, found := step["dev_mcp"]; !found {
			return fmt.Errorf("%s.harvest(%s): gates require `dev_mcp:`", path, name)
		}
	}
	return nil
}

func frozenInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		integer := int(number)
		return integer, float64(integer) == number
	default:
		return 0, false
	}
}

func frozenFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}
