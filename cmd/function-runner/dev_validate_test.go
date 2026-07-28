package main

import (
	"bytes"
	"context"
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

// imageBakedDevCapabilityRunner executes a real temporary dev-capability
// binary, while accepting the compiled image path as its input. Production
// does not use this adapter: it always executes the fixed absolute
// /usr/local/bin/dev-capability baked into the digest-pinned validator image.
// This is the narrow host-test seam for exercising function-runner's actual
// flag parsing and validation materialization without rebuilding or publishing
// an image.
type imageBakedDevCapabilityRunner struct{ binary string }

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
	args := []string{"--root", root, "--candidate", candidateName, "--workspace", "workspace", "--output", "validation", "--candidate-type", "opaque/v1", "--candidate-id", "71", "--candidate-digest", candidateDigest.String(), "--profile-name", "exact", "--profile", profilePath, "--config", configPath, "--capability-image", image, "--workflow-definition-id", "72", "--workflow-version", "73"}
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
