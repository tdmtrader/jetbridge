package judge_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/functions/judge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	measurementsType = snapshot.TypeRef("measurements/v1")
	reviewType       = snapshot.TypeRef("review/v1")
)

func TestMeasureDerivesTheClosedMetricSetFromTheCandidateRecord(t *testing.T) {
	root, ref := candidateMount(t, contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "one blocking defect and two notes",
		Findings: []contracts.Finding{
			{
				ID: "F-1", Severity: "critical", Blocking: true, Category: "correctness",
				Title: "unsynchronized write", Description: "two goroutines write the same map",
				Evidence: []contracts.Anchor{fileAnchor("main.go", 12)},
			},
			{
				ID: "F-2", Severity: "low", Category: "style",
				Title: "shadowed variable", Description: "err is shadowed",
				Evidence: []contracts.Anchor{fileAnchor("main.go", 40)},
			},
			{
				ID: "F-3", Severity: "observation", Category: "docs",
				Title: "comment is stale", Description: "the comment predates the rename",
			},
		},
	})

	record := measure(t, root, ref)

	if record.Body.Conclusion != "measured" {
		t.Fatalf("conclusion = %q, want measured", record.Body.Conclusion)
	}
	want := map[string]float64{
		"review.conclusion.accept":             0,
		"review.conclusion.changes-required":   1,
		"review.conclusion.inconclusive":       0,
		"review.findings.blocking":             1,
		"review.findings.severity.critical":    1,
		"review.findings.severity.high":        0,
		"review.findings.severity.low":         1,
		"review.findings.severity.medium":      0,
		"review.findings.severity.observation": 1,
		"review.findings.total":                3,
		"review.summary.characters":            33,
	}
	got := make(map[string]float64, len(record.Body.Metrics))
	for _, metric := range record.Body.Metrics {
		got[metric.ID] = metric.Value
	}
	if len(got) != len(want) {
		t.Fatalf("metric ids = %v, want exactly %d metrics", got, len(want))
	}
	for id, value := range want {
		if got[id] != value {
			t.Errorf("metric %q = %v, want %v", id, got[id], value)
		}
	}

	// Every metric must be anchored to the candidate subject, or a scorecard
	// number cannot be traced back to the field it came from.
	for _, metric := range record.Body.Metrics {
		if len(metric.Evidence) != 1 {
			t.Fatalf("metric %q evidence = %+v, want exactly one anchor", metric.ID, metric.Evidence)
		}
		anchor := metric.Evidence[0]
		if anchor.Subject != "candidate" || anchor.Locator.Kind != "json-pointer" ||
			!strings.HasPrefix(anchor.Locator.Pointer, "/body/") {
			t.Errorf("metric %q anchor = %+v, want a json-pointer into the candidate body", metric.ID, anchor)
		}
	}
	if len(record.Subjects) != 1 {
		t.Fatalf("subjects = %+v, want exactly the candidate", record.Subjects)
	}
	subject := record.Subjects[0]
	if subject.ID != "candidate" || subject.Role != contracts.SubjectRolePrimary ||
		subject.Input != "candidate" || subject.Type != reviewType || subject.Digest != ref.Digest {
		t.Fatalf("subject = %+v, want the candidate bound at its declared port", subject)
	}
}

// An accepted review with no findings is the boundary case the contract allows
// and the scorecard has to be able to read: every metric must still be present,
// because a metric that appears only sometimes cannot be paired across cells.
func TestMeasureEmitsEveryMetricForAFindingFreeAcceptance(t *testing.T) {
	root, ref := candidateMount(t, contracts.ReviewBody{
		Conclusion: "accept",
		Summary:    "no defects found",
	})

	record := measure(t, root, ref)

	if len(record.Body.Metrics) != 11 {
		t.Fatalf("metrics = %d, want the complete closed set", len(record.Body.Metrics))
	}
	for _, metric := range record.Body.Metrics {
		switch metric.ID {
		case "review.conclusion.accept":
			if metric.Value != 1 {
				t.Errorf("accept indicator = %v", metric.Value)
			}
		case "review.summary.characters":
			if metric.Value != 16 {
				t.Errorf("summary characters = %v", metric.Value)
			}
		default:
			if metric.Value != 0 {
				t.Errorf("metric %q = %v, want 0", metric.ID, metric.Value)
			}
		}
		if metric.Unit == "indicator" && (metric.Minimum == nil || metric.Maximum == nil || *metric.Minimum != 0 || *metric.Maximum != 1) {
			t.Errorf("indicator %q is not bounded to [0,1]: %+v", metric.ID, metric)
		}
		if metric.Unit != "indicator" && (metric.Minimum != nil || metric.Maximum != nil) {
			t.Errorf("metric %q declares bounds it cannot honor: %+v", metric.ID, metric)
		}
	}
}

