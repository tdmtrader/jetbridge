package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestDiagnosisRecordValidatesRankedCausalAnalysis(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 51, Type: "log-bundle/v1", Digest: recordDigest('b')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"logs": subject})
	valid := contracts.DiagnosisBody{
		Summary:    "registry authentication failed",
		Conclusion: "identified",
		Hypotheses: []contracts.Hypothesis{{
			ID: "auth", Rank: 1, Statement: "the registry rejected the token",
			Confidence: contracts.Score{Value: 0.9, Scale: "unit-interval", Direction: "higher-is-better"},
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "log-lines", Path: "events.log", Start: intPointer(4), End: intPointer(8)},
			}},
		}},
		Actions: []contracts.DiagnosisAction{{
			ID: "refresh", Priority: "immediate", Description: "refresh the pull credential",
			Addresses: []string{"auth"},
		}},
	}
	validateDiagnosisBody(t, valid, subject, context, false, "")

	tests := []struct {
		name  string
		setup func(*contracts.DiagnosisBody)
		want  string
	}{
		{"identified without evidence", func(body *contracts.DiagnosisBody) { body.Hypotheses[0].Evidence = nil }, "evidence"},
		{"suspected without hypotheses", func(body *contracts.DiagnosisBody) {
			body.Conclusion = "suspected"
			body.Hypotheses = nil
			body.Actions = nil
		}, "hypotheses"},
		{"rank gap", func(body *contracts.DiagnosisBody) {
			second := body.Hypotheses[0]
			second.ID = "network"
			second.Rank = 3
			body.Hypotheses = append(body.Hypotheses, second)
		}, "contiguous"},
		{"invalid confidence", func(body *contracts.DiagnosisBody) { body.Hypotheses[0].Confidence.Value = 1.2 }, "confidence"},
		{"unknown action hypothesis", func(body *contracts.DiagnosisBody) { body.Actions[0].Addresses = []string{"missing"} }, "missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := valid
			body.Hypotheses = append([]contracts.Hypothesis(nil), valid.Hypotheses...)
			body.Hypotheses[0].Evidence = append([]contracts.Anchor(nil), valid.Hypotheses[0].Evidence...)
			body.Actions = append([]contracts.DiagnosisAction(nil), valid.Actions...)
			body.Actions[0].Addresses = append([]string(nil), valid.Actions[0].Addresses...)
			tc.setup(&body)
			validateDiagnosisBody(t, body, subject, context, true, tc.want)
		})
	}
}

func TestDiagnosisRecordAllowsEmptyInconclusiveAnalysis(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 51, Type: "log-bundle/v1", Digest: recordDigest('b')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"logs": subject})
	body := contracts.DiagnosisBody{
		Summary: "insufficient evidence", Conclusion: "inconclusive",
		Hypotheses: []contracts.Hypothesis{}, Actions: []contracts.DiagnosisAction{},
	}
	validateDiagnosisBody(t, body, subject, context, false, "")
}

func validateDiagnosisBody(
	t *testing.T,
	body contracts.DiagnosisBody,
	subject snapshot.SnapshotRef,
	context snapshot.ValidationContext,
	wantError bool,
	want string,
) {
	t.Helper()
	record, err := contracts.NewRecord(
		mustTypeRef(t, "diagnosis/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "logs", subject,
		)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	_, err = validateFiles(t, "diagnosis/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, context)
	if !wantError && err != nil {
		t.Fatalf("valid diagnosis error = %v", err)
	}
	if wantError && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("diagnosis error = %v, want %q", err, want)
	}
}
