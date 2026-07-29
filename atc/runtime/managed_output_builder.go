package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// ValidateManagedOutputBuilder validates the one server-owned output-builder
// contract before a worker creates a container. It intentionally accepts no
// generic trusted-sidecar shape: authority, image, node type, and projections
// must all bind to the exact agent execution layout.
func ValidateManagedOutputBuilder(spec ContainerSpec) error {
	builder := spec.ManagedOutputBuilder
	for _, sidecar := range spec.Sidecars {
		if sidecar.Name == ManagedOutputBuilderName && builder == nil {
			return fmt.Errorf("managed output builder sidecar %q is reserved", ManagedOutputBuilderName)
		}
	}
	if builder == nil {
		return nil
	}
	if spec.Type != db.ContainerTypeAgent {
		return errors.New("managed output builder is valid only for agent containers")
	}
	if err := atc.ValidatePinnedOCIImage(spec.ImageSpec.ImageURL); err != nil {
		return fmt.Errorf("managed output builder requires a pinned admitted image: %w", err)
	}
	authority, err := decodeManagedOutputBuilderAuthority(builder.Authority)
	if err != nil {
		return err
	}
	if err := validateManagedOutputBuilderLayout(spec, builder, authority); err != nil {
		return err
	}

	found := 0
	for _, sidecar := range spec.Sidecars {
		if sidecar.Name != ManagedOutputBuilderName {
			continue
		}
		found++
		if sidecar.Image != spec.ImageSpec.ImageURL || sidecar.WorkingDir != "/" || len(sidecar.Command) != 2 ||
			sidecar.Command[0] != "/usr/local/bin/agent-output" || sidecar.Command[1] != "serve" || len(sidecar.Args) != 0 ||
			len(sidecar.Ports) != 1 || sidecar.Ports[0] != (atc.SidecarPort{ContainerPort: 7783, Protocol: "TCP"}) {
			return errors.New("managed output builder sidecar has an invalid fixed runtime shape")
		}
		if len(sidecar.Env) != 0 || len(spec.SidecarEnv[sidecar.Name]) != 0 {
			return errors.New("managed output builder must not receive authored or injected environment")
		}
	}
	if found != 1 {
		return fmt.Errorf("managed output builder sidecar must occur exactly once (found %d)", found)
	}
	return nil
}

