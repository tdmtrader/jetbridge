package snapshot_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func exposureDigest(t *testing.T, hexDigit string) snapshot.Digest {
	t.Helper()
	digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat(hexDigit, 64))
	if err != nil {
		t.Fatalf("ParseDigest(%q): %v", hexDigit, err)
	}
	return digest
}

func exposureRef(t *testing.T, rawType string, id int64, hexDigit string) snapshot.SnapshotRef {
	t.Helper()
	typeRef, err := snapshot.ParseTypeRef(rawType)
	if err != nil {
		t.Fatalf("ParseTypeRef(%q): %v", rawType, err)
	}
	snapshotID, err := snapshot.NewSnapshotID(id)
	if err != nil {
		t.Fatalf("NewSnapshotID(%d): %v", id, err)
	}
	return snapshot.SnapshotRef{ID: snapshotID, Type: typeRef, Digest: exposureDigest(t, hexDigit)}
}

func TestMaterializationModeProhibitsDynamicPartialMounting(t *testing.T) {
	for _, raw := range []string{"dynamic", "Dynamic", "dynamic-selector", "agent-driven", " dynamic "} {
		t.Run(raw, func(t *testing.T) {
			if _, err := snapshot.ParseMaterializationMode(raw); !errors.Is(err, snapshot.ErrDynamicMaterializationProhibited) {
				t.Fatalf("ParseMaterializationMode(%q) error = %v, want ErrDynamicMaterializationProhibited", raw, err)
			}
		})
	}

	var decoded snapshot.InputExposure
	wire := `{"mode":"dynamic","tree_digest":"sha256:` + strings.Repeat("a", 64) + `"}`
	if err := json.Unmarshal([]byte(wire), &decoded); !errors.Is(err, snapshot.ErrDynamicMaterializationProhibited) {
		t.Fatalf("decoding a dynamic exposure error = %v, want ErrDynamicMaterializationProhibited", err)
	}
}

func TestMaterializationModeAcceptsOnlyWholeTreeExposure(t *testing.T) {
	mode, err := snapshot.ParseMaterializationMode("full")
	if err != nil || mode != snapshot.MaterializationFull {
		t.Fatalf("ParseMaterializationMode(full) = (%q, %v), want (full, nil)", mode, err)
	}
	if err := mode.Validate(); err != nil {
		t.Fatalf("%q.Validate() = %v, want nil", mode, err)
	}
	// Partial exposure is not merely unused: no path set is knowable at
	// admission, so no honest lineage row exists for one and the mode is
	// unrepresentable rather than discouraged.
	for _, raw := range []string{"", " ", "FULL", "static-selector", "static_selector", "selector", "whole-tree"} {
		if _, err := snapshot.ParseMaterializationMode(raw); err == nil {
			t.Fatalf("ParseMaterializationMode(%q) succeeded, want an unknown-mode error", raw)
		}
	}
	if err := snapshot.MaterializationMode("dynamic").Validate(); err == nil {
		t.Fatal("MaterializationMode(dynamic).Validate() succeeded, want a prohibition error")
	}
}

func TestFullTreeExposureIsTheAgenticDefaultShape(t *testing.T) {
	tree := exposureDigest(t, "b")
	exposure := snapshot.FullTreeExposure("/tmp/build/abc/diff", tree)
	if exposure.Mode != snapshot.MaterializationFull {
		t.Fatalf("FullTreeExposure().Mode = %q, want full", exposure.Mode)
	}
	if exposure.TreeDigest != tree || exposure.MountPath != "/tmp/build/abc/diff" {
		t.Fatalf("FullTreeExposure() = %+v, want the exact mounted tree", exposure)
	}
	if err := exposure.Validate(); err != nil {
		t.Fatalf("FullTreeExposure().Validate() = %v, want nil", err)
	}
	// The mount path is whatever the runtime actually used, so a relative
	// artifact root is legal: honesty about the mount beats a cosmetic rule.
	if err := snapshot.FullTreeExposure("some-artifact-root/diff", tree).Validate(); err != nil {
		t.Fatalf("relative mount path Validate() = %v, want nil", err)
	}
}

