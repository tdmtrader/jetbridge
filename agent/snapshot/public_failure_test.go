package snapshot_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
)

// everyPublicValidationReason is the closed set, spelled out rather than
// derived. The wire value of a reason is part of the HTTP contract, so a rename
// has to be a visible edit here and not a silent consequence of a constant being
// retyped somewhere else.
func everyPublicValidationReason() []struct {
	reason  snapshot.ValidationFailureReason
	wire    string
	message string
} {
	return []struct {
		reason  snapshot.ValidationFailureReason
		wire    string
		message string
	}{
		{snapshot.RepositoryMetadataMissing, "repository_metadata_missing", "repository metadata is incomplete"},
		{snapshot.RepositoryMetadataUnsafe, "repository_metadata_unsafe", "repository metadata contains an unsupported or unsafe setting"},
		{snapshot.RepositoryHistoryIncomplete, "repository_history_incomplete", "repository history is shallow or incomplete"},
		{snapshot.RepositoryObjectFormatUnsupported, "repository_object_format_unsupported", "repository object format is unsupported"},
		{snapshot.RepositoryGitlinksUnsupported, "repository_gitlinks_unsupported", "repositories containing submodule gitlinks are unsupported"},
		{snapshot.RepositoryDirty, "repository_dirty", "repository work tree and index must be clean"},
		{snapshot.RepositoryInvalid, "repository_invalid", "repository object graph is invalid or incomplete"},

		{snapshot.ArchivePathNotCanonical, "archive_path_not_canonical", "snapshot archive entry path is not canonical"},
		{snapshot.ArchivePathTooLong, "archive_path_too_long", "snapshot archive entry path is too long"},
		{snapshot.ArchivePathDuplicate, "archive_path_duplicate", "snapshot archive declares one entry path more than once"},
		{snapshot.ArchivePathParentInvalid, "archive_path_parent_invalid", "snapshot archive entry has a symlinked or non-directory parent"},
		{snapshot.ArchivePathCollides, "archive_path_collides", "snapshot archive entry paths collide on the extraction filesystem"},
		{snapshot.ArchiveEntryTypeUnsupported, "archive_entry_type_unsupported", "snapshot archive entry type is unsupported"},
		{snapshot.ArchiveEntryMetadataUnsupported, "archive_entry_metadata_unsupported", "snapshot archive entry carries unsupported or unsafe metadata"},
		{snapshot.ArchiveEntrySizeInvalid, "archive_entry_size_invalid", "snapshot archive entry declares an invalid content size"},
		{snapshot.ArchiveSymlinkTargetInvalid, "archive_symlink_target_invalid", "snapshot archive symlink target is not canonical"},
		{snapshot.ArchiveSymlinkEscapesRoot, "archive_symlink_escapes_root", "snapshot archive symlink target escapes the archive root"},
		{snapshot.ArchiveStreamUnreadable, "archive_stream_unreadable", "snapshot archive stream is not a readable tar"},

		{snapshot.RecordDocumentMissing, "record_document_missing", "a required snapshot document is missing or is not a regular file"},
		{snapshot.RecordDocumentMalformed, "record_document_malformed", "a snapshot document is not strict, well-formed JSON"},
		{snapshot.RecordEnvelopeInvalid, "record_envelope_invalid", "record envelope version, type, or schema digest is not accepted"},
		{snapshot.RecordSubjectsInvalid, "record_subjects_invalid", "record subject set does not satisfy its contract"},
		{snapshot.RecordFieldMissing, "record_field_missing", "a required record field is missing or blank"},
		{snapshot.RecordFieldForbidden, "record_field_forbidden", "a record field is present where its contract forbids it"},
		{snapshot.RecordFieldTypeInvalid, "record_field_type_invalid", "a record field does not have the shape its contract declares"},
		{snapshot.RecordFieldValueNotAllowed, "record_field_value_not_allowed", "a record field value is outside the closed set its contract allows"},
		{snapshot.RecordFieldOutOfRange, "record_field_out_of_range", "a record field value is outside the range its contract allows"},
		{snapshot.RecordIdentifierInvalid, "record_identifier_invalid", "a record identifier does not match the identifier grammar"},
		{snapshot.RecordEntityIDDuplicate, "record_entity_id_duplicate", "a record entity set contains a duplicate id"},
		{snapshot.RecordEntityIDsUnsorted, "record_entity_ids_unsorted", "a record entity set is not lexicographically sorted by id"},
		{snapshot.RecordAnchorInvalid, "record_anchor_invalid", "a record anchor does not resolve to a declared subject and locator"},
		{snapshot.RecordConclusionInconsistent, "record_conclusion_inconsistent", "the record conclusion contradicts the rest of the record"},
		{snapshot.RecordBlockingInconsistent, "record_blocking_inconsistent", "a finding severity and its blocking flag contradict each other"},
		{snapshot.RecordEvidenceMissing, "record_evidence_missing", "the record omits evidence its contract requires"},
		{snapshot.RecordRankInvalid, "record_rank_invalid", "record ranks are not unique and contiguous from one"},
		{snapshot.RecordReferenceUnknown, "record_reference_unknown", "the record references an entity it does not contain"},
		{snapshot.SnapshotTreeInvalid, "snapshot_tree_invalid", "the snapshot tree does not have the shape its type requires"},
	}
}

