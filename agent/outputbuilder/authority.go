// Package outputbuilder is the managed, non-authoritative authoring surface
// for typed snapshot outputs. It deliberately has no path to seal a snapshot.
package outputbuilder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

const maxAuthorityFileBytes = 1 << 20

type NodeAuthority struct {
	WorkRoot string                     `json:"work_root"`
	Inputs   map[string]InputAuthority  `json:"inputs"`
	Outputs  map[string]OutputAuthority `json:"outputs"`
}

type InputAuthority struct {
	Ref       snapshot.SnapshotRef   `json:"ref"`
	MountRoot string                 `json:"mount_root"`
	Candidate bool                   `json:"candidate,omitempty"`
	Exposure  snapshot.InputExposure `json:"exposure"`
}

type OutputAuthority struct {
	Port      snapshot.Port `json:"port"`
	MountRoot string        `json:"mount_root"`
}

// LoadAuthority accepts exactly one platform-mounted JSON document. Authority
// is validated before it is used; CLI and MCP inputs can never amend it.
func LoadAuthority(name string) (NodeAuthority, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return NodeAuthority{}, fmt.Errorf("output builder authority: file path must be absolute and clean")
	}
	info, err := os.Lstat(name)
	if err != nil {
		return NodeAuthority{}, fmt.Errorf("output builder authority: inspect file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxAuthorityFileBytes {
		return NodeAuthority{}, fmt.Errorf("output builder authority: file must be a bounded regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return NodeAuthority{}, err
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maxAuthorityFileBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return NodeAuthority{}, err
	}
	if len(contents) > maxAuthorityFileBytes {
		return NodeAuthority{}, fmt.Errorf("output builder authority: file is too large")
	}
	var authority NodeAuthority
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&authority); err != nil {
		return NodeAuthority{}, fmt.Errorf("output builder authority: decode file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return NodeAuthority{}, fmt.Errorf("output builder authority: file contains trailing JSON")
	}
	if err := authority.Validate(); err != nil {
		return NodeAuthority{}, err
	}
	return authority.Clone(), nil
}

func (authority NodeAuthority) Clone() NodeAuthority {
	cloned := NodeAuthority{WorkRoot: authority.WorkRoot, Inputs: make(map[string]InputAuthority, len(authority.Inputs)), Outputs: make(map[string]OutputAuthority, len(authority.Outputs))}
	for name, input := range authority.Inputs {
		cloned.Inputs[name] = input
	}
	for name, output := range authority.Outputs {
		cloned.Outputs[name] = output
	}
	return cloned
}

// Validate enforces a direct-child, non-overlapping mount layout. The builder
// rechecks this model on every operation so replacement paths cannot gain use
// after initial acceptance.
func (authority NodeAuthority) Validate() error {
	if err := validateAuthorityRoot("work root", authority.WorkRoot); err != nil {
		return err
	}
	if len(authority.Outputs) == 0 {
		return fmt.Errorf("output builder authority: at least one output is required")
	}
	roots := make(map[string]string, len(authority.Inputs)+len(authority.Outputs))
	for _, name := range sortedNames(authority.Inputs) {
		if err := validateName("input", name); err != nil {
			return err
		}
		input := authority.Inputs[name]
		if err := input.Ref.Validate(); err != nil {
			return fmt.Errorf("output builder authority: input %q: %w", name, err)
		}
		if input.Exposure.MountPath != input.MountRoot || input.Exposure.TreeDigest != input.Ref.Digest {
			return fmt.Errorf("output builder authority: input %q exposure does not bind its mount and digest", name)
		}
		if err := input.Exposure.Validate(); err != nil {
			return fmt.Errorf("output builder authority: input %q exposure: %w", name, err)
		}
		if err := validateMount(authority.WorkRoot, "input "+name, input.MountRoot); err != nil {
			return err
		}
		if previous, found := roots[input.MountRoot]; found {
			return fmt.Errorf("output builder authority: input %q overlaps %s", name, previous)
		}
		roots[input.MountRoot] = "input " + name
	}
	ports := make([]snapshot.Port, 0, len(authority.Outputs))
	for _, name := range sortedNames(authority.Outputs) {
		if err := validateName("output", name); err != nil {
			return err
		}
		output := authority.Outputs[name]
		if output.Port.Name != name {
			return fmt.Errorf("output builder authority: output key %q does not match port name %q", name, output.Port.Name)
		}
		ports = append(ports, output.Port)
		if err := validateMount(authority.WorkRoot, "output "+name, output.MountRoot); err != nil {
			return err
		}
		if previous, found := roots[output.MountRoot]; found {
			return fmt.Errorf("output builder authority: output %q overlaps %s", name, previous)
		}
		roots[output.MountRoot] = "output " + name
	}
	if err := snapshot.ValidatePorts(ports); err != nil {
		return fmt.Errorf("output builder authority: outputs: %w", err)
	}
	return nil
}

func validateAuthorityRoot(label, root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return fmt.Errorf("output builder authority: %s must be an absolute clean non-root path", label)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("output builder authority: inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output builder authority: %s must be a directory and not a symlink", label)
	}
	return nil
}

func validateMount(work, label, root string) error {
	if filepath.Dir(root) != work {
		return fmt.Errorf("output builder authority: %s mount root %q must be a direct child of work root", label, root)
	}
	return validateAuthorityRoot(label+" mount root", root)
}

func validateName(kind, name string) error {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("output builder authority: %s name %q is invalid", kind, name)
	}
	return nil
}

func sortedNames[T any](values map[string]T) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
