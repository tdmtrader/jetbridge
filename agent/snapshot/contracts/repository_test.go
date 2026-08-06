package contracts_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRepositoryContractValidatesCleanFullRepositoryAndStableIdentity(t *testing.T) {
	repository := newGitRepository(t, "")
	first, err := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t))
	if err != nil {
		t.Fatalf("first repository validation error = %v", err)
	}
	firstMetadata := decodeRepositoryMetadata(t, first)
	if firstMetadata.ObjectFormat != "sha1" || len(firstMetadata.HeadSHA) != 40 || len(firstMetadata.TreeSHA) != 40 {
		t.Fatalf("first metadata = %+v, want full sha1 object IDs", firstMetadata)
	}
	if len(firstMetadata.RootCommits) != 1 || firstMetadata.RepositoryID == "" {
		t.Fatalf("first metadata = %+v, want one root and stable repository ID", firstMetadata)
	}

	writeTestFile(t, filepath.Join(repository, "README.md"), "second\n")
	runTestGit(t, repository, "add", "README.md")
	runTestGit(t, repository, "commit", "-q", "-m", "second")
	second, err := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t))
	if err != nil {
		t.Fatalf("second repository validation error = %v", err)
	}
	secondMetadata := decodeRepositoryMetadata(t, second)
	if secondMetadata.RepositoryID != firstMetadata.RepositoryID {
		t.Fatalf("repository ID changed from %q to %q after descendant commit", firstMetadata.RepositoryID, secondMetadata.RepositoryID)
	}
	if secondMetadata.HeadSHA == firstMetadata.HeadSHA || secondMetadata.TreeSHA == firstMetadata.TreeSHA {
		t.Fatalf("second metadata = %+v, want changed head and tree", secondMetadata)
	}
}

func TestRepositoryContractSupportsGitSHA256WhenAvailable(t *testing.T) {
	repository := newGitRepository(t, "sha256")
	result, err := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t))
	if err != nil {
		t.Fatalf("sha256 repository validation error = %v", err)
	}
	metadata := decodeRepositoryMetadata(t, result)
	if metadata.ObjectFormat != "sha256" || len(metadata.HeadSHA) != 64 || len(metadata.TreeSHA) != 64 || len(metadata.RootCommits[0]) != 64 {
		t.Fatalf("sha256 metadata = %+v, want full 64-character IDs", metadata)
	}
}

