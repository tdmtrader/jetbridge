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

// CompileDefinition compiles a schema-version-3 source manifest into a
// self-contained function definition.
func CompileDefinition(m Manifest) (*CompiledDefinition, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, ok := m.DefinitionSource()
	if !ok {
		return nil, fmt.Errorf("workflow: manifest has no %s (or legacy %s)", WorkflowFileName, LegacyWorkflowFileName)
	}
	if err := RequireSchemaVersion3([]byte(raw)); err != nil {
		return nil, err
	}
	definition, validationProfiles, err := parseFunctionDefinitionSource([]byte(raw))
	if err != nil {
		return nil, err
	}
	if err := compileFunctionAssets(m, definition, validationProfiles); err != nil {
		return nil, err
	}
	// Source compilation is intentionally independent of the durable workflow
	// definition ID. Static flow and ordinary Concourse semantics are complete
	// here; AnnotatePublicOutputs attaches workflow-retention metadata after the
	// definition has been allocated by persistence.
	if _, err := ValidateFunction(definition.Function); err != nil {
		return nil, err
	}
	return definition, nil
}

func compileFunctionAssets(m Manifest, definition *CompiledDefinition, validationProfiles []DevValidationProfile) error {
	return compileFunctionAssetsWithFrozenNodes(m, definition, validationProfiles, nil)
}

func compileFunctionAssetsWithFrozenNodes(m Manifest, definition *CompiledDefinition, validationProfiles []DevValidationProfile, frozen map[string]frozenNodeAssets) error {
	if err := validatePreexistingCompiledSkillFiles(definition.Function.SkillFiles, frozen); err != nil {
		return err
	}
	compiler, err := newFunctionAssetCompiler(m, definition.Function, validationProfiles)
	if err != nil {
		return err
	}
	compiler.frozenNodeAssets = frozen
	for path, content := range definition.Function.SkillFiles {
		if err := compiler.addCompiledSkillBytes(len(content), "frozen node", path); err != nil {
			return err
		}
		if err := compiler.addCompiledAssetBytes(len(content), "frozen node", "skill "+path); err != nil {
			return err
		}
	}
	for _, assets := range frozen {
		for _, profile := range assets.profiles {
			if err := compiler.addCompiledAssetBytes(len(profile.Profile), "frozen node", "dev validation profile"); err != nil {
				return err
			}
			if err := compiler.addCompiledAssetBytes(len(profile.ProtectedConfig), "frozen node", "dev validation config"); err != nil {
				return err
			}
		}
	}
	if err := compiler.preflight(); err != nil {
		return err
	}
	return compiler.compile()
}

// validatePreexistingCompiledSkillFiles admits only node-owned bytes before
// ordinary source compilation starts. Direct source has no legitimate
// compiled skill authority; node expansion may carry precisely its released
// frozen union and nothing else.
func validatePreexistingCompiledSkillFiles(files map[string]string, frozen map[string]frozenNodeAssets) error {
	if frozen == nil {
		if len(files) > 0 {
			return fmt.Errorf("workflow: compiled skill_files are server-owned")
		}
		return nil
	}
	expected := map[string]string{}
	for _, assets := range frozen {
		for path, content := range assets.skillFiles {
			if prior, found := expected[path]; found && prior != content {
				return fmt.Errorf("workflow: frozen node compiled skill file %q conflicts", path)
			}
			expected[path] = content
		}
	}
	if len(files) != len(expected) {
		return fmt.Errorf("workflow: compiled skill_files are server-owned")
	}
	for path, content := range expected {
		if actual, found := files[path]; !found || actual != content {
			return fmt.Errorf("workflow: compiled skill_files are server-owned")
		}
	}
	return nil
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
	capabilityURLs  map[string]string
	skillFiles      map[string]string
}

type functionAssetCompiler struct {
	manifest                      Manifest
	function                      *FunctionConfig
	manifestPaths                 []string
	skillTrees                    map[string][]manifestAsset
	capabilityBytes               map[string]int
	preparedAgents                map[*atc.AgentStep]preparedAgentAssets
	selectedSkillPaths            map[string]struct{}
	selectedSkillFiles            []manifestAsset
	sourceDevValidationProfiles   []DevValidationProfile
	compiledDevValidationProfiles []CompiledDevValidationProfile
	compiledAssetBytes            int
	compiledSkillBytes            int
	frozenNodeAssets              map[string]frozenNodeAssets
}

