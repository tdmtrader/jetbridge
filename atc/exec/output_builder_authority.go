package exec

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

const (
	outputBuilderMarkerEnv = "CONCOURSE_OUTPUT_BUILDER_MCP"
	outputBuilderMCPEnv    = "OUTPUT_BUILDER_MCP_URL"
	outputBuilderMCPURL    = "http://127.0.0.1:7783/mcp"
	outputBuilderMCPPort   = 7783
)

// outputBuilderAuthority derives the builder's only authority document from
// frozen typed declarations and their resolved bindings. It has no input from
// prompt, environment, or authored MCP configuration.
func outputBuilderAuthority(workRoot string, inputs snapshotInputBindings, inputDecls map[string]atc.SnapshotInputConfig, outputDecls map[string]atc.SnapshotOutputConfig) (*runtime.ManagedOutputBuilder, error) {
	hasBuiltinRecordOutput := false
	for _, declaration := range outputDecls {
		if _, builtin := contracts.BuiltinRawRecordCodec(declaration.Type); builtin {
			hasBuiltinRecordOutput = true
			break
		}
	}
	if !hasBuiltinRecordOutput {
		return nil, nil
	}
	if !filepath.IsAbs(workRoot) || filepath.Clean(workRoot) != workRoot || workRoot == "/" {
		return nil, fmt.Errorf("output builder authority: work root must be absolute, clean, and non-root")
	}
	authority := outputbuilder.NodeAuthority{WorkRoot: workRoot, Inputs: map[string]outputbuilder.InputAuthority{}, Outputs: map[string]outputbuilder.OutputAuthority{}}
	inputPaths := make([]string, 0, len(inputs.refs))
	boundOrder := make(map[string]struct{}, len(inputs.order))
	for _, name := range inputs.order {
		if _, duplicate := boundOrder[name]; duplicate {
			return nil, fmt.Errorf("output builder authority: input %q occurs more than once in binding order", name)
		}
		if _, found := inputs.refs[name]; !found {
			return nil, fmt.Errorf("output builder authority: binding order input %q has no ref", name)
		}
		boundOrder[name] = struct{}{}
	}
	for _, name := range sortedSnapshotKeys(inputDecls) {
		decl := inputDecls[name]
		if err := decl.Validate(); err != nil {
			return nil, fmt.Errorf("output builder authority: input %q: %w", name, err)
		}
		ref, present := inputs.refs[name]
		if !present {
			if decl.Optional {
				continue
			}
			return nil, fmt.Errorf("output builder authority: required input %q is absent", name)
		}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("output builder authority: input %q ref: %w", name, err)
		}
		if ref.Type != decl.Type {
			return nil, fmt.Errorf("output builder authority: input %q type does not match declaration", name)
		}
		exposure, present := inputs.exposures[name]
		if !present || exposure.TreeDigest != ref.Digest || exposure.Validate() != nil {
			return nil, fmt.Errorf("output builder authority: input %q has no exact resolved exposure", name)
		}
		mount := filepath.Join(workRoot, name)
		if !outputBuilderDirectChild(workRoot, name, mount) || exposure.MountPath != mount {
			return nil, fmt.Errorf("output builder authority: input %q mount does not match typed path", name)
		}
		authority.Inputs[name] = outputbuilder.InputAuthority{Ref: ref, MountRoot: mount, Exposure: exposure.Clone()}
		inputPaths = append(inputPaths, mount)
	}
	for name := range inputs.refs {
		if _, ordered := boundOrder[name]; !ordered {
			return nil, fmt.Errorf("output builder authority: bound input %q is absent from binding order", name)
		}
		if _, declared := inputDecls[name]; !declared {
			return nil, fmt.Errorf("output builder authority: bound input %q is not declared", name)
		}
	}
	for name := range inputs.exposures {
		if _, bound := inputs.refs[name]; !bound {
			return nil, fmt.Errorf("output builder authority: exposure input %q is not bound", name)
		}
	}
	outputPaths := make([]string, 0, len(outputDecls))
	ports := make([]snapshot.Port, 0, len(outputDecls))
	for _, name := range sortedSnapshotKeys(outputDecls) {
		decl := outputDecls[name]
		if err := decl.Validate(); err != nil {
			return nil, fmt.Errorf("output builder authority: output %q: %w", name, err)
		}
		if _, builtin := contracts.BuiltinRawRecordCodec(decl.Type); !builtin {
			continue
		}
		mount := filepath.Join(workRoot, name)
		if !outputBuilderDirectChild(workRoot, name, mount) {
			return nil, fmt.Errorf("output builder authority: output %q is not a direct child mount", name)
		}
		port := snapshot.Port{Name: name, Type: decl.Type, Optional: decl.Optional}
		authority.Outputs[name] = outputbuilder.OutputAuthority{Port: port, MountRoot: mount}
		ports = append(ports, port)
		outputPaths = append(outputPaths, mount)
	}
	if len(authority.Outputs) == 0 {
		return nil, nil
	}
	if err := snapshot.ValidatePorts(ports); err != nil {
		return nil, fmt.Errorf("output builder authority: outputs: %w", err)
	}
	seenPaths := make(map[string]struct{}, len(inputPaths)+len(outputPaths))
	for _, path := range append(append([]string{}, inputPaths...), outputPaths...) {
		if _, duplicate := seenPaths[path]; duplicate {
			return nil, fmt.Errorf("output builder authority: typed mount %q overlaps another typed mount", path)
		}
		seenPaths[path] = struct{}{}
	}
	raw, err := json.Marshal(authority)
	if err != nil {
		return nil, fmt.Errorf("output builder authority: encode: %w", err)
	}
	sort.Strings(inputPaths)
	sort.Strings(outputPaths)
	return &runtime.ManagedOutputBuilder{
		Authority:       runtime.PrivateFileMount{MountPath: runtime.ManagedOutputBuilderAuthorityMountRoot, Files: map[string][]byte{runtime.ManagedOutputBuilderAuthorityFile: raw}},
		InputMountPaths: inputPaths, OutputMountPaths: outputPaths,
	}, nil
}