func TestRepositoryContractRejectsUnsafeIncompleteOrDirtyRepositories(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{"dirty tracked file", func(t *testing.T, dir string) { writeTestFile(t, filepath.Join(dir, "README.md"), "dirty\n") }, "clean"},
		{"untracked file", func(t *testing.T, dir string) { writeTestFile(t, filepath.Join(dir, "new.txt"), "new\n") }, "clean"},
		{"git file", replaceGitDirectoryWith(t, "file"), ".git"},
		{"git symlink", replaceGitDirectoryWith(t, "symlink"), ".git"},
		{"shallow", func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, ".git", "shallow"), runTestGit(t, dir, "rev-parse", "HEAD")+"\n")
		}, "shallow"},
		{"commondir", func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, ".git", "commondir"), "../outside\n")
		}, "commondir"},
		{"external core.worktree", func(t *testing.T, dir string) {
			runTestGit(t, dir, "config", "core.worktree", t.TempDir())
		}, "core.worktree"},
		{"external alternates", func(t *testing.T, dir string) {
			outside := filepath.Join(t.TempDir(), "objects")
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatalf("mkdir alternates: %v", err)
			}
			writeTestFile(t, filepath.Join(dir, ".git", "objects", "info", "alternates"), outside+"\n")
		}, "alternates"},
		{"contained alternates", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".git", "objects", "alternate"), 0755); err != nil {
				t.Fatalf("mkdir contained alternate: %v", err)
			}
			writeTestFile(t, filepath.Join(dir, ".git", "objects", "info", "alternates"), "alternate\n")
		}, "alternates"},
		{"broken head", func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/missing\n")
		}, "HEAD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repository := newGitRepository(t, "")
			tc.setup(t, repository)
			if _, err := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRepositoryContractClassifiesSafePublicValidationFailures(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, string)
		reason snapshot.ValidationFailureReason
		want   string
	}{
		{
			name:   "missing metadata",
			setup:  func(_ *testing.T, _ string) {},
			reason: snapshot.RepositoryMetadataMissing,
			want:   "repository .git directory is required",
		},
		{
			name: "unsafe metadata",
			setup: func(t *testing.T, dir string) {
				config := filepath.Join(dir, ".git", "config")
				contents, err := os.ReadFile(config)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				writeTestFile(t, config, string(contents)+"\n[extensions]\npartialClone = origin\n")
			},
			reason: snapshot.RepositoryMetadataUnsafe,
			want:   "repository config",
		},
		{
			name: "shallow history",
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, ".git", "shallow"), runTestGit(t, dir, "rev-parse", "HEAD")+"\n")
			},
			reason: snapshot.RepositoryHistoryIncomplete,
			want:   "shallow",
		},
		{
			name: "unsupported object format",
			setup: func(t *testing.T, dir string) {
				config := filepath.Join(dir, ".git", "config")
				contents, err := os.ReadFile(config)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				writeTestFile(t, config, string(contents)+"\n[extensions]\nobjectFormat = sha512\n")
			},
			reason: snapshot.RepositoryObjectFormatUnsupported,
			want:   "object format",
		},
		{
			name: "gitlink",
			setup: func(t *testing.T, dir string) {
				head := runTestGit(t, dir, "rev-parse", "HEAD")
				runTestGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/module")
				runTestGit(t, dir, "commit", "-q", "-m", "add gitlink")
			},
			reason: snapshot.RepositoryGitlinksUnsupported,
			want:   "gitlink",
		},
		{
			name: "dirty work tree",
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "README.md"), "dirty\n")
			},
			reason: snapshot.RepositoryDirty,
			want:   "clean",
		},
		{
			name: "invalid object graph",
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/missing\n")
			},
			reason: snapshot.RepositoryInvalid,
			want:   "HEAD",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.reason != snapshot.RepositoryMetadataMissing {
				dir = newGitRepository(t, "")
			}
			tc.setup(t, dir)
			_, err := validateDirectory(t, "repository/v1", dir, emptyValidationContext(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want internal detail containing %q", err, tc.want)
			}
			var public *snapshot.PublicValidationFailure
			if !errors.As(err, &public) || public.Reason() != tc.reason {
				t.Fatalf("validation error = %v, public reason = %#v, want %q", err, public, tc.reason)
			}
		})
	}
}

func TestRepositoryContractLeavesRootAndCancellationFailuresPrivate(t *testing.T) {
	validationContext := emptyValidationContext(t)
	_, err := withValidator(t, "repository/v1", newGitRepository(t, ""), func(validator snapshot.Validator, _ *os.Root) (snapshot.ValidationResult, error) {
		return validator.AdmitForSeal(context.Background(), nil, validationContext)
	})
	assertNoPublicValidationFailure(t, err)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = withValidator(t, "repository/v1", newGitRepository(t, ""), func(validator snapshot.Validator, root *os.Root) (snapshot.ValidationResult, error) {
		return validator.AdmitForSeal(canceled, root, validationContext)
	})
	assertNoPublicValidationFailure(t, err)
}

