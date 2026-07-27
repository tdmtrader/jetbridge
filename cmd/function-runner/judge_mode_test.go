package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// TestJudgeModeSealsMeasurementsForADeclaredCandidate drives the mode exactly
// the way a task pod does: bare artifact names beneath one working directory,
// and the input/output contract identities supplied only through the
// environment the platform publishes.
func TestJudgeModeSealsMeasurementsForADeclaredCandidate(t *testing.T) {
	root := t.TempDir()
	candidateDigest := judgeCandidateMount(t, root, contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "one blocking defect",
		Findings: []contracts.Finding{{
			ID: "F-1", Severity: "high", Blocking: true, Category: "correctness",
			Title: "nil dereference", Description: "the pointer is never checked",
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: judgeLine(4), End: judgeLine(4)},
			}},
		}},
	})
	if err := os.MkdirAll(filepath.Join(root, "measurements"), 0700); err != nil {
		t.Fatal(err)
	}
	declaredSchema := judgeDeclarePorts(t, "candidate", candidateDigest, "measurements")

	var stdout, stderr strings.Builder
	if code := runCLI(context.Background(), []string{
		"judge", "--root", root, "--candidate", "candidate", "--output", "measurements",
	}, &stdout, &stderr); code != exitOK {
		t.Fatalf("judge exit = %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review.findings.blocking = 1") {
		t.Fatalf("judge stdout = %q, want the derived metrics", stdout.String())
	}

	sealed, err := os.OpenRoot(filepath.Join(root, "measurements"))
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	// ReadSealedMeasurementsRecord is the exact gate the web node runs when it
	// builds a scorecard (atc/atccmd/agent_experiments.go), so passing it here is
	// the end-to-end proof that the emitted bytes are readable measurements.
	record, err := contracts.ReadSealedMeasurementsRecord(context.Background(), sealed)
	if err != nil {
		t.Fatalf("emitted record is not a readable measurements/v1: %v", err)
	}
	if record.Schema != declaredSchema {
		t.Fatalf("schema = %q, want the declared %q", record.Schema, declaredSchema)
	}
	if len(record.Subjects) != 1 ||
		record.Subjects[0].ID != "candidate" ||
		record.Subjects[0].Input != "candidate" ||
		record.Subjects[0].Digest != candidateDigest {
		t.Fatalf("subjects = %+v, want the declared candidate identity copied verbatim", record.Subjects)
	}
	if record.Body.Conclusion != "measured" || len(record.Body.Metrics) == 0 {
		t.Fatalf("body = %+v", record.Body)
	}
}

// Without the platform's declaration there is no honest identity to bind the
// measurements to, and no compiled default could supply one, so the mode refuses
// rather than inventing a subject.
func TestJudgeModeRefusesAnUndeclaredCandidate(t *testing.T) {
	root := t.TempDir()
	judgeCandidateMount(t, root, contracts.ReviewBody{Conclusion: "accept", Summary: "clean"})

	var stdout, stderr strings.Builder
	if code := runCLI(context.Background(), []string{
		"judge", "--root", root, "--candidate", "candidate", "--output", "measurements",
	}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("judge exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "AGENT_INPUT_CANDIDATE_SNAPSHOT_TYPE") {
		t.Fatalf("stderr = %q, want it to name the missing declaration", stderr.String())
	}
}

func TestJudgeModeRejectsMalformedInvocations(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing mounts", args: []string{"judge"}},
		{name: "missing output", args: []string{"judge", "--candidate=candidate"}},
		{name: "aliased mounts", args: []string{"judge", "--candidate=same", "--output=same"}},
		{name: "nested mount name", args: []string{"judge", "--candidate=a/b", "--output=measurements"}},
		{name: "unexpected argument", args: []string{"judge", "--candidate=c", "--output=m", "extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := runCLI(context.Background(), test.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitUsage, stderr.String())
			}
		})
	}
}

// judgeCandidateMount writes one sealed review/v1 into the candidate mount and
// returns the digest a platform would declare for it. The digest is arbitrary
// here for the same reason it is opaque to the pod: nothing in the pod derives
// it, and the web node re-checks it against its own view when it seals.
func judgeCandidateMount(t *testing.T, root string, body contracts.ReviewBody) snapshot.Digest {
	t.Helper()
	reviewed := snapshot.SnapshotRef{
		ID: 3, Type: snapshot.TypeRef("repository/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "after", reviewed)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		t.Fatalf("review fixture is invalid: %v", err)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "candidate")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "record.json"), append(payload, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return snapshot.Digest("sha256:" + strings.Repeat("e", 64))
}

// judgeDeclarePorts publishes exactly the rows atc/exec/record_authority_env.go
// sets for a typed input and a record output port.
func judgeDeclarePorts(t *testing.T, candidatePort string, candidateDigest snapshot.Digest, outputPort string) snapshot.Digest {
	t.Helper()
	inputPrefix := "AGENT_INPUT_" + authorityEnvPort(candidatePort)
	t.Setenv(inputPrefix+"_SNAPSHOT_TYPE", "review/v1")
	t.Setenv(inputPrefix+"_SNAPSHOT_DIGEST", candidateDigest.String())

	schema, found := contracts.SchemaDigestFor(measurementsType)
	if !found {
		t.Fatalf("SchemaDigestFor(%s) not found", measurementsType)
	}
	outputPrefix := "AGENT_OUTPUT_" + authorityEnvPort(outputPort)
	t.Setenv(outputPrefix+"_RECORD_TYPE", measurementsType.String())
	t.Setenv(outputPrefix+"_RECORD_SCHEMA", schema.String())
	return schema
}

func judgeLine(value int) *int { return &value }
