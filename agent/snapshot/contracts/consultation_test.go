package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestConsultationRecordValidatesStructuredAdvice(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 91, Type: "repository/v1", Digest: recordDigest('c')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"repository": subject})
	valid := contracts.ConsultationBody{
		Answer: "Keep the compatibility endpoint for one release.",
		Claims: []contracts.ConsultationClaim{{
			ID:        "compatibility-risk",
			Statement: "Removing it now breaks existing callers.",
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "file-lines", Path: "api/openapi.yaml", Start: intPointer(4), End: intPointer(8)},
			}},
		}},
		Assumptions:     []string{"Existing callers have not all migrated."},
		Uncertainties:   []string{"Current endpoint traffic is unknown."},
		Recommendations: []string{"Measure endpoint use before removal."},
	}
	validateConsultationBody(t, valid, subject, context, false, "")

	tests := []struct {
		name  string
		setup func(*contracts.ConsultationBody)
		want  string
	}{
		{"missing answer", func(body *contracts.ConsultationBody) { body.Answer = " " }, "answer"},
		{"duplicate claim", func(body *contracts.ConsultationBody) {
			body.Claims = append(body.Claims, body.Claims[0])
		}, "duplicate"},
		{"unknown evidence subject", func(body *contracts.ConsultationBody) {
			body.Claims[0].Evidence[0].Subject = "missing"
		}, "missing"},
		{"blank assumption", func(body *contracts.ConsultationBody) {
			body.Assumptions = append(body.Assumptions, " ")
		}, "assumptions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := valid
			body.Claims = append([]contracts.ConsultationClaim(nil), valid.Claims...)
			body.Claims[0].Evidence = append([]contracts.Anchor(nil), valid.Claims[0].Evidence...)
			body.Assumptions = append([]string(nil), valid.Assumptions...)
			tc.setup(&body)
			validateConsultationBody(t, body, subject, context, true, tc.want)
		})
	}
}

func validateConsultationBody(
	t *testing.T,
	body contracts.ConsultationBody,
	subject snapshot.SnapshotRef,
	context snapshot.ValidationContext,
	wantError bool,
	want string,
) {
	t.Helper()
	record, err := contracts.NewRecord(
		mustTypeRef(t, "consultation/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "repository", subject,
		)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	_, err = validateFiles(t, "consultation/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, context)
	if !wantError && err != nil {
		t.Fatalf("valid consultation error = %v", err)
	}
	if wantError && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("consultation error = %v, want %q", err, want)
	}
}
