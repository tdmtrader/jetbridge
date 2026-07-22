package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const (
	// MaxCompiledAssetBytes bounds the literal implementation data expanded
	// into one version-3 function. A small source manifest can reference the
	// same asset from many nodes, so the source-manifest bound alone is not an
	// execution-size bound.
	MaxCompiledAssetBytes = MaxManifestBytes
	// MaxCompiledSkillBytes matches the downstream skill materializer's
	// immutable payload limit. Selected skill files are counted as one union,
	// regardless of how many agents select the same tree.
	MaxCompiledSkillBytes = 512 << 10
)

// Compile validates a source manifest and compiles it into a
// self-contained Config (design 2026-07-17 §3): workflow.yml is parsed
// and grammar-validated, prompt_files / system_prompt_file references
// are inlined, context paths are resolved into ContextFiles, and every
// referenced skill's tree is collected into SkillFiles. The render path
// consumes only the compiled Config — never the manifest. Unreferenced
// files (a README, notes) are allowed: they are source, they are
// hashed, and an edit to them correctly mints a new version.
func Compile(m Manifest) (*Config, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, ok := m["workflow.yml"]
	if !ok {
		return nil, fmt.Errorf("workflow: manifest has no workflow.yml")
	}
	cfg, err := Parse([]byte(raw))
	if err != nil {
		return nil, err
	}
	return compileLegacy(m, cfg)
}

// CompileDefinition compiles either legacy schema 1/2 source or a schema 3
// function into Task 1's tagged representation. Compile remains the legacy
// compatibility entry point for existing ticket-oriented callers.
func CompileDefinition(m Manifest) (*CompiledDefinition, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, ok := m["workflow.yml"]
	if !ok {
		return nil, fmt.Errorf("workflow: manifest has no workflow.yml")
	}
	definition, err := ParseCompiled([]byte(raw))
	if err != nil {
		return nil, err
	}
	if definition.Legacy != nil {
		legacy, err := compileLegacy(m, definition.Legacy)
		if err != nil {
			return nil, err
		}
		definition.Legacy = legacy
		return definition, nil
	}
	if err := compileFunctionAssets(m, definition); err != nil {
		return nil, err
	}
	return definition, nil
}

func compileLegacy(m Manifest, cfg *Config) (*Config, error) {

	for key, path := range cfg.PromptFiles {
		content, ok := m[path]
		if !ok {
			return nil, fmt.Errorf("workflow: prompt_files %q: %q is not in the manifest", key, path)
		}
		if err := validatePromptTemplate(key, content); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if cfg.Prompts == nil {
			cfg.Prompts = map[string]string{}
		}
		cfg.Prompts[key] = content
	}

	if cfg.SystemPromptFile != "" {
		content, ok := m[cfg.SystemPromptFile]
		if !ok {
			return nil, fmt.Errorf("workflow: system_prompt_file %q is not in the manifest", cfg.SystemPromptFile)
		}
		cfg.SystemPrompt = content
	}

	skillNames := append([]string{}, cfg.Skills...)
	contextPaths := append([]string{}, cfg.Context...)
	for i := range cfg.Steps {
		s := &cfg.Steps[i]
		if s.SystemPromptFile != "" {
			content, ok := m[s.SystemPromptFile]
			if !ok {
				return nil, fmt.Errorf("workflow: agent step %q: system_prompt_file %q is not in the manifest", s.Agent, s.SystemPromptFile)
			}
			s.SystemPrompt = content
		}
		skillNames = append(skillNames, s.Skills...)
		contextPaths = append(contextPaths, s.Context...)
	}

	for _, path := range contextPaths {
		content, ok := m[path]
		if !ok {
			return nil, fmt.Errorf("workflow: context file %q is not in the manifest", path)
		}
		if cfg.ContextFiles == nil {
			cfg.ContextFiles = map[string]string{}
		}
		cfg.ContextFiles[path] = content
	}

	for _, name := range skillNames {
		prefix := "skills/" + name + "/"
		if _, ok := m[prefix+"SKILL.md"]; !ok {
			return nil, fmt.Errorf("workflow: skill %q: %sSKILL.md is not in the manifest", name, prefix)
		}
		for path, content := range m {
			if strings.HasPrefix(path, prefix) {
				if cfg.SkillFiles == nil {
					cfg.SkillFiles = map[string]string{}
				}
				cfg.SkillFiles[path] = content
			}
		}
	}

	return cfg, nil
}

