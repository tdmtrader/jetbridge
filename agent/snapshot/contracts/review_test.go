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

// TestReviewRecordRejectsDuplicateFindingIdentities is the named regression test
// for review/v1's entity-set identity rule: two findings that share an id. The
// declared core validator runs before ReviewBody.Validate, so the message an
// operator sees is the frozen field-path grammar's `body/findings/*/id`, not
// ValidateEntityIDs' `findings[0].id` spelling of the same rule.
func TestReviewRecordRejectsDuplicateFindingIdentities(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": subject})
	finding := contracts.Finding{
		ID: "f-1", Severity: "high", Blocking: true,
		Category: "correctness", Title: "unsafe race", Description: "writes race",
		Evidence: []contracts.Anchor{{
			Subject: "primary",
			Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: intPointer(12), End: intPointer(12)},
		}},
	}
	body := contracts.ReviewBody{
		Conclusion: "changes-required", Summary: "one blocking issue",
		Findings: []contracts.Finding{finding, finding},
	}
	validateReviewBody(t, body, subject, context, true, `body/findings/*/id: "f-1" is duplicate`)
}

// TestReviewRecordRejectsAnUndeclaredConclusion pins the conclusion enum. A value
// outside the declared set is refused by the core, which names the full permitted
// set so the rejection tells an operator what was allowed. No finding is needed:
// conclusion precedes findings in the body, so the enum check fires first.
func TestReviewRecordRejectsAnUndeclaredConclusion(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": subject})
	body := contracts.ReviewBody{Conclusion: "approved-with-love", Summary: "loved it", Findings: []contracts.Finding{}}
	validateReviewBody(t, body, subject, context, true, `body/conclusion: "approved-with-love" is not one of accept, changes-required, inconclusive`)
}

// A record with no primary and a record with two are the two ways to miss the
// one-primary shape, and they fail in different places: the declared subject
// shape refuses both, and ReviewBody.Validate refuses both again. Naming them
// separately keeps a change that loosens either bound from passing silently.
func TestReviewRecordRequiresExactlyOnePrimarySubject(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	first := snapshot.SnapshotRef{ID: 41, Type: changeType, Digest: recordDigest('b')}
	second := snapshot.SnapshotRef{ID: 42, Type: changeType, Digest: recordDigest('c')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": first, "other": second})
	body := contracts.ReviewBody{Conclusion: "accept", Summary: "nothing to change"}

	for name, subjects := range map[string][]contracts.Subject{
		"no primary subject": {
			contracts.SubjectFromInput("context-1", contracts.SubjectRoleContext, "change", first),
		},
		"two primary subjects": {
			contracts.SubjectFromInput("primary-1", contracts.SubjectRolePrimary, "change", first),
			contracts.SubjectFromInput("primary-2", contracts.SubjectRolePrimary, "other", second),
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, err := contracts.NewRecord(mustTypeRef(t, "review/v1"), subjects, body)
			if err != nil {
				t.Fatalf("NewRecord(): %v", err)
			}
			_, err = validateFiles(t, "review/v1", map[string][]byte{
				"record.json": marshalRecord(t, record),
			}, context)
			if err == nil || !strings.Contains(err.Error(), `"primary" role`) {
				t.Fatalf("review error = %v, want a primary-role cardinality rejection", err)
			}
		})
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