// Determinism is the property that makes this function usable as an evaluator:
// the same candidate bytes must produce the same measurements bytes, or every
// paired comparison inherits the evaluator's own noise.
func TestMeasureIsDeterministicOverTheSameCandidate(t *testing.T) {
	body := contracts.ReviewBody{
		Conclusion: "inconclusive",
		Summary:    "the diff could not be resolved against the base",
	}
	firstRoot, firstRef := candidateMount(t, body)
	secondRoot, secondRef := candidateMount(t, body)

	first, err := json.Marshal(measure(t, firstRoot, firstRef))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(measure(t, secondRoot, secondRef))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("measurements differ between runs:\n%s\n%s", first, second)
	}
}

// The pod copies the platform's declared identity through instead of stamping
// its own compiled digest, because the agent-runner image and the web node are
// released independently.
func TestMeasureCopiesTheDeclaredRecordIdentityVerbatim(t *testing.T) {
	root, ref := candidateMount(t, contracts.ReviewBody{Conclusion: "accept", Summary: "clean"})
	declared := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	current, found := contracts.SchemaDigestFor(measurementsType)
	if !found || declared == current {
		t.Fatalf("test needs a schema digest that differs from the compiled one (%q)", current)
	}

	record, err := judge.Measure(context.Background(), judge.Request{
		Candidate: ref, CandidateInput: "candidate", CandidateRoot: root,
		MeasurementsAuthority: judge.RecordAuthority{Type: measurementsType, Schema: declared},
	})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if record.Schema != declared {
		t.Fatalf("schema = %q, want the declared digest %q", record.Schema, declared)
	}
}

func TestMeasureRejectsAnythingItCannotHonestlyMeasure(t *testing.T) {
	root, ref := candidateMount(t, contracts.ReviewBody{Conclusion: "accept", Summary: "clean"})
	schema, _ := contracts.SchemaDigestFor(measurementsType)
	valid := judge.Request{
		Candidate: ref, CandidateInput: "candidate", CandidateRoot: root,
		MeasurementsAuthority: judge.RecordAuthority{Type: measurementsType, Schema: schema},
	}

	tests := []struct {
		name    string
		mutate  func(*judge.Request)
		message string
	}{
		{
			name:    "candidate is not a review",
			mutate:  func(request *judge.Request) { request.Candidate.Type = snapshot.TypeRef("diagnosis/v1") },
			message: "exact type review/v1",
		},
		{
			name:    "no declared port",
			mutate:  func(request *judge.Request) { request.CandidateInput = "" },
			message: "candidate input port is required",
		},
		{
			name:    "no declared measurements identity",
			mutate:  func(request *judge.Request) { request.MeasurementsAuthority = judge.RecordAuthority{} },
			message: "measurements type must be exactly",
		},
		{
			name:    "declared measurements schema is malformed",
			mutate:  func(request *judge.Request) { request.MeasurementsAuthority.Schema = "not-a-digest" },
			message: "measurements schema",
		},
		{
			name:    "candidate mount holds no record",
			mutate:  func(request *judge.Request) { request.CandidateRoot = t.TempDir() },
			message: "not a readable review/v1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			_, err := judge.Measure(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Measure() error = %v, want one mentioning %q", err, test.message)
			}
		})
	}
}

