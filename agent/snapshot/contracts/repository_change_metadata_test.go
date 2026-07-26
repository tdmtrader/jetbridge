package contracts_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	metadataRepositoryID = "sha256:" + "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	metadataBaseSHA      = "1111111111111111111111111111111111111111"
	metadataResultCommit = "2222222222222222222222222222222222222222"
	metadataResultTree   = "3333333333333333333333333333333333333333"
)

// preRecordMetadataJSON reproduces the exact bytes the pre-record validator
// sealed: result_sha / result_tree_sha, the "bundle" representation spelling,
// and no changed_files member. Those snapshots are immutable, so this document
// has to stay readable for as long as any of them exist.
func preRecordMetadataJSON(representation, resultSHA string) []byte {
	document := struct {
		RepositoryID   string `json:"repository_id"`
		BaseSHA        string `json:"base_sha"`
		ResultSHA      string `json:"result_sha,omitempty"`
		ResultTreeSHA  string `json:"result_tree_sha"`
		Representation string `json:"representation"`
	}{
		RepositoryID: metadataRepositoryID, BaseSHA: metadataBaseSHA,
		ResultSHA: resultSHA, ResultTreeSHA: metadataResultTree, Representation: representation,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestRepositoryChangeMetadataReadsBothSealedShapes(t *testing.T) {
	current := contracts.RepositoryChangeMetadata{
		RepositoryID: metadataRepositoryID, BaseSHA: metadataBaseSHA,
		ResultCommit: metadataResultCommit, ResultTree: metadataResultTree,
		Representation: "git-bundle", ChangedFiles: []string{"a.txt", "b.txt"},
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := contracts.DecodeRepositoryChangeMetadata(encoded)
	if err != nil {
		t.Fatalf("current shape: %v", err)
	}
	if !reflect.DeepEqual(decoded, current) {
		t.Fatalf("current shape round trip = %+v, want %+v", decoded, current)
	}

	for name, expectation := range map[string]struct {
		raw  []byte
		want contracts.RepositoryChangeMetadata
	}{
		"pre-record bundle": {
			raw: preRecordMetadataJSON("bundle", metadataResultCommit),
			want: contracts.RepositoryChangeMetadata{
				RepositoryID: metadataRepositoryID, BaseSHA: metadataBaseSHA,
				ResultCommit: metadataResultCommit, ResultTree: metadataResultTree,
				Representation: "git-bundle",
			},
		},
		"pre-record git-tree": {
			raw: preRecordMetadataJSON("git-tree", metadataResultCommit),
			want: contracts.RepositoryChangeMetadata{
				RepositoryID: metadataRepositoryID, BaseSHA: metadataBaseSHA,
				ResultCommit: metadataResultCommit, ResultTree: metadataResultTree,
				Representation: "git-tree",
			},
		},
		// A patch proves only the tree, so the pre-record writer omitted
		// result_sha entirely. The absent commit must stay absent.
		"pre-record patch without a result commit": {
			raw: preRecordMetadataJSON("patch", ""),
			want: contracts.RepositoryChangeMetadata{
				RepositoryID: metadataRepositoryID, BaseSHA: metadataBaseSHA,
				ResultTree: metadataResultTree, Representation: "patch",
			},
		},
		// changed_files did not exist before the rename. It normalizes to the
		// absent list rather than to a claim that nothing changed.
		"current shape without changed_files": {
			raw: []byte(`{"repository_id":"` + metadataRepositoryID + `","base_sha":"` + metadataBaseSHA +
				`","result_commit":"` + metadataResultCommit + `","result_tree":"` + metadataResultTree +
				`","representation":"git-tree"}`),
			want: contracts.RepositoryChangeMetadata{
				RepositoryID: metadataRepositoryID, BaseSHA: metadataBaseSHA,
				ResultCommit: metadataResultCommit, ResultTree: metadataResultTree,
				Representation: "git-tree",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := contracts.DecodeRepositoryChangeMetadata(expectation.raw)
			if err != nil {
				t.Fatalf("decode %s: %v", expectation.raw, err)
			}
			if !reflect.DeepEqual(decoded, expectation.want) {
				t.Fatalf("decoded = %+v, want %+v", decoded, expectation.want)
			}
			if decoded.ChangedFiles != nil && len(decoded.ChangedFiles) == 0 {
				t.Fatal("an absent changed_files list was normalized into an empty one")
			}
		})
	}
}

// SAFETY PROPERTY, do not weaken: reading two shapes must not become reading
// anything. Every document below is refused, and the refusal is reported
// against the current shape so a broken modern document is not misdiagnosed.
func TestRepositoryChangeMetadataRefusesEverythingElse(t *testing.T) {
	for name, raw := range map[string]string{
		"mixed vocabularies": `{"repository_id":"` + metadataRepositoryID + `","base_sha":"` + metadataBaseSHA +
			`","result_sha":"` + metadataResultCommit + `","result_commit":"` + metadataResultCommit +
			`","result_tree_sha":"` + metadataResultTree + `","result_tree":"` + metadataResultTree +
			`","representation":"bundle"}`,
		"pre-record names plus changed_files": `{"repository_id":"` + metadataRepositoryID + `","base_sha":"` + metadataBaseSHA +
			`","result_sha":"` + metadataResultCommit + `","result_tree_sha":"` + metadataResultTree +
			`","representation":"bundle","changed_files":[]}`,
		"pre-record names plus an unknown field": `{"repository_id":"` + metadataRepositoryID + `","base_sha":"` + metadataBaseSHA +
			`","result_sha":"` + metadataResultCommit + `","result_tree_sha":"` + metadataResultTree +
			`","representation":"bundle","smuggled":{"any":"value"}}`,
		"pre-record names with a representation that writer never emitted": `{"repository_id":"` + metadataRepositoryID +
			`","base_sha":"` + metadataBaseSHA + `","result_sha":"` + metadataResultCommit +
			`","result_tree_sha":"` + metadataResultTree + `","representation":"git-bundle"}`,
		"trailing JSON": string(preRecordMetadataJSON("bundle", metadataResultCommit)) + `{"more":1}`,
		"array":         `["not an object"]`,
		"truncated":     `{"repository_id":"` + metadataRepositoryID + `"`,
		"wrong types": `{"repository_id":"` + metadataRepositoryID + `","base_sha":7,"result_sha":"` +
			metadataResultCommit + `","result_tree_sha":"` + metadataResultTree + `","representation":"bundle"}`,
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := contracts.DecodeRepositoryChangeMetadata([]byte(raw))
			if err == nil {
				t.Fatalf("%s was accepted as %+v", raw, decoded)
			}
			if !reflect.DeepEqual(decoded, contracts.RepositoryChangeMetadata{}) {
				t.Fatalf("refused decode returned %+v, want the zero value", decoded)
			}
		})
	}

	// The retired spelling is remapped only when it arrives in the pre-record
	// shape. In the current shape it passes through untouched, so the caller's
	// representation rule still refuses it instead of being handed a value the
	// document never carried.
	retired, err := contracts.DecodeRepositoryChangeMetadata([]byte(
		`{"repository_id":"` + metadataRepositoryID + `","base_sha":"` + metadataBaseSHA +
			`","result_commit":"` + metadataResultCommit + `","result_tree":"` + metadataResultTree +
			`","representation":"bundle","changed_files":[]}`))
	if err != nil || retired.Representation != "bundle" {
		t.Fatalf("current shape decode = (%+v, %v), want the representation left as %q", retired, err, "bundle")
	}

	// The reported failure names the current vocabulary, so a broken modern
	// document is not misdiagnosed as a legacy one.
	_, err = contracts.DecodeRepositoryChangeMetadata([]byte(
		`{"repository_id":"x","base_sha":"y","result_sha":"z","result_tree_sha":"w","representation":"bundle","changed_files":[]}`))
	if err == nil || !strings.Contains(err.Error(), "result_sha") {
		t.Fatalf("error = %v, want the current-shape decode failure", err)
	}
}