func TestInputExposureRejectsStructurallyIncompleteMounts(t *testing.T) {
	tree := exposureDigest(t, "c")
	for name, exposure := range map[string]snapshot.InputExposure{
		"partial materialization": {Mode: snapshot.MaterializationMode("static-selector"), TreeDigest: tree},
		"missing tree digest":     {Mode: snapshot.MaterializationFull},
		"invalid tree digest":     {Mode: snapshot.MaterializationFull, TreeDigest: snapshot.Digest("sha256:zz")},
		"missing mode":            {TreeDigest: tree},
		"padded mount path":       {Mode: snapshot.MaterializationFull, TreeDigest: tree, MountPath: " /tmp/build/diff"},
		"dot mount path":          {Mode: snapshot.MaterializationFull, TreeDigest: tree, MountPath: "."},
		"parent mount path":       {Mode: snapshot.MaterializationFull, TreeDigest: tree, MountPath: "../diff"},
		"unclean mount path":      {Mode: snapshot.MaterializationFull, TreeDigest: tree, MountPath: "/tmp/build//diff"},
		"trailing slash mount path": {
			Mode: snapshot.MaterializationFull, TreeDigest: tree, MountPath: "/tmp/build/diff/",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := exposure.Validate(); err == nil {
				t.Fatalf("Validate() succeeded for %s, want a structural error", name)
			}
		})
	}
}

func TestSealCommitContextExposureLineageIsTotalAndDefaultsToFullTree(t *testing.T) {
	base := exposureRef(t, "repository/v1", 1, "a")
	diff := exposureRef(t, "repository-change/v1", 2, "b")
	declared := snapshot.FullTreeExposure("/tmp/build/plan/diff", diff.Digest)
	commit := snapshot.SealCommitContext{
		TeamID: 1, TeamName: "main", CreatedBy: "alice",
		Build: &snapshot.BuildOccurrence{
			BuildID: 7, PlanID: "plan-1", Attempt: "1", StepKind: "agent", StepName: "judge",
		},
		InputOrder:      []string{"base", "diff"},
		Inputs:          map[string]snapshot.SnapshotRef{"base": base, "diff": diff},
		InputExposures:  map[string]snapshot.InputExposure{"diff": declared},
		ExpectedOutputs: []snapshot.Port{{Name: "review", Type: snapshot.TypeRef("review/v1")}},
	}
	if err := commit.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	exposures := commit.Exposures()
	if len(exposures) != 2 {
		t.Fatalf("Exposures() = %+v, want one entry per exposed input", exposures)
	}
	if got := exposures["diff"]; got.MountPath != "/tmp/build/plan/diff" || got.Mode != snapshot.MaterializationFull {
		t.Fatalf("Exposures()[diff] = %+v, want the declared mount", got)
	}
	// An undeclared exposure is not "unknown": an agentic step could read
	// anything it was given, so the honest default is the whole tree.
	base_exposure := exposures["base"]
	if base_exposure.Mode != snapshot.MaterializationFull || base_exposure.TreeDigest != base.Digest {
		t.Fatalf("Exposures()[base] = %+v, want a full-tree default bound to the input digest", base_exposure)
	}

	exposures["diff"] = snapshot.FullTreeExposure("/mutated", diff.Digest)
	if got := commit.Exposures()["diff"]; got.MountPath != "/tmp/build/plan/diff" {
		t.Fatalf("Exposures() returned a mutable view: %+v", got)
	}

	cloned := commit.Clone()
	cloned.InputExposures["diff"] = snapshot.FullTreeExposure("/mutated", diff.Digest)
	if got := commit.InputExposures["diff"]; got.MountPath != "/tmp/build/plan/diff" {
		t.Fatalf("Clone() shares its exposure map: %+v", got)
	}
}