func newFunctionAssetCompiler(m Manifest, function *FunctionConfig, validationProfiles []DevValidationProfile) (*functionAssetCompiler, error) {
	if err := validateCapabilityCatalog(function.Capabilities); err != nil {
		return nil, err
	}
	compiler := &functionAssetCompiler{
		manifest:                    m,
		function:                    function,
		manifestPaths:               m.Paths(),
		skillTrees:                  make(map[string][]manifestAsset),
		capabilityBytes:             make(map[string]int, len(function.Capabilities)),
		preparedAgents:              make(map[*atc.AgentStep]preparedAgentAssets),
		selectedSkillPaths:          make(map[string]struct{}),
		sourceDevValidationProfiles: append([]DevValidationProfile(nil), validationProfiles...),
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
	if err := compiler.preflightDevValidationProfiles(); err != nil {
		return err
	}
	if err := rejectAuthoredValidationRequirements(compiler.function); err != nil {
		return err
	}
	for index := range compiler.function.Plan {
		err := compiler.function.Plan[index].Config.Visit(atc.StepRecursor{
			OnTask:  compiler.preflightTask,
			OnAgent: compiler.preflightAgent,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (compiler *functionAssetCompiler) preflightDevValidationProfiles() error {
	if len(compiler.sourceDevValidationProfiles) == 0 {
		return nil
	}
	mutableRoots := devValidationMutableRoots(compiler.function)
	seen := map[string]struct{}{}
	compiler.compiledDevValidationProfiles = make([]CompiledDevValidationProfile, 0, len(compiler.sourceDevValidationProfiles))
	for index, source := range compiler.sourceDevValidationProfiles {
		identity := fmt.Sprintf("dev validation profile %d (%q)", index, source.Name)
		if err := validateDevValidationProfileName(source.Name); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		if _, duplicate := seen[source.Name]; duplicate {
			return fmt.Errorf("workflow: duplicate dev validation profile %q", source.Name)
		}
		seen[source.Name] = struct{}{}
		if err := validateDevValidationContracts(source.Candidate, source.BaseInputs); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		if err := rejectDevValidationInterpolation("capability reference", []byte(source.Capability)); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		capability, found := compiler.function.Capabilities[source.Capability]
		if !found {
			return fmt.Errorf("workflow: %s: unknown capability %q", identity, source.Capability)
		}
		if capability.Contract != DevValidationCapabilityContract {
			return fmt.Errorf("workflow: %s: capability %q must implement %s, got %s", identity, source.Capability, DevValidationCapabilityContract, capability.Contract)
		}
		if err := validateDevValidationAssetReference(source.ProfileFile, mutableRoots); err != nil {
			return fmt.Errorf("workflow: %s: profile_file: %w", identity, err)
		}
		if err := validateDevValidationAssetReference(source.ConfigFile, mutableRoots); err != nil {
			return fmt.Errorf("workflow: %s: config_file: %w", identity, err)
		}
		profile, err := resolveManifestFile(compiler.manifest, source.ProfileFile)
		if err != nil {
			return fmt.Errorf("workflow: %s: profile_file %q: %w", identity, source.ProfileFile, err)
		}
		config, err := resolveManifestFile(compiler.manifest, source.ConfigFile)
		if err != nil {
			return fmt.Errorf("workflow: %s: config_file %q: %w", identity, source.ConfigFile, err)
		}
		profileBytes, configBytes := []byte(profile), []byte(config)
		if err := rejectDevValidationInterpolation("profile bytes", profileBytes); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		if err := rejectDevValidationInterpolation("protected config bytes", configBytes); err != nil {
			return fmt.Errorf("workflow: %s: %w", identity, err)
		}
		if err := compiler.addCompiledAssetBytes(len(profileBytes), identity, "profile"); err != nil {
			return err
		}
		if err := compiler.addCompiledAssetBytes(len(configBytes), identity, "protected config"); err != nil {
			return err
		}
		imageDigest, err := devValidationImageDigest(capability.Sidecar.Image)
		if err != nil {
			return fmt.Errorf("workflow: %s: capability %q: %w", identity, source.Capability, err)
		}
		compiler.compiledDevValidationProfiles = append(compiler.compiledDevValidationProfiles, CompiledDevValidationProfile{Name: source.Name, Candidate: source.Candidate, BaseInputs: append([]DevValidationContract(nil), source.BaseInputs...), CapabilityImage: capability.Sidecar.Image, CapabilityImageDigest: imageDigest, Command: devValidationCommand(), Profile: append([]byte(nil), profileBytes...), ProfileDigest: digestDevValidationBytes(profileBytes), ProtectedConfig: append([]byte(nil), configBytes...), ProtectedConfigDigest: digestDevValidationBytes(configBytes)})
	}
	return nil
}

func (compiler *functionAssetCompiler) preflightTask(step *atc.TaskStep) error {
	if _, frozen := compiler.frozenNodeAssets[step.FunctionID]; frozen {
		return nil
	}
	if step.DevValidationAuthority != nil {
		return fmt.Errorf("workflow: task %q: dev_validation_authority is server-owned", step.Name)
	}
	if step.MergePreflightAuthority != nil {
		return fmt.Errorf("workflow: task %q: merge_preflight_authority is server-owned", step.Name)
	}
	if step.Privileged {
		return fmt.Errorf("workflow: task %q: privileged execution is not allowed for a transformation node", step.Name)
	}
	return nil
}

func (compiler *functionAssetCompiler) preflightAgent(step *atc.AgentStep) error {
	if _, frozen := compiler.frozenNodeAssets[step.FunctionID]; frozen {
		return nil
	}
	identity := agentCompileIdentity(step)
	if len(step.SkillFiles) > 0 {
		return fmt.Errorf("workflow: %s: skill_files are compiler-owned", identity)
	}
	if len(step.Skills) > 0 {
		for _, name := range append(append([]string(nil), step.Inputs...), step.Outputs...) {
			if name == "skills" {
				return fmt.Errorf("workflow: %s: compiled skills reserve logical inputs and outputs named %q", identity, name)
			}
		}
	}
	if step.RuntimeImage != "" {
		return fmt.Errorf("workflow: %s: runtime_image is server-selected and cannot be authored", identity)
	}
	if len(step.Sidecars) > 0 {
		return fmt.Errorf("workflow: %s: direct sidecars are not allowed in version 3; declare a named capability", identity)
	}

	prepared := preparedAgentAssets{capabilityURLs: map[string]string{}}
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
			if prepared.skillFiles == nil {
				prepared.skillFiles = make(map[string]string)
			}
			prepared.skillFiles[asset.path] = asset.content
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
	selectedPorts := make(map[int]string, len(step.Capabilities))
	for _, name := range step.Capabilities {
		if _, duplicate := seenCapabilities[name]; duplicate {
			return fmt.Errorf("workflow: %s: duplicate capability reference %q", identity, name)
		}
		seenCapabilities[name] = struct{}{}
		if _, found := compiler.function.Capabilities[name]; !found {
			return fmt.Errorf("workflow: %s: unknown capability %q", identity, name)
		}
		envName := capabilityMCPEnvName(name)
		if _, authored := step.Env[envName]; authored {
			return fmt.Errorf("workflow: %s: env %q is reserved for the compiled capability %q", identity, envName, name)
		}
		port, err := capabilityMCPPort(compiler.function.Capabilities[name].Sidecar)
		if err != nil {
			return fmt.Errorf("workflow: %s: capability %q: %w", identity, name, err)
		}
		if previous, duplicate := selectedPorts[port]; duplicate {
			return fmt.Errorf("workflow: %s: capabilities %q and %q both bind localhost MCP port %d", identity, previous, name, port)
		}
		selectedPorts[port] = name
		prepared.capabilityURLs[envName] = "http://127.0.0.1:" + fmt.Sprint(port) + "/mcp"
		if err := compiler.addCompiledAssetBytes(compiler.capabilityBytes[name], identity, "capability "+name); err != nil {
			return err
		}
		prepared.capabilityNames = append(prepared.capabilityNames, name)
	}
	for _, envName := range sortedTaskEnvKeys(step.Env) {
		if strings.HasSuffix(strings.ToUpper(envName), "_MCP_URL") {
			if _, generated := prepared.capabilityURLs[envName]; generated {
				// The more specific collision diagnostic above is stable and
				// explains which declaration owns the key.
				continue
			}
			return fmt.Errorf("workflow: %s: env %q declares an MCP endpoint without a named capability", identity, envName)
		}
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
	if len(compiler.compiledDevValidationProfiles) > 0 {
		compiler.function.DevValidationProfiles = cloneCompiledDevValidationProfiles(compiler.compiledDevValidationProfiles)
		provenance, err := DevValidationProvenanceHash(compiler.function.DevValidationProfiles)
		if err != nil {
			return err
		}
		compiler.function.DevValidationProvenanceHash = provenance
	}
	if len(compiler.selectedSkillFiles) > 0 {
		if compiler.function.SkillFiles == nil {
			compiler.function.SkillFiles = make(map[string]string, len(compiler.selectedSkillFiles))
		}
		for _, asset := range compiler.selectedSkillFiles {
			if existing, found := compiler.function.SkillFiles[asset.path]; found && existing != asset.content {
				return fmt.Errorf("workflow: compiled skill file %q conflicts with a frozen node asset", asset.path)
			}
			compiler.function.SkillFiles[asset.path] = asset.content
		}
	}
	for index := range compiler.function.Plan {
		err := compiler.function.Plan[index].Config.Visit(atc.StepRecursor{
			OnTask: func(step *atc.TaskStep) error {
				if _, frozen := compiler.frozenNodeAssets[step.FunctionID]; frozen {
					return nil
				}
				if err := renderDevValidationSelector(step, compiler.function.DevValidationProfiles); err != nil {
					return err
				}
				if err := renderMergePreflightSelector(step); err != nil {
					return err
				}
				step.Hermetic = true
				return nil
			},
			OnAgent: compiler.compileAgent,
		})
		if err != nil {
			return err
		}
	}
	// Named capabilities are authoring indirection. The compiled model contains
	// only the expanded interactive sidecars and the minimal validation image
	// authority, never the source capability catalog.
	compiler.function.Capabilities = nil
	return nil
}

func (compiler *functionAssetCompiler) compileAgent(step *atc.AgentStep) error {
	if _, frozen := compiler.frozenNodeAssets[step.FunctionID]; frozen {
		return nil
	}
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
	step.SkillFiles = cloneStringMap(prepared.skillFiles)
	// Transform agents operate only on their mounted snapshot values and local
	// capabilities. Live reads and writes belong to capture and publisher
	// boundaries, so admission makes network isolation non-optional.
	step.Hermetic = true
	if len(prepared.capabilityURLs) > 0 && step.Env == nil {
		step.Env = atc.TaskEnv{}
	}
	for _, name := range sortedStringMapKeys(prepared.capabilityURLs) {
		endpoint := prepared.capabilityURLs[name]
		step.Env[name] = endpoint
	}
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
		if !validCapabilityName(key) {
			return fmt.Errorf("workflow: capability name %q must match [a-z][a-z0-9-]*", key)
		}
		capability := catalog[key]
		if _, err := snapshot.ParseTypeRef(capability.Contract); err != nil {
			return fmt.Errorf("workflow: capability %q: invalid contract: %w", key, err)
		}
		if err := capability.Sidecar.ValidateCapability(); err != nil {
			return fmt.Errorf("workflow: capability %q: %w", key, err)
		}
		if capability.Sidecar.Name == "dev" || capability.Sidecar.Name == "platform" || capability.Sidecar.Name == "gateway" {
			return fmt.Errorf("workflow: capability %q: sidecar name %q is reserved for retired privileged runtime roles", key, capability.Sidecar.Name)
		}
		if _, err := capabilityMCPPort(capability.Sidecar); err != nil {
			return fmt.Errorf("workflow: capability %q: %w", key, err)
		}
		if previous, duplicate := seenSidecars[capability.Sidecar.Name]; duplicate {
			return fmt.Errorf("workflow: capability %q: sidecar name %q is also declared by capability %q", key, capability.Sidecar.Name, previous)
		}
		seenSidecars[capability.Sidecar.Name] = key
	}
	return nil
}

func validCapabilityName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, character := range name[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func capabilityMCPEnvName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_MCP_URL"
}

func capabilityMCPPort(sidecar atc.SidecarConfig) (int, error) {
	port := 0
	for _, candidate := range sidecar.Ports {
		if candidate.Protocol != "" && candidate.Protocol != "TCP" {
			continue
		}
		if candidate.ContainerPort < 1 || candidate.ContainerPort > 65535 {
			return 0, fmt.Errorf("MCP TCP port must be between 1 and 65535")
		}
		if port != 0 {
			return 0, fmt.Errorf("capability sidecar must declare exactly one TCP port for its MCP endpoint")
		}
		port = candidate.ContainerPort
	}
	if port == 0 {
		return 0, fmt.Errorf("capability sidecar must declare exactly one TCP port for its MCP endpoint")
	}
	return port, nil
}

func sortedTaskEnvKeys(env atc.TaskEnv) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