func TestRepositoryContractLeavesGitProcessFailuresPrivate(t *testing.T) {
	repository := newGitRepository(t, "")
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	writeTestFile(t, git, "#!/bin/sh\necho process-private >&2\nexit 97\n")
	if err := os.Chmod(git, 0755); err != nil {
		t.Fatalf("make fake Git executable: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t))
	if err == nil || !strings.Contains(err.Error(), "process-private") {
		t.Fatalf("validation error = %v, want retained process detail", err)
	}
	assertNoPublicValidationFailure(t, err)
}

func assertNoPublicValidationFailure(t *testing.T, err error) {
	t.Helper()
	var public *snapshot.PublicValidationFailure
	if err == nil || errors.As(err, &public) {
		t.Fatalf("error = %v, want a non-public validation failure", err)
	}
}

func TestRepositoryContractRejectsActiveTransportConfigurationBeforeInvokingGit(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{
			name: "partial clone extension",
			config: `[extensions]
	partialClone = origin
`,
		},
		{
			name: "promisor remote",
			config: `[remote "origin"]
	url = hostile::repository
	promisor = true
	partialCloneFilter = blob:none
`,
		},
		{
			name: "protocol-specific override",
			config: `[protocol "hostile"]
	allow = always
`,
		},
		{
			name: "custom remote transport",
			config: `[remote "origin"]
	url = https://example.invalid/repository
	vcs = hostile
`,
		},
		{
			name: "remote upload-pack command",
			config: `[remote "origin"]
	url = https://example.invalid/repository
	uploadpack = hostile-helper
`,
		},
		{
			name: "URL rewrite",
			config: `[url "hostile::repository"]
	insteadOf = https://example.invalid/
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repository := newGitRepository(t, "")
			configPath := filepath.Join(repository, ".git", "config")
			config, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read repository config: %v", err)
			}
			writeTestFile(t, configPath, string(config)+"\n"+tc.config)

			gitCalled := filepath.Join(t.TempDir(), "git-called")
			helperDirectory := t.TempDir()
			fakeGit := filepath.Join(helperDirectory, "git")
			writeTestFile(t, fakeGit, "#!/bin/sh\n: > \"$SNAPSHOT_GIT_CALLED\"\nexit 97\n")
			if err := os.Chmod(fakeGit, 0755); err != nil {
				t.Fatalf("make fake git executable: %v", err)
			}
			t.Setenv("SNAPSHOT_GIT_CALLED", gitCalled)
			t.Setenv("PATH", helperDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

			_, validationErr := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t))
			if validationErr == nil || !strings.Contains(validationErr.Error(), "repository config") {
				t.Errorf("validation error = %v, want repository config rejection", validationErr)
			}
			if _, err := os.Stat(gitCalled); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("git invocation marker error = %v, want Git not to be invoked", err)
			}
		})
	}
}

func TestRepositoryContractAcceptsOrdinaryRemoteAndBranchMetadata(t *testing.T) {
	repository := newGitRepository(t, "")
	runTestGit(t, repository, "remote", "add", "origin", "https://example.invalid/acme/repository.git")
	branch := runTestGit(t, repository, "symbolic-ref", "--short", "HEAD")
	runTestGit(t, repository, "config", "branch."+branch+".remote", "origin")
	runTestGit(t, repository, "config", "branch."+branch+".merge", "refs/heads/"+branch)

	if _, err := validateDirectory(t, "repository/v1", repository, emptyValidationContext(t)); err != nil {
		t.Fatalf("ordinary remote and branch metadata validation error = %v", err)
	}
}

func TestRepositoryChangeContractValidatesGitTree(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)

	writeTestFile(t, filepath.Join(base, "README.md"), "result\n")
	runTestGit(t, base, "add", "README.md")
	runTestGit(t, base, "commit", "-q", "-m", "result")
	resultMetadata := repositoryMetadataForDirectory(t, base)
	resultArchive, _ := canonicalRepositoryArchive(t, base)

	document := validChangeDocument(baseMetadata, resultMetadata, "git-tree", "result.tar", digestBytes(resultArchive))
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "result.tar", resultArchive, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err != nil {
		t.Fatalf("valid git-tree change error = %v", err)
	}

	document.ResultTreeSHA = baseMetadata.TreeSHA
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "result.tar", resultArchive, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "result_tree") {
		t.Fatalf("wrong git-tree result error = %v, want result_tree", err)
	}
}

func TestRepositoryChangeContractValidatesPatchWithoutPretendingItProvesACommit(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)

	writeTestFile(t, filepath.Join(base, "README.md"), "patched\n")
	patch := []byte(runTestGit(t, base, "diff", "--binary", "--no-ext-diff") + "\n")
	runTestGit(t, base, "add", "README.md")
	resultTree := runTestGit(t, base, "write-tree")
	document := repositoryChangeFixture{
		SchemaVersion: "1.0.0", RepositoryID: baseMetadata.RepositoryID, BaseInput: "base",
		BaseSHA: baseMetadata.HeadSHA, ResultTreeSHA: resultTree,
		Representation: "patch", PayloadPath: "change.patch", PayloadDigest: digestBytes(patch),
	}
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "change.patch", patch, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err != nil {
		t.Fatalf("valid patch change error = %v", err)
	}

	document.ResultSHA = baseMetadata.HeadSHA
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "change.patch", patch, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "result_commit") {
		t.Fatalf("patch result_commit error = %v, want result_commit rejection", err)
	}
}

func TestRepositoryChangeContractValidatesBundleAndBaseAncestry(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)

	writeTestFile(t, filepath.Join(base, "README.md"), "bundled\n")
	runTestGit(t, base, "add", "README.md")
	runTestGit(t, base, "commit", "-q", "-m", "bundled")
	resultMetadata := repositoryMetadataForDirectory(t, base)
	bundlePath := filepath.Join(t.TempDir(), "result.bundle")
	runTestGit(t, base, "bundle", "create", bundlePath, "HEAD", "^"+baseMetadata.HeadSHA)
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	document := validChangeDocument(baseMetadata, resultMetadata, "git-bundle", "result.bundle", digestBytes(bundle))
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "result.bundle", bundle, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err != nil {
		t.Fatalf("valid bundle change error = %v", err)
	}

	document.BaseSHA = strings.Repeat("0", len(baseMetadata.HeadSHA))
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "result.bundle", bundle, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "base_sha") {
		t.Fatalf("wrong bundle base error = %v, want base_sha", err)
	}
}

func TestRepositoryChangeContractRejectsDigestPathAndExactInputMismatches(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	metadata := repositoryMetadataForDirectory(t, base)
	document := repositoryChangeFixture{
		SchemaVersion: "1.0.0", RepositoryID: metadata.RepositoryID, BaseInput: "before",
		BaseSHA: metadata.HeadSHA, ResultTreeSHA: metadata.TreeSHA,
		Representation: "patch", PayloadPath: "../change.patch", PayloadDigest: "sha256:" + strings.Repeat("0", 64),
	}
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "change.patch", nil, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || (!strings.Contains(err.Error(), "content path") && !strings.Contains(err.Error(), "before")) {
		t.Fatalf("mismatch error = %v, want path or exact input error", err)
	}
}

func TestRepositoryChangeGitTreeRequiresActualHEADToEqualResultSHA(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)
	writeTestFile(t, filepath.Join(base, "README.md"), "declared result\n")
	runTestGit(t, base, "add", "README.md")
	runTestGit(t, base, "commit", "-q", "-m", "declared result")
	declaredResult := repositoryMetadataForDirectory(t, base)
	writeTestFile(t, filepath.Join(base, "README.md"), "actual head\n")
	runTestGit(t, base, "add", "README.md")
	runTestGit(t, base, "commit", "-q", "-m", "actual head")
	resultArchive, _ := canonicalRepositoryArchive(t, base)
	document := validChangeDocument(baseMetadata, declaredResult, "git-tree", "result.tar", digestBytes(resultArchive))

	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "result.tar", resultArchive, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("non-HEAD result error = %v, want HEAD mismatch", err)
	}
}

func TestRepositoryChangePatchRejectsEscapingSymlinkResult(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)
	if err := os.Symlink("../outside", filepath.Join(base, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	runTestGit(t, base, "add", "escape")
	patch := []byte(runTestGit(t, base, "diff", "--cached", "--binary", "--no-ext-diff", "HEAD") + "\n")
	document := repositoryChangeFixture{
		SchemaVersion: "1.0.0", RepositoryID: baseMetadata.RepositoryID, BaseInput: "base",
		BaseSHA: baseMetadata.HeadSHA, ResultTreeSHA: runTestGit(t, base, "write-tree"),
		Representation: "patch", PayloadPath: "change.patch", PayloadDigest: digestBytes(patch),
	}
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "change.patch", patch, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("escaping symlink patch error = %v, want symlink rejection", err)
	}
}

func TestRepositoryChangePatchRejectsNoncanonicalSymlinkTarget(t *testing.T) {
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)
	if err := os.Symlink("sub/../README.md", filepath.Join(base, "link")); err != nil {
		t.Fatalf("create noncanonical symlink: %v", err)
	}
	runTestGit(t, base, "add", "link")
	patch := []byte(runTestGit(t, base, "diff", "--cached", "--binary", "--no-ext-diff", "HEAD") + "\n")
	document := repositoryChangeFixture{
		SchemaVersion: "1.0.0", RepositoryID: baseMetadata.RepositoryID, BaseInput: "base",
		BaseSHA: baseMetadata.HeadSHA, ResultTreeSHA: runTestGit(t, base, "write-tree"),
		Representation: "patch", PayloadPath: "change.patch", PayloadDigest: digestBytes(patch),
	}
	if _, err := validateFiles(t, "repository-change/v1",
		repositoryChangeFiles(t, document, "change.patch", patch, baseDigest),
		repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("noncanonical symlink patch error = %v, want canonical-target rejection", err)
	}
}

func TestRepositoryChangeBundleRejectsGitlinksAndExtraHeads(t *testing.T) {
	t.Run("gitlink in declared result tree", func(t *testing.T) {
		base := newGitRepository(t, "")
		baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
		baseMetadata := repositoryMetadataForDirectory(t, base)
		runTestGit(t, base, "update-index", "--add", "--cacheinfo", "160000,"+baseMetadata.HeadSHA+",vendor/module")
		runTestGit(t, base, "commit", "-q", "-m", "gitlink result")
		resultMetadata := repositoryMetadataForDirectoryRevision(t, base, "HEAD", baseMetadata.RepositoryID)
		bundlePath := filepath.Join(t.TempDir(), "gitlink.bundle")
		runTestGit(t, base, "bundle", "create", bundlePath, "HEAD", "^"+baseMetadata.HeadSHA)
		bundle, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatalf("read gitlink bundle: %v", err)
		}
		document := validChangeDocument(baseMetadata, resultMetadata, "git-bundle", "result.bundle", digestBytes(bundle))
		if _, err := validateFiles(t, "repository-change/v1",
			repositoryChangeFiles(t, document, "result.bundle", bundle, baseDigest),
			repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "gitlink") {
			t.Fatalf("gitlink bundle error = %v, want gitlink rejection", err)
		}
	})

	t.Run("more than one exposed head", func(t *testing.T) {
		base := newGitRepository(t, "")
		baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
		baseMetadata := repositoryMetadataForDirectory(t, base)
		originalBranch := runTestGit(t, base, "symbolic-ref", "--short", "HEAD")
		writeTestFile(t, filepath.Join(base, "README.md"), "intended\n")
		runTestGit(t, base, "add", "README.md")
		runTestGit(t, base, "commit", "-q", "-m", "intended")
		resultMetadata := repositoryMetadataForDirectory(t, base)
		runTestGit(t, base, "switch", "-q", "-c", "extra", baseMetadata.HeadSHA)
		writeTestFile(t, filepath.Join(base, "extra.txt"), "extra\n")
		runTestGit(t, base, "add", "extra.txt")
		runTestGit(t, base, "commit", "-q", "-m", "extra")
		runTestGit(t, base, "switch", "-q", originalBranch)
		bundlePath := filepath.Join(t.TempDir(), "extra.bundle")
		runTestGit(t, base, "bundle", "create", bundlePath, "HEAD", "refs/heads/extra", "^"+baseMetadata.HeadSHA)
		bundle, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatalf("read extra-head bundle: %v", err)
		}
		document := validChangeDocument(baseMetadata, resultMetadata, "git-bundle", "result.bundle", digestBytes(bundle))
		if _, err := validateFiles(t, "repository-change/v1",
			repositoryChangeFiles(t, document, "result.bundle", bundle, baseDigest),
			repositoryInputContext(t, "base", baseArchive, baseDigest)); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("extra-head bundle error = %v, want exactly-one-head rejection", err)
		}
	})
}

type repositoryChangeFixture struct {
	SchemaVersion  string
	RepositoryID   string
	BaseInput      string
	BaseSHA        string
	ResultSHA      string
	ResultTreeSHA  string
	Representation string
	PayloadPath    string
	PayloadDigest  string
}

func validChangeDocument(base, result contracts.RepositoryMetadata, representation, payloadPath, payloadDigest string) repositoryChangeFixture {
	return repositoryChangeFixture{
		SchemaVersion: "1.0.0", RepositoryID: base.RepositoryID, BaseInput: "base",
		BaseSHA: base.HeadSHA, ResultSHA: result.HeadSHA, ResultTreeSHA: result.TreeSHA,
		Representation: representation, PayloadPath: payloadPath, PayloadDigest: payloadDigest,
	}
}

func repositoryChangeFiles(
	t *testing.T,
	document repositoryChangeFixture,
	payloadName string,
	payload []byte,
	baseDigest snapshot.Digest,
) map[string][]byte {
	t.Helper()
	payloadPath := document.PayloadPath
	if !strings.HasPrefix(payloadPath, "../") && !strings.HasPrefix(payloadPath, "content/") {
		payloadPath = "content/" + payloadPath
	}
	record, err := contracts.NewRecord(
		mustTypeRef(t, "repository-change/v1"),
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: document.BaseInput,
			Type: mustTypeRef(t, "repository/v1"), Digest: baseDigest,
		}},
		contracts.RepositoryChangeBody{
			RepositoryID: document.RepositoryID, BaseSHA: document.BaseSHA,
			Representation: document.Representation,
			Payload: contracts.ContentRef{
				Path: payloadPath, Digest: mustDigest(t, document.PayloadDigest),
				MediaType: "application/octet-stream",
			},
			ResultTree: document.ResultTreeSHA, ResultCommit: document.ResultSHA,
		},
	)
	if err != nil {
		t.Fatalf("NewRecord(repository-change/v1): %v", err)
	}
	return map[string][]byte{
		"record.json":            marshalRecord(t, record),
		"content/" + payloadName: payload,
	}
}

func repositoryInputContext(t *testing.T, name string, archive []byte, digest snapshot.Digest) snapshot.ValidationContext {
	t.Helper()
	typeRef := mustTypeRef(t, "repository/v1")
	id, err := snapshot.NewSnapshotID(1)
	if err != nil {
		t.Fatalf("NewSnapshotID(): %v", err)
	}
	ref := snapshot.SnapshotRef{ID: id, Type: typeRef, Digest: digest}
	validationContext, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{name: ref}, func(_ context.Context, openedName string, openedRef snapshot.SnapshotRef) (io.ReadCloser, error) {
		if openedName != name || openedRef != ref {
			t.Fatalf("opener got (%q, %+v), want (%q, %+v)", openedName, openedRef, name, ref)
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	})
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	return validationContext
}

func repositoryMetadataForDirectory(t *testing.T, dir string) contracts.RepositoryMetadata {
	t.Helper()
	result, err := validateDirectory(t, "repository/v1", dir, emptyValidationContext(t))
	if err != nil {
		t.Fatalf("repository validation error = %v", err)
	}
	return decodeRepositoryMetadata(t, result)
}

func repositoryMetadataForDirectoryRevision(t *testing.T, dir, revision, repositoryID string) contracts.RepositoryMetadata {
	t.Helper()
	objectFormat := runTestGit(t, dir, "rev-parse", "--show-object-format=storage")
	head := runTestGit(t, dir, "rev-parse", "--verify", revision+"^{commit}")
	tree := runTestGit(t, dir, "rev-parse", "--verify", head+"^{tree}")
	roots := strings.Fields(runTestGit(t, dir, "rev-list", "--max-parents=0", head))
	return contracts.RepositoryMetadata{
		RepositoryID: repositoryID,
		ObjectFormat: objectFormat,
		HeadSHA:      head,
		TreeSHA:      tree,
		RootCommits:  roots,
	}
}

func decodeRepositoryMetadata(t *testing.T, result snapshot.ValidationResult) contracts.RepositoryMetadata {
	t.Helper()
	var metadata contracts.RepositoryMetadata
	if err := json.Unmarshal(result.IntrinsicMetadata, &metadata); err != nil {
		t.Fatalf("decode repository metadata %s: %v", result.IntrinsicMetadata, err)
	}
	return metadata
}

func newGitRepository(t *testing.T, objectFormat string) string {
	t.Helper()
	dir := t.TempDir()
	args := []string{"init", "-q"}
	if objectFormat != "" {
		args = append(args, "--object-format="+objectFormat)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnvironment()
	if output, err := cmd.CombinedOutput(); err != nil {
		if objectFormat == "sha256" {
			t.Skipf("local Git does not support sha256 repositories: %v: %s", err, output)
		}
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	runTestGit(t, dir, "config", "user.name", "Snapshot Test")
	runTestGit(t, dir, "config", "user.email", "snapshot@example.invalid")
	writeTestFile(t, filepath.Join(dir, "README.md"), "base\n")
	runTestGit(t, dir, "add", "README.md")
	runTestGit(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q: %v: %s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func testGitEnvironment() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
	)
}

func replaceGitDirectoryWith(t *testing.T, kind string) func(*testing.T, string) {
	t.Helper()
	return func(t *testing.T, dir string) {
		original := filepath.Join(dir, ".git")
		moved := filepath.Join(dir, "git-data")
		if err := os.Rename(original, moved); err != nil {
			t.Fatalf("move .git: %v", err)
		}
		switch kind {
		case "file":
			writeTestFile(t, original, "gitdir: git-data\n")
		case "symlink":
			if err := os.Symlink("git-data", original); err != nil {
				t.Fatalf("symlink .git: %v", err)
			}
		default:
			t.Fatalf("unknown .git replacement %q", kind)
		}
	}
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatalf("mkdir %q: %v", name, err)
	}
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatalf("write %q: %v", name, err)
	}
}

func canonicalRepositoryArchive(t *testing.T, dir string) ([]byte, snapshot.Digest) {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(name)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close raw tar: %v", err)
	}
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatalf("canonicalize repository: %v", err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatalf("read canonical archive: %v", err)
	}
	return archive, tree.Digest
}

func digestBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// A repository snapshot must be able to name revisions other than HEAD, so one
// materialized tree can carry both sides of a pull request instead of being
// cloned twice. Refs are derived from the sealed bytes -- .git/refs and
// packed-refs are inside the canonical archive -- which is what keeps identical
// content producing byte-identical intrinsic metadata, as the snapshot store
// requires of any (type, digest) pair.
func TestRepositoryMetadataNamesEveryRefInTheTree(t *testing.T) {
	dir := newGitRepository(t, "")
	head := runTestGit(t, dir, "rev-parse", "--verify", "HEAD")
	writeTestFile(t, filepath.Join(dir, "second.md"), "second\n")
	runTestGit(t, dir, "add", "second.md")
	runTestGit(t, dir, "commit", "-q", "-m", "second")
	second := runTestGit(t, dir, "rev-parse", "--verify", "HEAD")
	// A second ref pointing at a commit that is deliberately NOT HEAD.
	runTestGit(t, dir, "update-ref", "refs/concourse/materialized/base", head)

	metadata := repositoryMetadataForDirectory(t, dir)

	if metadata.HeadSHA != second {
		t.Fatalf("head_sha = %q, want the checked-out commit %q", metadata.HeadSHA, second)
	}
	if metadata.Refs == nil {
		t.Fatal("metadata carries no refs: a snapshot still cannot name a revision")
	}
	if got := metadata.Refs["refs/concourse/materialized/base"]; got != head {
		t.Fatalf("refs[refs/concourse/materialized/base] = %q, want the non-HEAD commit %q", got, head)
	}
}