func TestSealCommitContextRejectsExposuresThatOutrunTheExposedInputs(t *testing.T) {
	base := exposureRef(t, "repository/v1", 1, "a")
	valid := snapshot.SealCommitContext{
		TeamID: 1, TeamName: "main", CreatedBy: "alice",
		Build: &snapshot.BuildOccurrence{
			BuildID: 7, PlanID: "plan-1", Attempt: "1", StepKind: "agent", StepName: "judge",
		},
		InputOrder:      []string{"base"},
		Inputs:          map[string]snapshot.SnapshotRef{"base": base},
		ExpectedOutputs: []snapshot.Port{{Name: "review", Type: snapshot.TypeRef("review/v1")}},
	}
	for name, exposures := range map[string]map[string]snapshot.InputExposure{
		"unexposed port": {"diff": snapshot.FullTreeExposure("", base.Digest)},
		"blank port":     {" ": snapshot.FullTreeExposure("", base.Digest)},
		"foreign tree digest": {
			"base": snapshot.FullTreeExposure("", exposureDigest(t, "f")),
		},
		"invalid exposure": {
			"base": {Mode: snapshot.MaterializationMode("static-selector"), TreeDigest: base.Digest},
		},
	} {
		t.Run(name, func(t *testing.T) {
			commit := valid.Clone()
			commit.InputExposures = exposures
			if err := commit.Validate(); err == nil {
				t.Fatalf("Validate() succeeded for %s, want an exposure lineage error", name)
			}
		})
	}
}

func TestSealRequestCarriesExposureLineageIntoTheCommitContext(t *testing.T) {
	diff := exposureRef(t, "repository-change/v1", 2, "b")
	request := snapshot.SealRequest{
		BuildID: 7, TeamID: 1, TeamName: "main", CreatedBy: "alice",
		PlanID: "plan-1", Attempt: "1", StepKind: "agent", StepName: "judge",
		InputOrder:         []string{"diff"},
		Inputs:             map[string]snapshot.SnapshotRef{"diff": diff},
		InputExposures:     map[string]snapshot.InputExposure{"diff": snapshot.FullTreeExposure("/tmp/build/plan/diff", diff.Digest)},
		OutputDeclarations: []snapshot.Port{{Name: "review", Type: snapshot.TypeRef("review/v1"), Optional: true}},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	commit, err := request.CommitContext()
	if err != nil {
		t.Fatalf("CommitContext() error = %v", err)
	}
	if got := commit.Exposures()["diff"]; got.MountPath != "/tmp/build/plan/diff" || got.TreeDigest != diff.Digest {
		t.Fatalf("CommitContext() exposure = %+v, want the mount captured by the executor", got)
	}

	request.InputExposures = map[string]snapshot.InputExposure{"absent": snapshot.FullTreeExposure("", diff.Digest)}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() succeeded for an exposure naming an unexposed port")
	}
}

// ValidationResult is compared for agreement per (type_name, type_version,
// digest) in five places — agent/snapshot/sealer.go:535, sealer.go:571,
// agent/snapshot/store.go:385, store.go:835 and the SQL at
// atc/db/agent_snapshots_factory.go:498-502 — so it has no version-bump path
// and must stay a pure function of the sealed bytes. Exposure lineage is
// production-occurrence data and must never be smuggled in here.
func TestValidationResultCarriesNoOccurrenceOrExposureLineage(t *testing.T) {
	resultType := reflect.TypeOf(snapshot.ValidationResult{})
	var fields []string
	for index := range resultType.NumField() {
		fields = append(fields, resultType.Field(index).Name)
	}
	if !reflect.DeepEqual(fields, []string{"IntrinsicMetadata"}) {
		t.Fatalf("ValidationResult fields = %q, want exactly [IntrinsicMetadata]", fields)
	}
}