func compileFunctionAssets(m Manifest, definition *CompiledDefinition) error {
	compiler, err := newFunctionAssetCompiler(m, definition.Function)
	if err != nil {
		return err
	}
	if err := compiler.preflight(); err != nil {
		return err
	}
	return compiler.compile()
}

type manifestAsset struct {
	path    string
	content string
}

type preparedAgentAssets struct {
	prompt          string
	systemPrompt    string
	context         []manifestAsset
	contextBytes    int
	capabilityNames []string
}

type functionAssetCompiler struct {
	manifest           Manifest
	function           *FunctionConfig
	manifestPaths      []string
	skillTrees         map[string][]manifestAsset
	capabilityBytes    map[string]int
	preparedAgents     map[*atc.AgentStep]preparedAgentAssets
	selectedSkillPaths map[string]struct{}
	selectedSkillFiles []manifestAsset
	compiledAssetBytes int
	compiledSkillBytes int
}

func newFunctionAssetCompiler(m Manifest, function *FunctionConfig) (*functionAssetCompiler, error) {
	if err := validateCapabilityCatalog(function.Capabilities); err != nil {
		return nil, err
	}
	compiler := &functionAssetCompiler{
		manifest:           m,
		function:           function,
		manifestPaths:      m.Paths(),
		skillTrees:         make(map[string][]manifestAsset),
		capabilityBytes:    make(map[string]int, len(function.Capabilities)),
		preparedAgents:     make(map[*atc.AgentStep]preparedAgentAssets),
		selectedSkillPaths: make(map[string]struct{}),
	}
	for _, name := range sortedCapabilityNames(function.Capabilities) {
		canonical, err := json.Marshal(function.Capabilities[name].Sidecar)
		if err != nil {
			return nil, fmt.Errorf("workflow: capability %q: canonicalize sidecar: %w", name, err)
		}
		compiler.capabilityBytes[name] = len(canonical)
	}
	return compiler, nil
}