// A candidate whose record.json is not a valid review/v1 is a contract failure,
// not a measurement: the function refuses rather than emitting zeros that a
// scorecard would read as a real observation.
func TestMeasureRefusesACandidateThatFailsItsOwnContract(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "record.json"), []byte(`{"record_version":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	schema, _ := contracts.SchemaDigestFor(measurementsType)
	_, err := judge.Measure(context.Background(), judge.Request{
		Candidate:             snapshot.SnapshotRef{ID: 1, Type: reviewType, Digest: digest("c")},
		CandidateInput:        "candidate",
		CandidateRoot:         root,
		MeasurementsAuthority: judge.RecordAuthority{Type: measurementsType, Schema: schema},
	})
	if err == nil || !strings.Contains(err.Error(), "not a readable review/v1") {
		t.Fatalf("Measure() error = %v", err)
	}
}

// The bytes this function writes are the bytes the web node re-reads when it
// builds a scorecard, so they must pass the platform's own read-time gate:
// envelope, declared schema, and body invariants together.
func TestWriteMeasurementsEmitsARecordTheReadTimeGateAccepts(t *testing.T) {
	root, ref := candidateMount(t, contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "one blocking defect",
		Findings: []contracts.Finding{{
			ID: "F-1", Severity: "high", Blocking: true, Category: "correctness",
			Title: "nil dereference", Description: "the pointer is never checked",
			Evidence: []contracts.Anchor{fileAnchor("main.go", 3)},
		}},
	})
	record := measure(t, root, ref)

	output := t.TempDir()
	if err := judge.WriteMeasurements(context.Background(), output, record); err != nil {
		t.Fatalf("WriteMeasurements: %v", err)
	}

	sealed, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	stored, err := contracts.ReadSealedMeasurementsRecord(context.Background(), sealed)
	if err != nil {
		t.Fatalf("ReadSealedMeasurementsRecord: %v", err)
	}
	if stored.Type != measurementsType || len(stored.Body.Metrics) != len(record.Body.Metrics) {
		t.Fatalf("stored record = %+v", stored)
	}
	if err := stored.Body.ValidateDetached(); err != nil {
		t.Fatalf("detached body: %v", err)
	}
}

func TestWriteMeasurementsRefusesAnEnvelopeItIsTheAuthorityOn(t *testing.T) {
	root, ref := candidateMount(t, contracts.ReviewBody{Conclusion: "accept", Summary: "clean"})
	valid := measure(t, root, ref)

	tests := []struct {
		name    string
		mutate  func(*contracts.Record[contracts.MeasurementsBody])
		message string
	}{
		{
			name:    "wrong record version",
			mutate:  func(record *contracts.Record[contracts.MeasurementsBody]) { record.RecordVersion = "9.9.9" },
			message: "record_version",
		},
		{
			name: "wrong contract type",
			mutate: func(record *contracts.Record[contracts.MeasurementsBody]) {
				record.Type = snapshot.TypeRef("review/v1")
			},
			message: "record type must be exactly",
		},
		{
			name: "body disagrees with its own conclusion",
			mutate: func(record *contracts.Record[contracts.MeasurementsBody]) {
				record.Body.Conclusion = "not-applicable"
			},
			message: "not-applicable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			record.Body.Metrics = append([]contracts.Measurement(nil), valid.Body.Metrics...)
			test.mutate(&record)
			err := judge.WriteMeasurements(context.Background(), t.TempDir(), record)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("WriteMeasurements() error = %v, want one mentioning %q", err, test.message)
			}
		})
	}
}

func measure(t *testing.T, root string, ref snapshot.SnapshotRef) contracts.Record[contracts.MeasurementsBody] {
	t.Helper()
	schema, found := contracts.SchemaDigestFor(measurementsType)
	if !found {
		t.Fatal("measurements/v1 has no compiled schema digest")
	}
	record, err := judge.Measure(context.Background(), judge.Request{
		Candidate: ref, CandidateInput: "candidate", CandidateRoot: root,
		MeasurementsAuthority: judge.RecordAuthority{Type: measurementsType, Schema: schema},
	})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	return record
}

// candidateMount lays out one sealed review/v1 exactly as a task mount presents
// it: record.json at the root of its own directory.
func candidateMount(t *testing.T, body contracts.ReviewBody) (string, snapshot.SnapshotRef) {
	t.Helper()
	reviewed := snapshot.SnapshotRef{ID: 7, Type: snapshot.TypeRef("repository/v1"), Digest: digest("a")}
	record, err := contracts.NewRecord(
		reviewType,
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
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "record.json"), append(payload, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return root, snapshot.SnapshotRef{ID: 11, Type: reviewType, Digest: digest("d")}
}

func fileAnchor(path string, line int) contracts.Anchor {
	return contracts.Anchor{
		Subject: "primary",
		Locator: contracts.Locator{Kind: "file-lines", Path: path, Start: &line, End: &line},
	}
}

func digest(fill string) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(fill, 64))
}
