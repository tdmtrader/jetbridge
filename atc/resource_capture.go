package atc

import (
	"fmt"
	"regexp"

	"github.com/concourse/concourse/agent/snapshot"
)

const (
	// ResourceCaptureFunctionID is the sole source selector for the capture
	// adapter's sealing task. Like every other function_id it is plain,
	// author-writable configuration and is therefore never a trust anchor on
	// its own; see ResourceCaptureAuthority.
	ResourceCaptureFunctionID = "resource-version-capture/v1"
	ResourceCaptureAdapter    = "resource-version"
	ResourceCaptureJobName    = "capture"
	ResourceCaptureTaskName   = "seal-snapshot"
	ResourceCaptureInput      = "source"
	ResourceCaptureOutput     = "snapshot"
	ResourceCaptureRunPath    = "/bin/sh"
	// ResourceCaptureTemplatePrefix is the reserved name prefix of the
	// server-owned one-shot pipeline that runs a capture. The suffix is
	// left(operation_key, 24) + "-" + left(config_hash, 12); the same pattern
	// is re-established by the DB when it authorizes a capture output.
	ResourceCaptureTemplatePrefix = "agent-resource-capture-"
)

// resourceCaptureTemplatePattern mirrors the SQL name predicate in
// db.FindResourceCaptureOutput / ListPendingResourceCaptureOutputs.
var resourceCaptureTemplatePattern = regexp.MustCompile(
	`^` + ResourceCaptureTemplatePrefix + `([0-9a-f]{24})-[0-9a-f]{12}$`,
)

var resourceCaptureOperationKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ResourceCaptureAuthority is server-produced authority for the one fixed task
// that converts an ordinary, untyped resource version into the FIRST typed
// snapshot. It is the only sanctioned waiver of exact typed input coverage,
// and it waives nothing else: a capture task's output must still be exactly
// typed, and the typed-snapshot smuggling guard on ordinary inputs is
// untouched.
//
// The authority is not self-authorizing. Like MergePreflightAuthority it is
// only honored when it matches server-set build metadata — here, the
// authenticated resource-capture template that owns the build's pipeline run.
type ResourceCaptureAuthority struct {
	OperationKey string           `json:"operation_key"`
	SourceInput  string           `json:"source_input"`
	OutputPort   string           `json:"output_port"`
	SnapshotType snapshot.TypeRef `json:"snapshot_type"`
}

func (a *ResourceCaptureAuthority) Clone() *ResourceCaptureAuthority {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

func (a ResourceCaptureAuthority) Validate() error {
	if !resourceCaptureOperationKeyPattern.MatchString(a.OperationKey) {
		return fmt.Errorf("resource capture authority operation key is malformed")
	}
	if a.SourceInput != ResourceCaptureInput || a.OutputPort != ResourceCaptureOutput {
		return fmt.Errorf("resource capture authority is not the fixed adapter port shape")
	}
	if err := a.SnapshotType.Validate(); err != nil {
		return fmt.Errorf("resource capture authority snapshot type: %w", err)
	}
	// validation/v1 is minted only by authoritative validation tasks, which
	// carry their own protected profile and capability image. A capture
	// adapter converts opaque resource bytes and must never be able to assert
	// a verdict type.
	if a.SnapshotType == snapshot.TypeRef("validation/v1") {
		return fmt.Errorf("resource capture authority must not mint validation snapshots")
	}
	return nil
}

// BoundToTemplate reports whether templateName is the server-owned capture
// template for this authority's operation. templateName must come from
// authenticated build metadata; an authority that names its own template
// proves nothing.
func (a ResourceCaptureAuthority) BoundToTemplate(templateName string) bool {
	if !resourceCaptureOperationKeyPattern.MatchString(a.OperationKey) {
		return false
	}
	match := resourceCaptureTemplatePattern.FindStringSubmatch(templateName)
	return match != nil && match[1] == a.OperationKey[:24]
}

// ResourceCaptureRunArgs is the fixed pass-through command. Keeping it here
// rather than in the renderer lets execution re-derive and compare it, so an
// authority cannot be lifted onto a task that runs something else.
func ResourceCaptureRunArgs(sourceInput, outputPort string) []string {
	return []string{"-ec", fmt.Sprintf("cp -a %s/. %s/", sourceInput, outputPort)}
}