func outputBuilderDirectChild(workRoot, name, path string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && filepath.Dir(path) == workRoot
}

func reserveOutputBuilder(sidecars []atc.SidecarConfig, env []string) error {
	for _, row := range env {
		name, value, _ := strings.Cut(row, "=")
		if name == outputBuilderMarkerEnv || name == outputBuilderMCPEnv || value == outputBuilderMCPURL {
			return fmt.Errorf("managed output builder configuration is reserved by the platform")
		}
	}
	for _, sidecar := range sidecars {
		if sidecar.Name == runtime.ManagedOutputBuilderName {
			return fmt.Errorf("managed output builder sidecar name %q is reserved", runtime.ManagedOutputBuilderName)
		}
		for _, port := range sidecar.Ports {
			if port.ContainerPort == outputBuilderMCPPort {
				return fmt.Errorf("managed output builder endpoint port %d is reserved", outputBuilderMCPPort)
			}
		}
		for _, row := range sidecar.Env {
			if row.Name == outputBuilderMarkerEnv || row.Name == outputBuilderMCPEnv || row.Value == outputBuilderMCPURL {
				return fmt.Errorf("managed output builder endpoint is reserved")
			}
		}
	}
	return nil
}

func injectManagedOutputBuilder(spec *runtime.ContainerSpec, image string, builder *runtime.ManagedOutputBuilder) error {
	if builder == nil {
		return nil
	}
	if err := atc.ValidatePinnedOCIImage(image); err != nil {
		return fmt.Errorf("managed output builder requires admitted pinned runtime image: %w", err)
	}
	spec.ManagedOutputBuilder = builder
	spec.Sidecars = append(spec.Sidecars, atc.SidecarConfig{Name: runtime.ManagedOutputBuilderName, Image: image, Command: []string{"/usr/local/bin/agent-output", "serve"}, Ports: []atc.SidecarPort{{ContainerPort: outputBuilderMCPPort, Protocol: "TCP"}}, WorkingDir: "/"})
	spec.Env = append(spec.Env, outputBuilderMarkerEnv+"=1")
	return runtime.ValidateManagedOutputBuilder(*spec)
}
