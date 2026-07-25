package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// MaterializationMode declares how much of an exposed input a step was
// actually shown. It exists so "did the judge look at the diff?" is answerable
// from persisted lineage rather than inferred from a producer's own record.
//
// The set is closed at two values on purpose. Dynamic, agent-driven partial
// mounting is prohibited: its path set is unknown at admission, so no honest
// lineage row could be written for it. There is deliberately no constant for
// it, and ParseMaterializationMode fails loudly if one is expressed.
type MaterializationMode string

const (
	// MaterializationFull is the default for agentic steps. An agent could
	// read anything it was given, so lineage records the whole tree — the tree
	// digest is the record of every path in it. This is honest, not lazy.
	MaterializationFull MaterializationMode = "full"

	// MaterializationStaticSelector is for deterministic steps: the exact
	// document set was chosen before the step ran, so every exposed path can
	// be enumerated with its own digest at admission time.
	MaterializationStaticSelector MaterializationMode = "static-selector"
)

// ErrDynamicMaterializationProhibited is returned whenever a dynamic,
// agent-driven partial materialization is expressed. Lineage for it cannot be
// known at admission, so it is not a mode this platform can record.
var ErrDynamicMaterializationProhibited = errors.New(
	"snapshot: dynamic agent-driven partial materialization is prohibited: exposure lineage cannot be known at admission",
)

func ParseMaterializationMode(raw string) (MaterializationMode, error) {
	if err := prohibitDynamicMaterialization(raw); err != nil {
		return "", err
	}
	mode := MaterializationMode(raw)
	if err := mode.Validate(); err != nil {
		return "", err
	}
	return mode, nil
}

func prohibitDynamicMaterialization(raw string) error {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(normalized, "dynamic") || strings.Contains(normalized, "agent-driven") {
		return fmt.Errorf("%w: %q", ErrDynamicMaterializationProhibited, raw)
	}
	return nil
}

func (m MaterializationMode) Validate() error {
	if err := prohibitDynamicMaterialization(string(m)); err != nil {
		return err
	}
	switch m {
	case MaterializationFull, MaterializationStaticSelector:
		return nil
	case "":
		return fmt.Errorf("snapshot: materialization mode is required")
	default:
		return fmt.Errorf("snapshot: unknown materialization mode %q", string(m))
	}
}

func (m MaterializationMode) String() string { return string(m) }

func (m MaterializationMode) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(m))
}

func (m *MaterializationMode) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseMaterializationMode(raw)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// ExposedPath is one exact document a static selector materialized, with the
// digest of the bytes that were exposed. The path is relative to the canonical
// archive root — the only namespace verifiable at seal time — never to a pod
// mount path.
type ExposedPath struct {
	Path   string `json:"path"`
	Digest Digest `json:"digest"`
}

func (p ExposedPath) Validate() error {
	if err := validateExposedPath(p.Path); err != nil {
		return err
	}
	if err := p.Digest.Validate(); err != nil {
		return fmt.Errorf("snapshot: exposed path %q digest: %w", p.Path, err)
	}
	return nil
}

func validateExposedPath(raw string) error {
	if raw == "" {
		return fmt.Errorf("snapshot: exposed path is required")
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("snapshot: exposed path %q must not be padded", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return fmt.Errorf("snapshot: exposed path %q must be archive-relative", raw)
	}
	if path.Clean(raw) != raw {
		return fmt.Errorf("snapshot: exposed path %q must be a clean archive-relative path", raw)
	}
	// A pattern is what a dynamic selector would need. Enumerable literal
	// paths are the structural reason a partial exposure can be recorded at
	// all, so wildcards are rejected outright rather than expanded later.
	if strings.ContainsAny(raw, "*?") {
		return fmt.Errorf("snapshot: exposed path %q must be a literal path, not a pattern", raw)
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("snapshot: exposed path %q has an empty or relative segment", raw)
		}
	}
	return nil
}

// InputExposure is the mount-time record of what one exposed input actually
// showed a step. It is production-occurrence data: it never enters sealed
// bytes, and it is never part of ValidationResult.IntrinsicMetadata, which is
// compared for content agreement and therefore has no version-bump path.
type InputExposure struct {
	Mode MaterializationMode `json:"mode"`
	// TreeDigest is the digest of the exposed snapshot's canonical tree. The
	// server binds it to the exposed input's own digest, so a producer cannot
	// claim to have been shown a tree it was not given.
	TreeDigest Digest `json:"tree_digest"`
	// MountPath is where the tree was materialized in the step's filesystem,
	// when it was materialized at all. It is empty for an input exposed only
	// by reference to a validator.
	MountPath string `json:"mount_path,omitempty"`
	// Paths is the exact exposed document set, sorted by path. It is empty for
	// full materialization, where the tree digest already records every path.
	Paths []ExposedPath `json:"paths,omitempty"`
}

