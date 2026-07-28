package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/functions/devvalidate"
	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
)

func gitForValidation(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func captureForValidation(t *testing.T, dir string) ([]byte, snapshot.Digest) {
	t.Helper()
	tree, err := repositorymerge.CaptureDirectory(context.Background(), snapshot.Canonicalizer{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	raw, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return raw, tree.Digest
}

// imageBakedDevCapabilityRunner executes a real temporary dev-capability
// binary, while accepting the compiled image path as its input. Production
// does not use this adapter: it always executes the fixed absolute
// /usr/local/bin/dev-capability baked into the digest-pinned validator image.
// This is the narrow host-test seam for exercising function-runner's actual
// flag parsing and validation materialization without rebuilding or publishing
// an image.
type imageBakedDevCapabilityRunner struct{ binary string }

type observingDevValidationRunner struct{ called bool }

func (r *observingDevValidationRunner) Run(context.Context, devvalidate.Request) (contracts.Record[contracts.ValidationBody], error) {
	r.called = true
	return contracts.Record[contracts.ValidationBody]{}, nil
}

func (r imageBakedDevCapabilityRunner) Run(ctx context.Context, args []string, dir string, env []string) (int, error) {
	if len(args) < 2 || args[0] != workflow.DevValidationCLIPath || args[1] != workflow.DevValidationCLIValidateCommand {
		return 0, fmt.Errorf("unexpected compiled validation command %q", args)
	}
	command := exec.CommandContext(ctx, r.binary, args[1:]...)
	command.Dir, command.Env = dir, env
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		if exit.ExitCode() == 2 && stderr.Len() > 0 {
			return exit.ExitCode(), fmt.Errorf("real dev-capability configuration error: %s", stderr.String())
		}
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("run real dev-capability: %w: %s", err, stderr.String())
	}
	return 0, nil
}

func buildRealDevCapability(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "dev-capability")
	command := exec.Command("go", "build", "-o", binary, "./cmd/dev-capability")
	command.Dir = filepath.Clean(filepath.Join("..", "..", "ci-agent"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build real dev-capability: %v\n%s", err, output)
	}
	return binary
}

func TestCopyCandidatePreservesContainedSymlinkAndRejectsEscapingOne(t *testing.T) {
	for name, target := range map[string]string{
		"contained": "file.txt",
		"escaping":  "../../outside",
	} {
		t.Run(name, func(t *testing.T) {
			source, destination := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("safe"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(source, "link")); err != nil {
				t.Fatal(err)
			}
			err := copyCandidate(context.Background(), source, destination)
			if name == "escaping" {
				if err == nil {
					t.Fatal("escaping candidate symlink copied")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.Readlink(filepath.Join(destination, "link"))
			if err != nil || got != target {
				t.Fatalf("copied symlink = %q, %v", got, err)
			}
		})
	}
}

func TestDevValidateRejectsSubstitutedProtectedBytesBeforeRunnerLaunch(t *testing.T) {
	root := t.TempDir()
	candidate, workspace, output := filepath.Join(root, "candidate"), filepath.Join(root, "workspace"), filepath.Join(root, "validation")
	for _, path := range []string{candidate, workspace, output} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(candidate, "input.txt"), []byte("exact candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	_, candidateDigest := captureForValidation(t, candidate)
	protected := t.TempDir()
	profilePath, configPath := filepath.Join(protected, "profile.yml"), filepath.Join(protected, "config.yml")
	if err := os.WriteFile(profilePath, []byte("substituted profile"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("substituted config"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &observingDevValidationRunner{}
	o := devValidateOptions{
		root: root, candidate: "candidate", workspace: "workspace", output: "validation", candidateType: "opaque/v1", candidateID: "1", candidateDigest: candidateDigest.String(), profileName: "exact", profilePath: profilePath,
		profileDigest: "sha256:" + strings.Repeat("a", 64), configPath: configPath, configDigest: contentDigest([]byte("substituted config")).String(), image: "example.test/dev-capability@sha256:" + strings.Repeat("b", 64), definitionID: "2", version: "3",
	}
	if err := executeDevValidateWithRunner(context.Background(), o, runner); err == nil {
		t.Fatal("substituted protected profile was accepted")
	}
	if runner.called {
		t.Fatal("validation runner/process launch occurred after protected digest mismatch")
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("output was sealed before protected digest validation: %v", entries)
	}
}

func TestExactRepositoryBaseRejectsMissingAndAmbiguousRepositoryBases(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"base-a", "base-b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, options := range map[string]devValidateOptions{
		"missing":   {bases: []string{"base-a"}, baseRefs: []string{"base-a=1,opaque/v1," + digest}},
		"ambiguous": {bases: []string{"base-a", "base-b"}, baseRefs: []string{"base-a=1,repository/v1," + digest, "base-b=2,repository/v1," + digest}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := exactRepositoryBase(options, root); err == nil {
				t.Fatal("invalid base binding accepted")
			}
		})
	}
}

func TestDevValidateRealFunctionRunnerAndImageBakedCapabilitySealsRev3(t *testing.T) {
	root := t.TempDir()
	candidateName := "candidate-change"
	candidateRoot := filepath.Join(root, candidateName)
	workspace, output := filepath.Join(root, "workspace"), filepath.Join(root, "validation")
	for _, path := range []string{candidateRoot, workspace, output} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(candidateRoot, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateRoot, "app", "current.txt"), []byte("exact candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tree, err := repositorymerge.CaptureDirectory(context.Background(), snapshot.Canonicalizer{}, candidateRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest := tree.Digest
	_ = tree.Close()
	profile := []byte("schema_version: 1\nname: exact\nchecks:\n  - id: test\n    operation: test\n    scope: full\n    timeout: 1m\n    retries: 0\n")
	config := []byte("schema_version: 1\nrepo:\n  test: {cmd: [sh, -c, 'test -f app/current.txt']}\ncomponents:\n  - id: app\n    description: app\n    paths: [app/]\n    kind: service\n")
	protected := t.TempDir()
	profilePath, configPath := filepath.Join(protected, "profile.yml"), filepath.Join(protected, "config.yml")
	if err := os.WriteFile(profilePath, profile, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	image := "example.test/dev-capability@sha256:" + strings.Repeat("a", 64)
	previous := newDevValidationRunner
	newDevValidationRunner = func() devValidationRunner {
		return devvalidate.NewRunner(imageBakedDevCapabilityRunner{binary: buildRealDevCapability(t)})
	}
	t.Cleanup(func() { newDevValidationRunner = previous })
	args := []string{"--root", root, "--candidate", candidateName, "--workspace", "workspace", "--output", "validation", "--candidate-type", "opaque/v1", "--candidate-id", "71", "--candidate-digest", candidateDigest.String(), "--profile-name", "exact", "--profile", profilePath, "--profile-digest", contentDigest(profile).String(), "--config", configPath, "--config-digest", contentDigest(config).String(), "--capability-image", image, "--workflow-definition-id", "72", "--workflow-version", "73"}
	var stdout, stderr bytes.Buffer
	if status := runDevValidate(context.Background(), args, &stdout, &stderr); status != exitOK {
		t.Fatalf("real function-runner dev-validate exit = %d, stderr = %s", status, stderr.String())
	}
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(snapshot.Canonicalizer{}))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := registry.Lookup(snapshot.TypeRef("validation/v1"))
	if err != nil {
		t.Fatal(err)
	}
	rootHandle, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	declarations, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{candidateName: {ID: 71, Type: "opaque/v1", Digest: candidateDigest}}, nil, snapshot.WithValidationAttestationAuthority(snapshot.ValidationAttestationAuthority{CandidateInput: candidateName, Candidate: snapshot.SnapshotRef{ID: 71, Type: "opaque/v1", Digest: candidateDigest}, ProfileDigest: contentDigest(profile), ProtectedConfigDigest: contentDigest(config), CapabilityImage: image, CapabilityImageDigest: imageDigest, WorkflowDefinitionID: 72, WorkflowVersion: 73, Toolchain: "dev-capability/" + imageDigest.String()}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = validator.AdmitForSeal(context.Background(), rootHandle, declarations)
	_ = rootHandle.Close()
	if err != nil {
		t.Fatalf("Task5 authority rejected real function-runner output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "content", "logs", "attempt-0001.log")); err != nil {
		t.Fatalf("complete real CLI log missing: %v", err)
	}
}

func TestDevValidateRepositoryChangeUsesExactBaseAndRenamePaths(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "baseline")
	candidateRepo := filepath.Join(root, "work")
	candidateName := "candidate-change"
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	gitForValidation(t, base, "init", "--initial-branch=main")
	if err := os.Mkdir(filepath.Join(base, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "app", "before.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForValidation(t, base, "add", ".")
	gitForValidation(t, base, "commit", "-m", "base")
	baseSHA := gitForValidation(t, base, "rev-parse", "HEAD")
	gitForValidation(t, root, "clone", "--no-hardlinks", base, candidateRepo)
	gitForValidation(t, candidateRepo, "mv", "app/before.txt", "app/current.txt")
	gitForValidation(t, candidateRepo, "commit", "-m", "rename")
	baseArchive, baseDigest := captureForValidation(t, base)
	payload, _ := captureForValidation(t, candidateRepo)
	payloadSum := sha256.Sum256(payload)
	payloadDigest := snapshot.Digest("sha256:" + hex.EncodeToString(payloadSum[:]))
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(snapshot.Canonicalizer{}))
	if err != nil {
		t.Fatal(err)
	}
	repositoryValidator, err := registry.Lookup("repository/v1")
	if err != nil {
		t.Fatal(err)
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := repositoryValidator.RevalidateSealed(context.Background(), baseRoot, snapshot.ValidationContext{})
	_ = baseRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	var repository contracts.RepositoryMetadata
	if err := json.Unmarshal(metadata.IntrinsicMetadata, &repository); err != nil {
		t.Fatal(err)
	}
	baseRef := snapshot.SnapshotRef{ID: 80, Type: "repository/v1", Digest: baseDigest}
	body := contracts.RepositoryChangeBody{RepositoryID: repository.RepositoryID, BaseSHA: baseSHA, Representation: "git-tree", Payload: contracts.ContentRef{Path: "content/payload.tar", Digest: payloadDigest, MediaType: "application/octet-stream"}, ResultTree: gitForValidation(t, candidateRepo, "rev-parse", "HEAD^{tree}"), ResultCommit: gitForValidation(t, candidateRepo, "rev-parse", "HEAD")}
	record, err := contracts.NewRecord(snapshot.TypeRef("repository-change/v1"), []contracts.Subject{contracts.SubjectFromInput("base", contracts.SubjectRoleBase, "baseline", baseRef)}, body)
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, candidateName)
	if err := os.MkdirAll(filepath.Join(candidate, "content"), 0700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "record.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "content", "payload.tar"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	_, candidateDigest := captureForValidation(t, candidate)
	workspace, output := filepath.Join(root, "workspace"), filepath.Join(root, "validation")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	protected := t.TempDir()
	profile := []byte("schema_version: 1\nname: exact\nchecks:\n  - id: test\n    operation: test\n    scope: full\n    timeout: 1m\n    retries: 0\n")
	config := []byte("schema_version: 1\nrepo:\n  test: {cmd: [sh, -c, 'test -f app/current.txt && test ! -f app/before.txt']}\ncomponents:\n  - id: app\n    description: app\n    paths: [app/]\n    kind: service\n")
	profilePath, configPath := filepath.Join(protected, "profile.yml"), filepath.Join(protected, "config.yml")
	_ = os.WriteFile(profilePath, profile, 0600)
	_ = os.WriteFile(configPath, config, 0600)
	image := "example.test/dev-capability@sha256:" + strings.Repeat("a", 64)
	previous := newDevValidationRunner
	binary := buildRealDevCapability(t)
	newDevValidationRunner = func() devValidationRunner { return devvalidate.NewRunner(imageBakedDevCapabilityRunner{binary}) }
	t.Cleanup(func() { newDevValidationRunner = previous })
	args := []string{"--root", root, "--candidate", candidateName, "--workspace", "workspace", "--output", "validation", "--candidate-type", "repository-change/v1", "--candidate-id", "71", "--candidate-digest", candidateDigest.String(), "--profile-name", "exact", "--profile", profilePath, "--profile-digest", contentDigest(profile).String(), "--config", configPath, "--config-digest", contentDigest(config).String(), "--capability-image", image, "--workflow-definition-id", "72", "--workflow-version", "73", "--base", "baseline", "--base-ref", fmt.Sprintf("baseline=%s,%s,%s", baseRef.ID, baseRef.Type, baseRef.Digest)}
	var stdout, stderr bytes.Buffer
	if status := runDevValidate(context.Background(), args, &stdout, &stderr); status != exitOK {
		t.Fatalf("repository dev-validate = %d: %s", status, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, "app", "current.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "app", "before.txt")); !os.IsNotExist(err) {
		t.Fatalf("rename PreviousPath was not materialized: %v", err)
	}
	sealedRoot, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	declarations, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{candidateName: {ID: 71, Type: "repository-change/v1", Digest: candidateDigest}, "baseline": baseRef}, nil, snapshot.WithValidationAttestationAuthority(snapshot.ValidationAttestationAuthority{CandidateInput: candidateName, Candidate: snapshot.SnapshotRef{ID: 71, Type: "repository-change/v1", Digest: candidateDigest}, BaseInputs: []snapshot.ValidationAuthorityInput{{Input: "baseline", Ref: baseRef}}, ProfileDigest: contentDigest(profile), ProtectedConfigDigest: contentDigest(config), CapabilityImage: image, CapabilityImageDigest: imageDigest, WorkflowDefinitionID: 72, WorkflowVersion: 73, Toolchain: "dev-capability/" + imageDigest.String()}))
	if err != nil {
		t.Fatal(err)
	}
	validationValidator, err := registry.Lookup("validation/v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validationValidator.AdmitForSeal(context.Background(), sealedRoot, declarations); err != nil {
		_ = sealedRoot.Close()
		t.Fatalf("Task5 seal rejected exact candidate/base: %v", err)
	}
	_ = sealedRoot.Close()
	_ = baseArchive
}