func decodeManagedOutputBuilderAuthority(mount PrivateFileMount) (outputbuilder.NodeAuthority, error) {
	if mount.MountPath != ManagedOutputBuilderAuthorityMountRoot || len(mount.Files) != 1 ||
		len(mount.Files[ManagedOutputBuilderAuthorityFile]) == 0 {
		return outputbuilder.NodeAuthority{}, fmt.Errorf("managed output builder requires one authority.json file at %q", ManagedOutputBuilderAuthorityMountRoot)
	}
	decoder := json.NewDecoder(bytes.NewReader(mount.Files[ManagedOutputBuilderAuthorityFile]))
	decoder.DisallowUnknownFields()
	var authority outputbuilder.NodeAuthority
	if err := decoder.Decode(&authority); err != nil {
		return outputbuilder.NodeAuthority{}, fmt.Errorf("managed output builder authority: decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return outputbuilder.NodeAuthority{}, errors.New("managed output builder authority contains trailing JSON")
	}
	return authority, nil
}

// validateManagedOutputBuilderLayout is deliberately filesystem-free. Task 8
// retains NodeAuthority.Validate for the mounted in-sidecar filesystem proof;
// this validator binds its canonical layout to the server's ContainerSpec.
func validateManagedOutputBuilderLayout(spec ContainerSpec, builder *ManagedOutputBuilder, authority outputbuilder.NodeAuthority) error {
	if authority.WorkRoot != spec.Dir || !validManagedAuthorityPath(authority.WorkRoot) {
		return errors.New("managed output builder authority work root does not match the container")
	}
	if len(authority.Outputs) == 0 {
		return errors.New("managed output builder authority has no record outputs")
	}
	inputPaths := make([]string, 0, len(authority.Inputs))
	for _, name := range sortedManagedNames(authority.Inputs) {
		input := authority.Inputs[name]
		path, err := managedAuthorityChild(authority.WorkRoot, name)
		if err != nil || input.MountRoot != path || input.Candidate || input.Ref.Validate() != nil ||
			input.Exposure.Validate() != nil || input.Exposure.MountPath != path || input.Exposure.TreeDigest != input.Ref.Digest {
			return fmt.Errorf("managed output builder authority input %q is not canonical", name)
		}
		if countContainerInputs(spec.Dir, spec.Inputs, path) != 1 {
			return fmt.Errorf("managed output builder input %q does not bind one exact container input", name)
		}
		inputPaths = append(inputPaths, path)
	}
	outputPaths := make([]string, 0, len(authority.Outputs))
	ports := make([]snapshot.Port, 0, len(authority.Outputs))
	for _, name := range sortedManagedNames(authority.Outputs) {
		output := authority.Outputs[name]
		path, err := managedAuthorityChild(authority.WorkRoot, name)
		if err != nil || output.MountRoot != path || output.Port.Name != name {
			return fmt.Errorf("managed output builder authority output %q is not canonical", name)
		}
		if _, builtin := contracts.BuiltinRawRecordCodec(output.Port.Type); !builtin {
			return fmt.Errorf("managed output builder authority output %q is not a built-in record", name)
		}
		if outputPath, found := spec.Outputs[name]; !found || resolveManagedSpecPath(spec.Dir, outputPath) != path {
			return fmt.Errorf("managed output builder output %q does not bind one exact container output", name)
		}
		ports = append(ports, output.Port)
		outputPaths = append(outputPaths, path)
	}
	if err := snapshot.ValidatePorts(ports); err != nil {
		return fmt.Errorf("managed output builder authority outputs: %w", err)
	}
	if err := equalManagedPaths(builder.InputMountPaths, inputPaths); err != nil {
		return fmt.Errorf("managed output builder input projections: %w", err)
	}
	if err := equalManagedPaths(builder.OutputMountPaths, outputPaths); err != nil {
		return fmt.Errorf("managed output builder output projections: %w", err)
	}
	roots := append(append([]string{}, inputPaths...), outputPaths...)
	for index, root := range roots {
		for prior := 0; prior < index; prior++ {
			if managedPathsOverlap(root, roots[prior]) {
				return fmt.Errorf("managed output builder roots %q and %q overlap", root, roots[prior])
			}
		}
	}
	for _, input := range spec.Inputs {
		path := resolveManagedSpecPath(spec.Dir, input.DestinationPath)
		if containsManagedPath(inputPaths, path) {
			continue
		}
		for _, root := range roots {
			if managedPathsOverlap(root, path) {
				return fmt.Errorf("managed output builder root %q overlaps untyped input", root)
			}
		}
	}
	for name, output := range spec.Outputs {
		path := resolveManagedSpecPath(spec.Dir, output)
		if authorityOutput, found := authority.Outputs[name]; found && authorityOutput.MountRoot == path {
			continue
		}
		for _, root := range roots {
			if managedPathsOverlap(root, path) {
				return fmt.Errorf("managed output builder root %q overlaps untyped output", root)
			}
		}
	}
	for _, root := range roots {
		for _, mount := range spec.SecretMounts {
			if managedPathsOverlap(root, resolveManagedSpecPath(spec.Dir, mount.MountPath)) {
				return fmt.Errorf("managed output builder root %q overlaps ordinary secret mount", root)
			}
		}
		for _, cache := range spec.Caches {
			if managedPathsOverlap(root, resolveManagedSpecPath(spec.Dir, cache)) {
				return fmt.Errorf("managed output builder root %q overlaps cache", root)
			}
		}
		for _, scratch := range spec.ScratchPaths {
			if managedPathsOverlap(root, resolveManagedSpecPath(spec.Dir, scratch)) {
				return fmt.Errorf("managed output builder root %q overlaps scratch", root)
			}
		}
	}
	return nil
}

func containsManagedPath(paths []string, candidate string) bool {
	for _, path := range paths {
		if path == candidate {
			return true
		}
	}
	return false
}

func validManagedAuthorityPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}

func managedAuthorityChild(root, name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\/") {
		return "", fmt.Errorf("invalid direct-child name %q", name)
	}
	path := filepath.Join(root, name)
	if filepath.Dir(path) != root {
		return "", fmt.Errorf("path %q is not a direct child", path)
	}
	return path, nil
}

func equalManagedPaths(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d paths, want %d", len(got), len(want))
	}
	seen := map[string]struct{}{}
	for _, path := range got {
		if !validManagedAuthorityPath(path) || managedPathsOverlap(path, ManagedOutputBuilderAuthorityMountRoot) {
			return fmt.Errorf("invalid path %q", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("duplicate path %q", path)
		}
		seen[path] = struct{}{}
	}
	for _, path := range want {
		if _, found := seen[path]; !found {
			return fmt.Errorf("missing canonical path %q", path)
		}
	}
	return nil
}

func sortedManagedNames[V any](values map[string]V) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func countContainerInputs(dir string, inputs []Input, path string) int {
	count := 0
	for _, input := range inputs {
		if resolveManagedSpecPath(dir, input.DestinationPath) == path {
			count++
		}
	}
	return count
}

func resolveManagedSpecPath(dir, path string) string {
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

func managedPathsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
