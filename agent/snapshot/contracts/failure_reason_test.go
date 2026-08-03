package contracts_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// Every record and tree reason must be reachable by driving a REAL registry
// validator over a REAL tree. The reasons in this table are the rules the
// node-authoring guide asks an author to satisfy, and each case is the mistake
// an author actually makes.
//
// The pairing matters as much as the coverage: two cases that both produced
// `record_field_value_not_allowed` would tell a caller nothing the old bare
// `validation_failed` did not, so each reason is claimed by exactly one rule
// family here.
func TestEveryRecordContractReasonIsReachableThroughItsValidator(t *testing.T) {
	changeRef := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	logsRef := snapshot.SnapshotRef{ID: 51, Type: "log-bundle/v1", Digest: recordDigest('b')}
	reviewContext := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": changeRef})
	diagnosisContext := validationContextFor(t, map[string]snapshot.SnapshotRef{"logs": logsRef})
	emptyContext := emptyValidationContext(t)

	tests := []struct {
		name    string
		rawType string
		files   func(*testing.T) map[string][]byte
		context snapshot.ValidationContext
		reason  snapshot.ValidationFailureReason
	}{
		{
			name:    "record.json is absent from the tree",
			rawType: "review/v1",
			files:   func(*testing.T) map[string][]byte { return map[string][]byte{"notes.md": []byte("hello")} },
			context: reviewContext,
			reason:  snapshot.RecordDocumentMissing,
		},
		{
			name:    "record.json is not JSON",
			rawType: "review/v1",
			files: func(*testing.T) map[string][]byte {
				return map[string][]byte{"record.json": []byte("{ this is not json")}
			},
			context: reviewContext,
			reason:  snapshot.RecordDocumentMalformed,
		},
		{
			name:    "the envelope pins a record_version the platform does not issue",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return map[string][]byte{"record.json": mutateRecordJSON(t, validReviewRecord(t, changeRef), func(raw map[string]any) {
					raw["record_version"] = "0.9.0"
				})}
			},
			context: reviewContext,
			reason:  snapshot.RecordEnvelopeInvalid,
		},
		{
			name:    "a subject binds to an input the step never exposed",
			rawType: "review/v1",
			files:   func(t *testing.T) map[string][]byte { return validReviewRecord(t, changeRef) },
			context: emptyContext,
			reason:  snapshot.RecordSubjectsInvalid,
		},
		{
			name:    "a required field is blank",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) { body.Summary = "   " })
			},
			context: reviewContext,
			reason:  snapshot.RecordFieldMissing,
		},
		{
			name:    "a field the pinning forbids is present",
			rawType: "diagnosis/v1",
			files: func(t *testing.T) map[string][]byte {
				return diagnosisRecordWith(t, logsRef, func(body *contracts.DiagnosisBody) {
					bound := 0.0
					body.Hypotheses[0].Confidence.Minimum = &bound
				})
			},
			context: diagnosisContext,
			reason:  snapshot.RecordFieldForbidden,
		},
		{
			name:    "a timestamp that is not RFC 3339",
			rawType: "work-item/v1",
			files: func(t *testing.T) map[string][]byte {
				return map[string][]byte{"work-item.json": marshalJSON(t, map[string]any{
					"schema_version": "1.0.0", "adapter": "jira", "external_id": "PROJ-1",
					"revision": "3", "captured_at": "yesterday", "title": "Fix it", "body": "please",
				})}
			},
			context: emptyContext,
			reason:  snapshot.RecordFieldTypeInvalid,
		},
		{
			name:    "a severity outside the closed vocabulary",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					body.Findings[0].Severity = "sev1"
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordFieldValueNotAllowed,
		},
		{
			name:    "a unit-interval score outside zero to one",
			rawType: "diagnosis/v1",
			files: func(t *testing.T) map[string][]byte {
				return diagnosisRecordWith(t, logsRef, func(body *contracts.DiagnosisBody) {
					body.Hypotheses[0].Confidence.Value = 1.2
				})
			},
			context: diagnosisContext,
			reason:  snapshot.RecordFieldOutOfRange,
		},
		{
			name:    "an id that is not an identifier",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					body.Findings[0].ID = "not an id!"
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordIdentifierInvalid,
		},
		{
			name:    "two findings sharing one id",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					body.Findings = append(body.Findings, body.Findings[0])
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordEntityIDDuplicate,
		},
		{
			name:    "findings out of lexicographic id order",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					earlier := body.Findings[0]
					earlier.ID = "A-1"
					body.Findings = append(body.Findings, earlier)
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordEntityIDsUnsorted,
		},
		{
			name:    "evidence anchored to a subject the record never declared",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					body.Findings[0].Evidence[0].Subject = "nobody"
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordAnchorInvalid,
		},
		{
			name:    "accept alongside a blocking finding",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) { body.Conclusion = "accept" })
			},
			context: reviewContext,
			reason:  snapshot.RecordConclusionInconsistent,
		},
		{
			name:    "a high finding that is not blocking",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					body.Conclusion = "inconclusive"
					body.Findings[0].Blocking = false
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordBlockingInconsistent,
		},
		{
			name:    "a non-observation finding with no evidence",
			rawType: "review/v1",
			files: func(t *testing.T) map[string][]byte {
				return reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
					body.Conclusion = "inconclusive"
					body.Findings[0].Severity = "low"
					body.Findings[0].Blocking = false
					body.Findings[0].Evidence = nil
				})
			},
			context: reviewContext,
			reason:  snapshot.RecordEvidenceMissing,
		},
		{
			name:    "hypothesis ranks with a gap",
			rawType: "diagnosis/v1",
			files: func(t *testing.T) map[string][]byte {
				return diagnosisRecordWith(t, logsRef, func(body *contracts.DiagnosisBody) {
					second := body.Hypotheses[0]
					second.ID = "network"
					second.Rank = 3
					body.Hypotheses = append(body.Hypotheses, second)
				})
			},
			context: diagnosisContext,
			reason:  snapshot.RecordRankInvalid,
		},
		{
			name:    "an action addressing a hypothesis the record does not contain",
			rawType: "diagnosis/v1",
			files: func(t *testing.T) map[string][]byte {
				return diagnosisRecordWith(t, logsRef, func(body *contracts.DiagnosisBody) {
					body.Actions[0].Addresses = []string{"missing"}
				})
			},
			context: diagnosisContext,
			reason:  snapshot.RecordReferenceUnknown,
		},
		{
			name:    "an opaque snapshot with an empty tree",
			rawType: "opaque/v1",
			files:   func(*testing.T) map[string][]byte { return map[string][]byte{} },
			context: emptyContext,
			reason:  snapshot.SnapshotTreeInvalid,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateFiles(t, test.rawType, test.files(t), test.context)
			if err == nil {
				t.Fatal("the validator accepted a tree its contract forbids")
			}
			var public *snapshot.PublicValidationFailure
			if !errors.As(err, &public) {
				t.Fatalf("rejection carries no public reason: %v", err)
			}
			if public.Reason() != test.reason {
				t.Fatalf("reason = %q, want %q (cause: %v)", public.Reason(), test.reason, err)
			}
			if public.PublicMessage() == "" {
				t.Fatal("a classified rejection has no public message")
			}
		})
	}
}