func (compiler *functionAssetCompiler) preflight() error {
	for index := range compiler.function.Plan {
		err := compiler.function.Plan[index].Config.Visit(atc.StepRecursor{
			OnAgent: compiler.preflightAgent,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (compiler *functionAssetCompiler) preflightAgent(step *atc.AgentStep) error {
	identity := agentCompileIdentity(step)
	if len(step.Sidecars) > 0 {
		return fmt.Errorf("workflow: %s: direct sidecars are not allowed in version 3; declare a named capability", identity)
	}

	prepared := preparedAgentAssets{}
	if step.Prompt != "" && step.PromptFile != "" {
		return fmt.Errorf("workflow: %s: prompt and prompt_file are mutually exclusive", identity)
	}
	prepared.prompt = step.Prompt
	if step.PromptFile != "" {
		content, err := resolveManifestFile(compiler.manifest, step.PromptFile)
		if err != nil {
			return fmt.Errorf("workflow: %s: prompt_file %q: %w", identity, step.PromptFile, err)
		}
		prepared.prompt = content
	}
	if prepared.prompt == "" {
		return fmt.Errorf("workflow: %s: prompt is required", identity)
	}
	if err := compiler.addCompiledAssetBytes(len(prepared.prompt), identity, "prompt"); err != nil {
		return err
	}

	if step.SystemPrompt != "" && step.SystemPromptFile != "" {
		return fmt.Errorf("workflow: %s: system_prompt and system_prompt_file are mutually exclusive", identity)
	}
	prepared.systemPrompt = step.SystemPrompt
	if step.SystemPromptFile != "" {
		content, err := resolveManifestFile(compiler.manifest, step.SystemPromptFile)
		if err != nil {
			return fmt.Errorf("workflow: %s: system_prompt_file %q: %w", identity, step.SystemPromptFile, err)
		}
		prepared.systemPrompt = content
	}
	if err := compiler.addCompiledAssetBytes(len(prepared.systemPrompt), identity, "system_prompt"); err != nil {
		return err
	}

	if step.Context != "" {
		return fmt.Errorf("workflow: %s: context is compiled-only; use context_files", identity)
	}
	seenContext := make(map[string]struct{}, len(step.ContextFiles))
	for _, path := range step.ContextFiles {
		if _, duplicate := seenContext[path]; duplicate {
			continue
		}
		content, err := resolveManifestFile(compiler.manifest, path)
		if err != nil {
			return fmt.Errorf("workflow: %s: context_file %q: %w", identity, path, err)
		}
		seenContext[path] = struct{}{}
		asset := manifestAsset{path: path, content: content}
		prepared.context = append(prepared.context, asset)
		for _, partBytes := range []int{len("## "), len(path), len("\n\n"), len(content), len("\n\n")} {
			if err := compiler.addCompiledAssetBytes(partBytes, identity, "context"); err != nil {
				return err
			}
			prepared.contextBytes += partBytes
		}
	}

	seenSkills := make(map[string]struct{}, len(step.Skills))
	for _, name := range step.Skills {
		if err := validateSkillName(name); err != nil {
			return fmt.Errorf("workflow: %s: skill %q: %w", identity, name, err)
		}
		if _, duplicate := seenSkills[name]; duplicate {
			return fmt.Errorf("workflow: %s: duplicate skill %q", identity, name)
		}
		seenSkills[name] = struct{}{}
		tree, err := compiler.skillTree(name)
		if err != nil {
			return fmt.Errorf("workflow: %s: skill %q: %w", identity, name, err)
		}
		for _, asset := range tree {
			if _, selected := compiler.selectedSkillPaths[asset.path]; selected {
				continue
			}
			if err := compiler.addCompiledSkillBytes(len(asset.content), identity, asset.path); err != nil {
				return err
			}
			if err := compiler.addCompiledAssetBytes(len(asset.content), identity, "skill "+asset.path); err != nil {
				return err
			}
			compiler.selectedSkillPaths[asset.path] = struct{}{}
			compiler.selectedSkillFiles = append(compiler.selectedSkillFiles, asset)
		}
	}

	seenCapabilities := make(map[string]struct{}, len(step.Capabilities))
	for _, name := range step.Capabilities {
		if _, duplicate := seenCapabilities[name]; duplicate {
			return fmt.Errorf("workflow: %s: duplicate capability reference %q", identity, name)
		}
		seenCapabilities[name] = struct{}{}
		if _, found := compiler.function.Capabilities[name]; !found {
			return fmt.Errorf("workflow: %s: unknown capability %q", identity, name)
		}
		if err := compiler.addCompiledAssetBytes(compiler.capabilityBytes[name], identity, "capability "+name); err != nil {
			return err
		}
		prepared.capabilityNames = append(prepared.capabilityNames, name)
	}

	compiler.preparedAgents[step] = prepared
	return nil
}

func (compiler *functionAssetCompiler) skillTree(name string) ([]manifestAsset, error) {
	if tree, found := compiler.skillTrees[name]; found {
		return tree, nil
	}
	prefix := "skills/" + name + "/"
	root := prefix + "SKILL.md"
	if _, err := resolveManifestFile(compiler.manifest, root); err != nil {
		return nil, err
	}
	tree := make([]manifestAsset, 0)
	for _, path := range compiler.manifestPaths {
		if strings.HasPrefix(path, prefix) {
			tree = append(tree, manifestAsset{path: path, content: compiler.manifest[path]})
		}
	}
	compiler.skillTrees[name] = tree
	return tree, nil
}

func (compiler *functionAssetCompiler) addCompiledAssetBytes(amount int, identity, asset string) error {
	if amount > MaxCompiledAssetBytes-compiler.compiledAssetBytes {
		return fmt.Errorf("workflow: %s: compiled assets exceed %d bytes while adding %s", identity, MaxCompiledAssetBytes, asset)
	}
	compiler.compiledAssetBytes += amount
	return nil
}

func (compiler *functionAssetCompiler) addCompiledSkillBytes(amount int, identity, path string) error {
	if amount > MaxCompiledSkillBytes-compiler.compiledSkillBytes {
		return fmt.Errorf("workflow: %s: compiled skills exceed %d bytes while adding %q", identity, MaxCompiledSkillBytes, path)
	}
	compiler.compiledSkillBytes += amount
	return nil
}

func (compiler *functionAssetCompiler) compile() error {
	if len(compiler.selectedSkillFiles) > 0 {
		compiler.function.SkillFiles = make(map[string]string, len(compiler.selectedSkillFiles))
		for _, asset := range compiler.selectedSkillFiles {
			compiler.function.SkillFiles[asset.path] = asset.content
		}
	}
	for index := range compiler.function.Plan {
		err := compiler.function.Plan[index].Config.Visit(atc.StepRecursor{
			OnAgent: compiler.compileAgent,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (compiler *functionAssetCompiler) compileAgent(step *atc.AgentStep) error {
	prepared, found := compiler.preparedAgents[step]
	if !found {
		return fmt.Errorf("workflow: %s: internal error: agent assets were not preflighted", agentCompileIdentity(step))
	}
	step.Prompt = prepared.prompt
	step.PromptFile = ""
	step.SystemPrompt = prepared.systemPrompt
	step.SystemPromptFile = ""
	if len(prepared.context) > 0 {
		var context strings.Builder
		context.Grow(prepared.contextBytes)
		for _, asset := range prepared.context {
			fmt.Fprintf(&context, "## %s\n\n%s\n\n", asset.path, asset.content)
		}
		step.Context = context.String()
	}
	step.ContextFiles = nil
	for _, name := range prepared.capabilityNames {
		copy := cloneSidecarConfig(compiler.function.Capabilities[name].Sidecar)
		step.Sidecars = append(step.Sidecars, atc.SidecarSource{Config: &copy})
	}
	step.Capabilities = nil
	return nil
}

func sortedCapabilityNames(catalog map[string]Capability) []string {
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateCapabilityCatalog(catalog map[string]Capability) error {
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	seenSidecars := make(map[string]string, len(catalog))
	for _, key := range keys {
		if key == "" {
			return fmt.Errorf("workflow: capability name is required")
		}
		capability := catalog[key]
		if _, err := snapshot.ParseTypeRef(capability.Contract); err != nil {
			return fmt.Errorf("workflow: capability %q: invalid contract: %w", key, err)
		}
		if err := capability.Sidecar.ValidateCapability(); err != nil {
			return fmt.Errorf("workflow: capability %q: %w", key, err)
		}
		if previous, duplicate := seenSidecars[capability.Sidecar.Name]; duplicate {
			return fmt.Errorf("workflow: capability %q: sidecar name %q is also declared by capability %q", key, capability.Sidecar.Name, previous)
		}
		seenSidecars[capability.Sidecar.Name] = key
	}
	return nil
}

func cloneSidecarConfig(source atc.SidecarConfig) atc.SidecarConfig {
	copy := source
	copy.Command = append([]string(nil), source.Command...)
	copy.Args = append([]string(nil), source.Args...)
	copy.Env = append([]atc.SidecarEnvVar(nil), source.Env...)
	copy.Ports = append([]atc.SidecarPort(nil), source.Ports...)
	if source.Resources != nil {
		resources := *source.Resources
		copy.Resources = &resources
	}
	return copy
}

func validateSkillName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("name is required")
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("name must not be dot-prefixed")
	case strings.ContainsAny(name, `/\\`):
		return fmt.Errorf("name must be a bare directory name")
	default:
		return nil
	}
}

func agentCompileIdentity(step *atc.AgentStep) string {
	if step.FunctionID != "" {
		return fmt.Sprintf("agent %q (function_id %q)", step.Name, step.FunctionID)
	}
	return fmt.Sprintf("agent %q", step.Name)
}
