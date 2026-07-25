package contracts_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRepositoryChangeRecordBindsItsExactBaseAndDerivesChangedFiles(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)

	writeTestFile(t, filepath.Join(base, "README.md"), "patched\n")
	patch := []byte(runTestGit(t, base, "diff", "--binary", "--no-ext-diff") + "\n")
	runTestGit(t, base, "add", "README.md")

	validationContext := repositoryInputContext(t, "base", baseArchive, baseDigest)
	baseRef, found := validationContext.Input("base")
	if !found {
		t.Fatal("base input was not exposed")
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{
			contracts.SubjectFromInput("base", contracts.SubjectRoleBase, "base", baseRef),
		},
		contracts.RepositoryChangeBody{
			RepositoryID:   baseMetadata.RepositoryID,
			BaseSHA:        baseMetadata.HeadSHA,
			Representation: "patch",
			Payload: contracts.ContentRef{
				Path:      "content/change.patch",
				Digest:    mustDigest(t, digestBytes(patch)),
				MediaType: "text/x-diff",
			},
			ResultTree: runTestGit(t, base, "write-tree"),
		},
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	result, err := validateFiles(t, "repository-change/v1", map[string][]byte{
		"record.json":          marshalRecord(t, record),
		"content/change.patch": patch,
	}, validationContext)
	if err != nil {
		t.Fatalf("valid repository-change record: %v", err)
	}
	var metadata contracts.RepositoryChangeMetadata
	if err := json.Unmarshal(result.IntrinsicMetadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if len(metadata.ChangedFiles) != 1 || metadata.ChangedFiles[0] != "README.md" {
		t.Fatalf("changed files = %v, want [README.md]", metadata.ChangedFiles)
	}
}

func mustDigest(t *testing.T, raw string) snapshot.Digest {
	t.Helper()
	digest, err := snapshot.ParseDigest(raw)
	if err != nil {
		t.Fatalf("ParseDigest(%q): %v", raw, err)
	}
	return digest
}
