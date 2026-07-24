package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestReviewRecordValidatesJudgmentAndBlockingSemantics(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": subject})
	valid := contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "one blocking issue",
		Findings: []contracts.Finding{{
			ID: "F-1", Severity: "high", Blocking: true,
			Category: "correctness", Title: "unsafe race", Description: "writes race",
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: intPointer(12), End: intPointer(12)},
			}},
			Recommendation: "synchronize the writes",
		}},
	}
	validateReviewBody(t, valid, subject, context, false, "")

	tests := []struct {
		name  string
		setup func(*contracts.ReviewBody)
		want  string
	}{
		{
			name: "changes required without blocking finding",
			setup: func(body *contracts.ReviewBody) {
				body.Findings[0].Blocking = false
				body.Findings[0].Severity = "low"
			},
			want: "blocking",
		},
		{
			name: "accept with blocking finding",
			setup: func(body *contracts.ReviewBody) {
				body.Conclusion = "accept"
			},
			want: "accept",
		},
		{
			name: "observation blocks",
			setup: func(body *contracts.ReviewBody) {
				body.Findings[0].Severity = "observation"
			},
			want: "observation",
		},
		{
			name: "high is advisory",
			setup: func(body *contracts.ReviewBody) {
				body.Findings[0].Blocking = false
			},
			want: "high",
		},
		{
			name: "issue has no evidence",
			setup: func(body *contracts.ReviewBody) {
				body.Findings[0].Evidence = nil
			},
			want: "evidence",
		},
		{
			name: "evidence names unknown subject",
			setup: func(body *contracts.ReviewBody) {
				body.Findings[0].Evidence[0].Subject = "missing"
			},
			want: "not declared",
		},
		{
			name: "findings are unsorted",
			setup: func(body *contracts.ReviewBody) {
				second := body.Findings[0]
				second.ID = "A-1"
				body.Findings = append(body.Findings, second)
			},
			want: "sorted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := valid
			body.Findings = append([]contracts.Finding(nil), valid.Findings...)
			body.Findings[0].Evidence = append([]contracts.Anchor(nil), valid.Findings[0].Evidence...)
			tc.setup(&body)
			validateReviewBody(t, body, subject, context, true, tc.want)
		})
	}
}

func TestReviewRecordAllowsEmptyAcceptAndInconclusiveFindings(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": subject})
	for _, conclusion := range []string{"accept", "inconclusive"} {
		body := contracts.ReviewBody{Conclusion: conclusion, Summary: "no findings", Findings: []contracts.Finding{}}
		validateReviewBody(t, body, subject, context, false, "")
	}
}

func validateReviewBody(
	t *testing.T,
	body contracts.ReviewBody,
	subject snapshot.SnapshotRef,
	context snapshot.ValidationContext,
	wantError bool,
	want string,
) {
	t.Helper()
	record, err := contracts.NewRecord(
		mustTypeRef(t, "review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", subject,
		)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	_, err = validateFiles(t, "review/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, context)
	if !wantError && err != nil {
		t.Fatalf("valid review error = %v", err)
	}
	if wantError && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("review error = %v, want %q", err, want)
	}
}
