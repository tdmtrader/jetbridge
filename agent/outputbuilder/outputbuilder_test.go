package outputbuilder

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// This catches a regression where an author can direct the builder to an
// undeclared filesystem location instead of the platform-bound output mount.
func TestNodeAuthorityRejectsOutputOutsideWorkRoot(t *testing.T) {
	root := t.TempDir()
	authority := NodeAuthority{
		WorkRoot: root,
		Outputs: map[string]OutputAuthority{
			"review": {Port: snapshot.Port{Name: "review", Type: "review/v1"}, MountRoot: t.TempDir()},
		},
	}

	if err := authority.Validate(); err == nil {
		t.Fatal("Validate() accepted an output mount outside the bound work root")
	}
}

// This catches a regression where output construction can bypass the declared
// port, schema preflight, or atomic replacement boundary.
func TestBuilderWritesOnlyDeclaredValidOutputAtomically(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := validReviewWrite()
	report, err := builder.WriteOutput(context.Background(), request)
	if err != nil || !report.Valid {
		t.Fatalf("WriteOutput() = %+v, %v; want valid report", report, err)
	}
	before, err := os.ReadFile(filepath.Join(authority.Outputs["review"].MountRoot, "record.json"))
	if err != nil {
		t.Fatal(err)
	}

	request.Body = json.RawMessage(`{"conclusion":"accept","summary":""}`)
	report, err = builder.WriteOutput(context.Background(), request)
	if err != nil || report.Valid {
		t.Fatalf("invalid WriteOutput() = %+v, %v; want a non-valid report", report, err)
	}
	after, err := os.ReadFile(filepath.Join(authority.Outputs["review"].MountRoot, "record.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("invalid write changed published candidate: after=%q err=%v", after, err)
	}

	_, err = builder.WriteOutput(context.Background(), WriteRequest{Output: "undeclared"})
	if err == nil || !strings.Contains(err.Error(), "unknown output") {
		t.Fatalf("WriteOutput(undeclared) error = %v, want unknown output", err)
	}
}

// This catches a regression where a content request can escape the output
// mount or traverse a symlink to copy bytes outside the platform's authority.
func TestBuilderRejectsTraversalAndSymlinkContent(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authority.Outputs["review"].MountRoot, "source.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source.txt", filepath.Join(authority.Outputs["review"].MountRoot, "link.txt")); err != nil {
		t.Fatal(err)
	}

	for _, content := range []ContentRequest{
		{Source: "../outside", Destination: "content/a.txt"},
		{Source: "link.txt", Destination: "content/a.txt"},
		{Source: "source.txt", Destination: "../a.txt"},
	} {
		request := validReviewWrite()
		request.Content = []ContentRequest{content}
		report, writeErr := builder.WriteOutput(context.Background(), request)
		if writeErr == nil && report.Valid {
			t.Fatalf("WriteOutput(%+v) accepted unsafe content request", content)
		}
	}
}

// This catches a regression where cancellation is ignored and candidate bytes
// can be committed after the caller has withdrawn the operation.
func TestBuilderDoesNotWriteAfterCancellation(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = builder.WriteOutput(ctx, validReviewWrite())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteOutput(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(authority.Outputs["review"].MountRoot, "record.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled write created record.json: %v", err)
	}
}

// This catches a regression where the authoring preflight omits the same
// bounded content policy the final snapshot capture will enforce.
func TestBuilderPreflightEnforcesConfiguredContentLimit(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir(), MaxContentBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.WriteOutput(context.Background(), validReviewWrite())
	if err != nil || report.Valid {
		t.Fatalf("WriteOutput() = %+v, %v; want an invalid bounded report", report, err)
	}
	if _, err := os.Stat(filepath.Join(authority.Outputs["review"].MountRoot, "record.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("over-limit candidate was committed: %v", err)
	}
}

// This catches a regression where a zero-value builder silently disables the
// final snapshot service's default limits.
func TestBuilderUsesSnapshotDefaultsWhenLimitsAreUnset(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if builder.canonicalizer.MaxEntries != snapshot.DefaultMaxSnapshotEntries || builder.canonicalizer.MaxContentBytes != snapshot.DefaultMaxSnapshotContentBytes {
		t.Fatalf("builder limits = (%d, %d), want final-capture defaults", builder.canonicalizer.MaxEntries, builder.canonicalizer.MaxContentBytes)
	}
}

// This catches a regression where a new candidate that has no content retains
// stale, previously authored content outside the validated stage.
func TestBuilderRemovesStaleContentWhenStageHasNone(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(authority.Outputs["review"].MountRoot, "content", "stale.txt")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report, err := builder.WriteOutput(context.Background(), validReviewWrite()); err != nil || !report.Valid {
		t.Fatalf("WriteOutput() = %+v, %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(authority.Outputs["review"].MountRoot, "content")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale content remains after zero-content write: %v", err)
	}
}

// This catches a regression where the mounted authority file is writable by
// the agent that is meant to be constrained by it.
func TestLoadAuthorityRejectsWritableAuthorityFile(t *testing.T) {
	authority := validAuthority(t)
	encoded, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "authority.json")
	if err := os.WriteFile(name, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthority(name); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("LoadAuthority(writable) error = %v, want read-only rejection", err)
	}
}

