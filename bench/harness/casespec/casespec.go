// Package casespec reads a bench corpus case.yaml well enough to materialize
// its pre-state into typed snapshots.
//
// It exists because the corpus is heterogeneous by design: pre_state ports are
// spelled as block maps in some cases and inline flow maps in others, the
// exposed change file is `task/change.diff` in most cases but
// `task/change-under-review.diff` in neg-cc-001, and some ports carry an
// explicit `materialize:` directive that forbids a plain checkout. Parsing this
// with grep/awk silently produces wrong snapshots rather than failing.
package casespec

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Spec is the part of a case.yaml the materializer needs.
type Spec struct {
	ID     string
	Dir    string
	Inputs map[string]string // port name -> snapshot type ref
	Ports  map[string]Port   // port name -> how to materialize it
}

// Port is one pre_state entry.
type Port struct {
	Name string
	// Repo is the corpus repo label (jetbridge, concourse-upstream,
	// lightingdesign) when this port is a source tree.
	Repo string
	// Ref is the pinned commit for a source-tree port.
	Ref string
	// Path is a case-relative exposed file (task/...) for a document port.
	Path string
	// Materialize is the case's explicit materialization directive, when it
	// carries one. Its presence means a naive checkout is unsafe.
	Materialize string
}

// SourceTree reports whether this port materializes a repository tree.
func (port Port) SourceTree() bool { return port.Ref != "" }

// Load reads and normalizes one case directory.
func Load(corpusDir, caseID string) (*Spec, error) {
	dir := filepath.Join(corpusDir, caseID)
	raw, err := os.ReadFile(filepath.Join(dir, "case.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read case: %w", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse case %s: %w", caseID, err)
	}

	spec := &Spec{
		ID:     caseID,
		Dir:    dir,
		Inputs: map[string]string{},
		Ports:  map[string]Port{},
	}

	signature, _ := document["signature"].(map[string]any)
	if signature == nil {
		return nil, fmt.Errorf("case %s: no signature", caseID)
	}
	inputs, _ := signature["inputs"].(map[string]any)
	if len(inputs) == 0 {
		return nil, fmt.Errorf("case %s: signature has no inputs", caseID)
	}
	for name, typeRef := range inputs {
		text, _ := typeRef.(string)
		if text == "" {
			return nil, fmt.Errorf("case %s: input %q has no type", caseID, name)
		}
		spec.Inputs[name] = text
	}

	preState, _ := document["pre_state"].(map[string]any)
	if preState == nil {
		return nil, fmt.Errorf("case %s: no pre_state", caseID)
	}
	for name, value := range preState {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		port := Port{
			Name:        name,
			Repo:        text(entry["repo"]),
			Ref:         text(entry["ref"]),
			Path:        text(entry["path"]),
			Materialize: text(entry["materialize"]),
		}
		spec.Ports[name] = port
	}

	// Every declared input must have a way to be materialized.
	for name := range spec.Inputs {
		port, found := spec.Ports[name]
		if !found {
			return nil, fmt.Errorf("case %s: input %q has no pre_state entry", caseID, name)
		}
		if port.Ref == "" && port.Path == "" {
			return nil, fmt.Errorf("case %s: pre_state %q has neither ref nor path", caseID, name)
		}
	}
	return spec, nil
}

func text(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}