func TestEveryPublicValidationReasonHasAStableWireValueAndFixedMessage(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	for _, test := range everyPublicValidationReason() {
		if string(test.reason) != test.wire {
			t.Errorf("reason constant = %q, want wire value %q", test.reason, test.wire)
		}
		if _, duplicate := seen[test.wire]; duplicate {
			t.Errorf("reason %q is declared twice", test.wire)
		}
		seen[test.wire] = struct{}{}

		cause := errors.New("git stderr contains /tmp/private and token=secret")
		err := snapshot.NewPublicValidationFailure(test.reason, cause)
		var public *snapshot.PublicValidationFailure
		if !errors.As(err, &public) {
			t.Fatalf("%q did not produce a public failure: %v", test.wire, err)
		}
		if public.Reason() != test.reason {
			t.Errorf("reason = %q, want %q", public.Reason(), test.reason)
		}
		if public.PublicMessage() != test.message {
			t.Errorf("%q message = %q, want %q", test.wire, public.PublicMessage(), test.message)
		}
		if public.Entry() != "" {
			t.Errorf("%q carried an entry it was never given: %q", test.wire, public.Entry())
		}
		if !errors.Is(err, cause) {
			t.Errorf("%q lost its private cause", test.wire)
		}
		if !strings.Contains(err.Error(), cause.Error()) {
			t.Errorf("%q Error() dropped the private cause the server log needs: %q", test.wire, err.Error())
		}
	}
}

// An unregistered reason must stay unconstructible through BOTH constructors.
// The entry-carrying one is the newer door into the same struct, and a public
// failure carrying a reason no message table knows would reach a client as an
// empty message with a reason string nothing defines.
func TestUnregisteredReasonsAndNilCausesCannotManufactureAPublicFailure(t *testing.T) {
	t.Parallel()
	cause := errors.New("token=secret")
	for _, test := range []struct {
		name string
		err  error
	}{
		{"unknown reason", snapshot.NewPublicValidationFailure("invented_reason", cause)},
		{"unknown reason with entry", snapshot.NewPublicValidationFailureForEntry("invented_reason", "a/b", cause)},
		{"nil cause", snapshot.NewPublicValidationFailure(snapshot.RepositoryDirty, nil)},
		{"nil cause with entry", snapshot.NewPublicValidationFailureForEntry(snapshot.ArchivePathNotCanonical, "a/b", nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var public *snapshot.PublicValidationFailure
			if errors.As(test.err, &public) {
				t.Fatalf("manufactured a public failure: %#v", public)
			}
			if test.err == nil {
				t.Fatal("fail-safe path returned no error at all")
			}
		})
	}
}

