package exec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

func TestOutputBuilderAuthorityUsesOnlyBoundTypedPortsAndBuiltInRecords(t *testing.T) {
	digest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	inputs := snapshotInputBindings{order: []string{"change"}, refs: map[string]snapshot.SnapshotRef{
		"change": {ID: 7, Type: "repository-change/v1", Digest: digest},
	}}
	inputs.recordExposure("change", inputs.refs["change"], "/work/change")
	builder, err := outputBuilderAuthority("/work", inputs,
		map[string]atc.SnapshotInputConfig{"change": {Type: "repository-change/v1"}, "optional": {Type: "repository-change/v1", Optional: true}},
		map[string]atc.SnapshotOutputConfig{"review": {Type: "review/v1"}, "opaque": {Type: "opaque/v1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if builder == nil || len(builder.InputMountPaths) != 1 || builder.InputMountPaths[0] != "/work/change" || len(builder.OutputMountPaths) != 1 || builder.OutputMountPaths[0] != "/work/review" {
		t.Fatalf("builder projection = %#v", builder)
	}
	var authority outputbuilder.NodeAuthority
	if err := json.Unmarshal(builder.Authority.Files[runtime.ManagedOutputBuilderAuthorityFile], &authority); err != nil {
		t.Fatal(err)
	}
	if _, found := authority.Inputs["optional"]; found {
		t.Fatal("absent optional typed input entered authority")
	}
	if _, found := authority.Outputs["opaque"]; found {
		t.Fatal("non-record output entered authority")
	}
	if authority.Inputs["change"].Ref.Digest != digest || authority.Outputs["review"].MountRoot != "/work/review" {
		t.Fatalf("authority = %#v", authority)
	}
}

func TestOutputBuilderAuthorityIsAbsentWithoutBuiltInRecordOutput(t *testing.T) {
	builder, err := outputBuilderAuthority("/work", snapshotInputBindings{refs: map[string]snapshot.SnapshotRef{}}, nil, map[string]atc.SnapshotOutputConfig{"opaque": {Type: "opaque/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if builder != nil {
		t.Fatalf("builder = %#v, want nil", builder)
	}
}