// This catches a regression where repository-change preflight receives only
// declarations but no server-bound archive reader for its exact base input.
func TestValidationContextOpensTheExactMountedInputArchive(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := builder.validationContext().OpenInput(context.Background(), "base")
	if err != nil {
		t.Fatalf("OpenInput(base) error = %v", err)
	}
	tree, captureErr := (snapshot.Canonicalizer{TempDir: t.TempDir()}).Capture(context.Background(), reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if tree.Digest != authority.Inputs["base"].Ref.Digest {
		t.Fatalf("opened digest = %s, want exact authority digest %s", tree.Digest, authority.Inputs["base"].Ref.Digest)
	}
}

// This catches a replacement race where a path check succeeds but a later
// write is redirected through the attacker-controlled replacement directory.
func TestBuilderKeepsWritingThroughItsBoundOutputRootAfterPathReplacement(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(authority.WorkRoot, "review-bound")
	if err := os.Rename(authority.Outputs["review"].MountRoot, moved); err != nil {
		t.Fatal(err)
	}
	forged := t.TempDir()
	if err := os.Symlink(forged, authority.Outputs["review"].MountRoot); err != nil {
		t.Fatal(err)
	}
	if report, err := builder.WriteOutput(context.Background(), validReviewWrite()); err != nil || !report.Valid {
		t.Fatalf("WriteOutput() = %+v, %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "record.json")); err != nil {
		t.Fatalf("bound root was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(forged, "record.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement root received output: %v", err)
	}
}

// This catches a regression where author preflight becomes sealing authority:
// the final validator must reopen and reject bytes changed after the builder
// returns a successful preflight report.
func TestFinalValidatorIndependentlyRejectsPostBuilderMutation(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := builder.WriteOutput(context.Background(), validReviewWrite()); err != nil || !report.Valid {
		t.Fatalf("WriteOutput() = %+v, %v", report, err)
	}
	if err := os.WriteFile(filepath.Join(authority.Outputs["review"].MountRoot, "record.json"), []byte(`{"type":"review/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(authority.Outputs["review"].MountRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	validator, err := registry.Lookup("review/v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.AdmitForSeal(context.Background(), root, snapshot.ValidationContext{}); err == nil {
		t.Fatal("final validator accepted bytes mutated after builder preflight")
	}
}

// This catches a regression where describe_output tells the node its
// declared inputs but drops the sealed intrinsic metadata a type like
// repository-change/v1 must carry as repository_id - forcing the private
// server-side hash to be re-implemented in the node's own prompt.
func TestDescribeOutputReturnsEachInputsIntrinsicMetadata(t *testing.T) {
	authority := validAuthority(t)
	base := authority.Inputs["base"]
	base.IntrinsicMetadata = json.RawMessage(`{"repository_id":"sha256:deadbeef"}`)
	authority.Inputs["base"] = base

	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	description, err := builder.DescribeOutput(context.Background(), "review")
	if err != nil {
		t.Fatalf("DescribeOutput() error = %v", err)
	}
	if len(description.Inputs) != 1 || description.Inputs[0].Name != "base" {
		t.Fatalf("DescribeOutput().Inputs = %+v, want exactly the declared base input", description.Inputs)
	}
	if string(description.Inputs[0].IntrinsicMetadata) != string(base.IntrinsicMetadata) {
		t.Fatalf("DescribeOutput().Inputs[0].IntrinsicMetadata = %s, want %s verbatim", description.Inputs[0].IntrinsicMetadata, base.IntrinsicMetadata)
	}
}

// This catches a regression where an input type with no intrinsic metadata
// (or an authority built before this field existed) fails DescribeOutput
// instead of omitting the field cleanly.
func TestDescribeOutputOmitsIntrinsicMetadataWhenAbsent(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	description, err := builder.DescribeOutput(context.Background(), "review")
	if err != nil {
		t.Fatalf("DescribeOutput() error = %v", err)
	}
	if len(description.Inputs) != 1 {
		t.Fatalf("DescribeOutput().Inputs = %+v, want exactly one input", description.Inputs)
	}
	if description.Inputs[0].IntrinsicMetadata != nil {
		t.Fatalf("DescribeOutput().Inputs[0].IntrinsicMetadata = %s, want it omitted cleanly", description.Inputs[0].IntrinsicMetadata)
	}
}

func validAuthority(t *testing.T) NodeAuthority {
	t.Helper()
	work := t.TempDir()
	input := filepath.Join(work, "base")
	output := filepath.Join(work, "review")
	if err := os.Mkdir(input, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := canonicalEmptyDigest(t)
	return NodeAuthority{
		WorkRoot: work,
		Inputs: map[string]InputAuthority{"base": {
			Ref: snapshot.SnapshotRef{ID: 1, Type: "repository/v1", Digest: digest}, MountRoot: input,
			Exposure: snapshot.FullTreeExposure(input, digest),
		}},
		Outputs: map[string]OutputAuthority{"review": {
			Port: snapshot.Port{Name: "review", Type: "review/v1"}, MountRoot: output,
		}},
	}
}

func canonicalEmptyDigest(t *testing.T) snapshot.Digest {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := (snapshot.Canonicalizer{TempDir: t.TempDir()}).Capture(context.Background(), bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	return tree.Digest
}

func validReviewWrite() WriteRequest {
	return WriteRequest{
		Output:   "review",
		Subjects: []SubjectRequest{{ID: "repository", Role: contracts.SubjectRolePrimary, Input: "base"}},
		Body:     json.RawMessage(`{"conclusion":"accept","summary":"ready","findings":[]}`),
	}
}