func TestPublicFailureEntryIsBoundedSanitizedAndVisiblyTruncated(t *testing.T) {
	t.Parallel()
	marker := snapshot.PublicEntryTruncationMarker
	if marker == "" {
		t.Fatal("truncation must be visible, so the marker cannot be empty")
	}
	if snapshot.MaxPublicEntryBytes <= 0 || int64(snapshot.MaxPublicEntryBytes) >= snapshot.MaxSnapshotPathBytes/8 {
		t.Fatalf("MaxPublicEntryBytes = %d is not well under MaxSnapshotPathBytes = %d",
			snapshot.MaxPublicEntryBytes, snapshot.MaxSnapshotPathBytes)
	}

	tests := []struct {
		name  string
		entry string
		check func(*testing.T, string)
	}{
		{
			name:  "short paths survive verbatim",
			entry: "dir/sub/record.json",
			check: func(t *testing.T, got string) {
				if got != "dir/sub/record.json" {
					t.Fatalf("entry = %q, want it unchanged", got)
				}
			},
		},
		{
			name:  "an over-long path is truncated to the bound with a visible marker",
			entry: strings.Repeat("a", int(snapshot.MaxSnapshotPathBytes)+1),
			check: func(t *testing.T, got string) {
				if len(got) > snapshot.MaxPublicEntryBytes {
					t.Fatalf("entry is %d bytes, want at most %d", len(got), snapshot.MaxPublicEntryBytes)
				}
				if !strings.HasSuffix(got, marker) {
					t.Fatalf("entry %q was truncated silently, without the marker", got)
				}
				if strings.TrimSuffix(got, marker) != strings.Repeat("a", len(got)-len(marker)) {
					t.Fatalf("entry = %q, want a prefix of the original", got)
				}
			},
		},
		{
			name:  "control bytes and ANSI escapes never reach the client raw",
			entry: "dir/\x1b[31mred\x00\x07/file",
			check: func(t *testing.T, got string) {
				for _, character := range got {
					if unicode.In(character, unicode.C) {
						t.Fatalf("entry %q still carries %U", got, character)
					}
				}
				if strings.ContainsAny(got, "\x00\x07\x1b") {
					t.Fatalf("entry %q still carries a control byte", got)
				}
			},
		},
		{
			name:  "bidi overrides that reorder a rendered path are replaced",
			entry: "dir/‮gnp.exe/file",
			check: func(t *testing.T, got string) {
				if strings.ContainsRune(got, '‮') {
					t.Fatalf("entry %q still carries a bidi override", got)
				}
			},
		},
		{
			name:  "invalid UTF-8 is replaced, so the result is always valid UTF-8",
			entry: "dir/\xff\xfe\x80/file",
			check: func(t *testing.T, got string) {
				if !utf8.ValidString(got) {
					t.Fatalf("entry %q is not valid UTF-8", got)
				}
				if strings.Contains(got, "\xff") || strings.Contains(got, "\x80") {
					t.Fatalf("entry %q still carries a raw invalid byte", got)
				}
				if want := "dir/���/file"; got != want {
					t.Fatalf("entry = %q, want %q: each invalid byte is replaced, not dropped", got, want)
				}
			},
		},
		{
			name:  "truncation of multi-byte runes never splits one",
			entry: strings.Repeat("é", snapshot.MaxPublicEntryBytes),
			check: func(t *testing.T, got string) {
				if !utf8.ValidString(got) {
					t.Fatalf("entry %q is not valid UTF-8", got)
				}
				if len(got) > snapshot.MaxPublicEntryBytes {
					t.Fatalf("entry is %d bytes, want at most %d", len(got), snapshot.MaxPublicEntryBytes)
				}
				if !strings.HasSuffix(got, marker) {
					t.Fatalf("entry %q was truncated silently", got)
				}
			},
		},
		{
			name:  "an empty entry stays empty rather than becoming a marker",
			entry: "",
			check: func(t *testing.T, got string) {
				if got != "" {
					t.Fatalf("entry = %q, want empty", got)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("private: /tmp/capture-9182/root")
			err := snapshot.NewPublicValidationFailureForEntry(snapshot.ArchivePathNotCanonical, test.entry, cause)
			var public *snapshot.PublicValidationFailure
			if !errors.As(err, &public) {
				t.Fatalf("no public failure: %v", err)
			}
			if public.Reason() != snapshot.ArchivePathNotCanonical {
				t.Fatalf("reason = %q", public.Reason())
			}
			if !errors.Is(err, cause) {
				t.Fatal("private cause was not retained for logs")
			}
			test.check(t, public.Entry())
		})
	}
}