// A log bundle whose only regular file is its metadata is the other shape
// rejection, and it is a different validator from the opaque one.
func TestLogBundleWithoutALogFileIsAClassifiedTreeRejection(t *testing.T) {
	_, err := validateFiles(t, "log-bundle/v1", map[string][]byte{
		"metadata.json": marshalJSON(t, map[string]any{
			"schema_version": "1.0.0", "captured_at": "2026-07-22T12:00:00Z", "source": "runner",
		}),
	}, emptyValidationContext(t))
	var public *snapshot.PublicValidationFailure
	if !errors.As(err, &public) || public.Reason() != snapshot.SnapshotTreeInvalid {
		t.Fatalf("reason = %#v (err %v)", public, err)
	}
}

// The private cause has to survive alongside the public classification: it is
// what the server log gets, and losing it would trade one blind spot for
// another.
func TestClassifyingARecordRejectionKeepsItsDetailedCause(t *testing.T) {
	changeRef := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	_, err := validateFiles(t, "review/v1", reviewRecordWith(t, changeRef, func(body *contracts.ReviewBody) {
		body.Findings[0].Severity = "sev1"
	}), validationContextFor(t, map[string]snapshot.SnapshotRef{"change": changeRef}))
	if err == nil {
		t.Fatal("the validator accepted an unknown severity")
	}
	for _, detail := range []string{"body/findings/*/severity", "sev1"} {
		if !contains(err.Error(), detail) {
			t.Fatalf("the detailed cause lost %q: %v", detail, err)
		}
	}
}

func contains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

func validReviewBody() contracts.ReviewBody {
	return contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "one blocking issue",
		Findings: []contracts.Finding{{
			ID: "F-1", Severity: "high", Blocking: true,
			Category: "correctness", Title: "unsafe race", Description: "writes race",
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: intPointer(12), End: intPointer(12)},
			}},
		}},
	}
}

func validDiagnosisBody() contracts.DiagnosisBody {
	return contracts.DiagnosisBody{
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
}

func validReviewRecord(t *testing.T, subject snapshot.SnapshotRef) map[string][]byte {
	return reviewRecordWith(t, subject, func(*contracts.ReviewBody) {})
}

func reviewRecordWith(t *testing.T, subject snapshot.SnapshotRef, mutate func(*contracts.ReviewBody)) map[string][]byte {
	t.Helper()
	body := validReviewBody()
	mutate(&body)
	record, err := contracts.NewRecord(
		mustTypeRef(t, "review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "change", subject)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	return map[string][]byte{"record.json": marshalRecord(t, record)}
}

func diagnosisRecordWith(t *testing.T, subject snapshot.SnapshotRef, mutate func(*contracts.DiagnosisBody)) map[string][]byte {
	t.Helper()
	body := validDiagnosisBody()
	mutate(&body)
	record, err := contracts.NewRecord(
		mustTypeRef(t, "diagnosis/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "logs", subject)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	return map[string][]byte{"record.json": marshalRecord(t, record)}
}

// mutateRecordJSON edits the encoded envelope rather than the typed record,
// because the envelope rules under test are exactly the ones NewRecord refuses
// to let a caller break.
func mutateRecordJSON(t *testing.T, files map[string][]byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(files["record.json"], &raw); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	mutate(raw)
	return marshalJSON(t, raw)
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}