// FullTreeExposure records the whole tree as exposed, which is the default for
// agentic steps.
func FullTreeExposure(mountPath string, treeDigest Digest) InputExposure {
	return InputExposure{Mode: MaterializationFull, TreeDigest: treeDigest, MountPath: mountPath}
}

// NewStaticSelectorExposure records an enumerated document set as exposed. The
// path set is sorted here so callers never hand-sort a wire value, and every
// path must carry its own digest — which is precisely why a dynamic selection
// cannot be expressed through this constructor.
func NewStaticSelectorExposure(mountPath string, treeDigest Digest, paths ...ExposedPath) (InputExposure, error) {
	exposure := InputExposure{
		Mode: MaterializationStaticSelector, TreeDigest: treeDigest, MountPath: mountPath,
		Paths: append([]ExposedPath(nil), paths...),
	}
	sortExposedPaths(exposure.Paths)
	if err := exposure.Validate(); err != nil {
		return InputExposure{}, err
	}
	return exposure, nil
}

func sortExposedPaths(paths []ExposedPath) {
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && paths[j].Path < paths[j-1].Path; j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
}

func (e InputExposure) Validate() error {
	if err := e.Mode.Validate(); err != nil {
		return err
	}
	if err := e.TreeDigest.Validate(); err != nil {
		return fmt.Errorf("snapshot: exposure tree digest: %w", err)
	}
	if err := validateMountPath(e.MountPath); err != nil {
		return err
	}
	switch e.Mode {
	case MaterializationFull:
		if len(e.Paths) != 0 {
			return fmt.Errorf("snapshot: full materialization must not enumerate a partial path set")
		}
	case MaterializationStaticSelector:
		if len(e.Paths) == 0 {
			return fmt.Errorf("snapshot: static selector materialization must enumerate its exposed paths")
		}
	}
	for index, exposed := range e.Paths {
		if err := exposed.Validate(); err != nil {
			return err
		}
		if index > 0 && e.Paths[index-1].Path >= exposed.Path {
			return fmt.Errorf("snapshot: exposed paths must be unique and sorted, got %q after %q", exposed.Path, e.Paths[index-1].Path)
		}
	}
	return nil
}

func validateMountPath(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("snapshot: exposure mount path %q must not be padded", raw)
	}
	// The mount path is recorded exactly as the runtime used it, so a relative
	// artifact root is legal. What is rejected is anything that is not one
	// concrete location: padding, unclean spellings, and relative traversal.
	if path.Clean(raw) != raw {
		return fmt.Errorf("snapshot: exposure mount path %q must be a clean path", raw)
	}
	if raw == "/" || raw == "." || raw == ".." || strings.HasPrefix(raw, "../") {
		return fmt.Errorf("snapshot: exposure mount path %q must name one concrete location", raw)
	}
	return nil
}

func (e InputExposure) Clone() InputExposure {
	e.Paths = append([]ExposedPath(nil), e.Paths...)
	return e
}

func (e InputExposure) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire InputExposure
	return json.Marshal(wire(e))
}

func (e *InputExposure) UnmarshalJSON(data []byte) error {
	type wire InputExposure
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := InputExposure(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*e = parsed.Clone()
	return nil
}

// validateDeclaredExposures checks server-declared exposure lineage against
// the exposed inputs it describes. Every declaration must name an exposed
// input and must carry that input's own tree digest: the exposure is a
// server-side observation of the mount, never a claim the step can make.
func validateDeclaredExposures(exposures map[string]InputExposure, inputs map[string]SnapshotRef) error {
	for port, exposure := range exposures {
		if strings.TrimSpace(port) == "" {
			return fmt.Errorf("snapshot: exposure input port name is required")
		}
		ref, exposed := inputs[port]
		if !exposed {
			return fmt.Errorf("snapshot: exposure input port %q is not an exposed input", port)
		}
		if err := exposure.Validate(); err != nil {
			return fmt.Errorf("snapshot: exposure for input %q: %w", port, err)
		}
		if exposure.TreeDigest != ref.Digest {
			return fmt.Errorf("snapshot: exposure for input %q declares tree %s, but the exposed input is %s",
				port, exposure.TreeDigest, ref.Digest)
		}
	}
	return nil
}

// resolveExposures returns one exposure per exposed input. An input with no
// declaration is not "unknown": the step could have read all of it, so the
// honest default is the whole tree.
func resolveExposures(exposures map[string]InputExposure, inputs map[string]SnapshotRef) map[string]InputExposure {
	resolved := make(map[string]InputExposure, len(inputs))
	for port, ref := range inputs {
		if declared, found := exposures[port]; found {
			resolved[port] = declared.Clone()
			continue
		}
		resolved[port] = FullTreeExposure("", ref.Digest)
	}
	return resolved
}

func cloneInputExposures(values map[string]InputExposure) map[string]InputExposure {
	if values == nil {
		return nil
	}
	cloned := make(map[string]InputExposure, len(values))
	for port, exposure := range values {
		cloned[port] = exposure.Clone()
	}
	return cloned
}
