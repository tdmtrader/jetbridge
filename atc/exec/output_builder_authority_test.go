package exec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

func TestOutputBuilderAuthorityUsesOnlyBoundTypedPortsAndBuiltInRecords(t *testing.T) {
	digest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	ref := snapshot.SnapshotRef{ID: 7, Type: "repository-change/v1", Digest: digest}
	inputs := snapshotInputBindings{order: []string{"change"}, refs: map[string]snapshot.SnapshotRef{"change": ref}}
	inputs.recordExposure("change", ref, "/work/change")
	intrinsicMetadata := json.RawMessage(`{"repository_id":"sha256:cafe"}`)
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(outputBuilderManifest(ref, intrinsicMetadata), true, nil)

	builder, err := outputBuilderAuthority(context.Background(), 91, metadata, "/work", inputs,
		map[string]atc.SnapshotInputConfig{"change": {Type: "repository-change/v1"}, "optional": {Type: "repository-change/v1", Optional: true}},
		map[string]atc.SnapshotOutputConfig{"review": {Type: "review/v1"}, "opaque": {Type: "opaque/v1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if builder == nil || len(builder.InputMountPaths) != 1 || builder.InputMountPaths[0] != "/work/change" || len(builder.OutputMountPaths) != 1 || builder.OutputMountPaths[0] != "/work/review" {
		t.Fatalf("builder projection = %#v", builder)
	}
	if calls := metadata.GetAuthorizedCallCount(); calls != 1 {
		t.Fatalf("GetAuthorized call count = %d, want exactly one fetch per input", calls)
	}
	if _, gotTeam, gotID := metadata.GetAuthorizedArgsForCall(0); gotTeam != 91 || gotID != ref.ID {
		t.Fatalf("GetAuthorized called with (team=%d, id=%v), want (91, %v)", gotTeam, gotID, ref.ID)
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
	// This catches a regression where the server-derived value a type like
	// repository-change/v1 needs (repository_id) is dropped instead of
	// forwarded verbatim from the sealed manifest ATC already holds.
	if string(authority.Inputs["change"].IntrinsicMetadata) != string(intrinsicMetadata) {
		t.Fatalf("authority.Inputs[change].IntrinsicMetadata = %s, want %s verbatim", authority.Inputs["change"].IntrinsicMetadata, intrinsicMetadata)
	}
}

// This catches a regression where an input type with no intrinsic metadata
// (an ordinary manifest with an empty field) fails authority construction
// instead of omitting the field cleanly.
func TestOutputBuilderAuthorityOmitsIntrinsicMetadataWhenManifestHasNone(t *testing.T) {
	digest := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	ref := snapshot.SnapshotRef{ID: 3, Type: "repository-change/v1", Digest: digest}
	inputs := snapshotInputBindings{order: []string{"change"}, refs: map[string]snapshot.SnapshotRef{"change": ref}}
	inputs.recordExposure("change", ref, "/work/change")
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(outputBuilderManifest(ref, nil), true, nil)

	builder, err := outputBuilderAuthority(context.Background(), 1, metadata, "/work", inputs,
		map[string]atc.SnapshotInputConfig{"change": {Type: "repository-change/v1"}},
		map[string]atc.SnapshotOutputConfig{"review": {Type: "review/v1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var authority outputbuilder.NodeAuthority
	if err := json.Unmarshal(builder.Authority.Files[runtime.ManagedOutputBuilderAuthorityFile], &authority); err != nil {
		t.Fatal(err)
	}
	if authority.Inputs["change"].IntrinsicMetadata != nil {
		t.Fatalf("authority.Inputs[change].IntrinsicMetadata = %s, want it omitted cleanly", authority.Inputs["change"].IntrinsicMetadata)
	}
}

// This catches a regression where the server trusts a fetched manifest
// without reverifying it against the exact bound reference - the same recheck
// materializeSealedSnapshotArtifact and authorizedRequirementArtifact already
// perform elsewhere in this package before trusting a fetched manifest.
func TestOutputBuilderAuthorityRejectsIntrinsicMetadataForAMismatchedManifest(t *testing.T) {
	digest := snapshot.Digest("sha256:" + strings.Repeat("c", 64))
	ref := snapshot.SnapshotRef{ID: 5, Type: "repository-change/v1", Digest: digest}
	inputs := snapshotInputBindings{order: []string{"change"}, refs: map[string]snapshot.SnapshotRef{"change": ref}}
	inputs.recordExposure("change", ref, "/work/change")
	mismatched := outputBuilderManifest(ref, json.RawMessage(`{"repository_id":"sha256:cafe"}`))
	mismatched.Digest = snapshot.Digest("sha256:" + strings.Repeat("d", 64))
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(mismatched, true, nil)

	_, err := outputBuilderAuthority(context.Background(), 1, metadata, "/work", inputs,
		map[string]atc.SnapshotInputConfig{"change": {Type: "repository-change/v1"}},
		map[string]atc.SnapshotOutputConfig{"review": {Type: "review/v1"}},
	)
	if err == nil || !strings.Contains(err.Error(), `input "change"`) {
		t.Fatalf("outputBuilderAuthority() error = %v, want it to name input %q", err, "change")
	}
}

// This catches a regression where an oversized authority document (root_commits
// is the only unbounded field in sealed intrinsic metadata) is silently
// truncated, or only discovered later as an opaque node-side mount failure,
// instead of failing at build time with the offending input named.
func TestOutputBuilderAuthorityFailsClosedWhenEncodedDocumentExceedsTheMountCap(t *testing.T) {
	digest := snapshot.Digest("sha256:" + strings.Repeat("e", 64))
	ref := snapshot.SnapshotRef{ID: 11, Type: "repository-change/v1", Digest: digest}
	inputs := snapshotInputBindings{order: []string{"change"}, refs: map[string]snapshot.SnapshotRef{"change": ref}}
	inputs.recordExposure("change", ref, "/work/change")
	huge := json.RawMessage(`{"root_commits":"` + strings.Repeat("a", outputbuilder.MaxAuthorityFileBytes+1) + `"}`)
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(outputBuilderManifest(ref, huge), true, nil)

	builder, err := outputBuilderAuthority(context.Background(), 1, metadata, "/work", inputs,
		map[string]atc.SnapshotInputConfig{"change": {Type: "repository-change/v1"}},
		map[string]atc.SnapshotOutputConfig{"review": {Type: "review/v1"}},
	)
	if err == nil {
		t.Fatal("outputBuilderAuthority() accepted a document over the mount cap")
	}
	if !strings.Contains(err.Error(), `input "change"`) {
		t.Fatalf("outputBuilderAuthority() error = %v, want it to name the offending input %q", err, "change")
	}
	if builder != nil {
		t.Fatalf("builder = %#v, want nil on a failed build", builder)
	}
}

func TestOutputBuilderAuthorityIsAbsentWithoutBuiltInRecordOutput(t *testing.T) {
	builder, err := outputBuilderAuthority(context.Background(), 0, nil, "/work", snapshotInputBindings{refs: map[string]snapshot.SnapshotRef{}}, nil, map[string]atc.SnapshotOutputConfig{"opaque": {Type: "opaque/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if builder != nil {
		t.Fatalf("builder = %#v, want nil", builder)
	}
}

func outputBuilderManifest(ref snapshot.SnapshotRef, intrinsicMetadata json.RawMessage) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: ref.ID, Type: ref.Type, Digest: ref.Digest, ByteSize: 1024, FileCount: 1,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		IntrinsicMetadata: intrinsicMetadata, CreatedAt: time.Now().UTC(),
	}
}
