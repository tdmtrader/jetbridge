package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	seedRunnerImage = "registry.home/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bRunnerImage    = "registry.home/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cRunnerImage    = "registry.home/agent-runner@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	sourceCommit    = "0123456789abcdef0123456789abcdef01234567"
	runnerVersion   = "0.2.222"
)

type homeInfraFixture struct {
	t       *testing.T
	dir     string
	origin  string
	clone   string
	initial string
}

func newHomeInfraFixture(t *testing.T, image string) *homeInfraFixture {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	clone := filepath.Join(dir, "home-infra")
	runGit(t, dir, "-c", "init.defaultBranch=master", "init", "--bare", origin)
	runGit(t, dir, "init", "-b", "main", seed)
	if err := os.MkdirAll(filepath.Join(seed, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(seed, "apps", "concourse.yaml"), runnerManifest(image))
	runGit(t, seed, "add", "apps/concourse.yaml")
	runGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "seed")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "origin", "HEAD:main")
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, dir, "clone", origin, clone)
	return &homeInfraFixture{t: t, dir: dir, origin: origin, clone: clone, initial: gitOutput(t, origin, "rev-parse", "refs/heads/main")}
}

func runnerManifest(image string) string {
	return "image:\n  repository: registry.home/jetbridge\n  tag: rc-old\nweb:\n  env:\n      - { name: CONCOURSE_AGENT_STEP_IMAGE, value: \"" + image + "\" }\nspec:\n  ignoreDifferences:\n    - group: apps\n      kind: Deployment\n      name: concourse-web\n      namespace: cicd\n      jsonPointers:\n        - /spec/template/spec/containers/0/image\n    - group: apps\n      kind: StatefulSet\n      name: unrelated\n      namespace: cicd\n      jsonPointers:\n        - /spec/template/spec/containers/0/image\n        - /spec/replicas\n  syncPolicy:\n    automated:\n      selfHeal: true\n"
}

func writeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteWebImageHomeInfra(t *testing.T) {
	const webImage = "registry.home/jetbridge@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	const finalImage = "registry.home/jetbridge@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	fixture := newHomeInfraFixture(t, seedRunnerImage)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "deploy", "write-web-image-home-infra.sh")
	output, err := exec.Command("sh", helper, webImage, sourceCommit, fixture.clone).CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
	manifest := gitOutput(t, fixture.clone, "show", "HEAD:apps/concourse.yaml")
	for _, want := range []string{"repository: registry.home/jetbridge", "digest: sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "sourceCommit: " + sourceCommit} {
		if !strings.Contains(manifest, want) {
			t.Errorf("GitOps manifest lacks %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "testedDigest:") {
		t.Fatalf("candidate image writer created pre-test authority:\n%s", manifest)
	}
	if strings.Contains(manifest, "name: concourse-web") {
		t.Fatalf("candidate image writer preserved the legacy web image ignore rule:\n%s", manifest)
	}
	for _, preserved := range []string{"ignoreDifferences:", "name: unrelated", "/spec/template/spec/containers/0/image", "/spec/replicas", "syncPolicy:"} {
		if !strings.Contains(manifest, preserved) {
			t.Fatalf("candidate image writer removed unrelated Argo configuration %q:\n%s", preserved, manifest)
		}
	}
	message := gitOutput(t, fixture.clone, "log", "-1", "--format=%B")
	for _, want := range []string{"chore(deploy): pin web image", sourceCommit, webImage} {
		if !strings.Contains(message, want) {
			t.Errorf("commit message lacks %q:\n%s", want, message)
		}
	}
	if _, err := exec.Command("sh", helper, "registry.home/jetbridge:latest", sourceCommit, fixture.clone).CombinedOutput(); err == nil {
		t.Fatal("helper accepted mutable web image")
	}
	if output, err := exec.Command("sh", helper, finalImage, sourceCommit, fixture.clone).CombinedOutput(); err != nil {
		t.Fatalf("final helper update: %v\n%s", err, output)
	}
	manifest = gitOutput(t, fixture.clone, "show", "HEAD:apps/concourse.yaml")
	if !strings.Contains(manifest, "digest: sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee") || strings.Contains(manifest, "testedDigest:") {
		t.Fatalf("final update did not pin only the current image:\n%s", manifest)
	}
	before := gitOutput(t, fixture.clone, "rev-parse", "HEAD")
	if output, err := exec.Command("sh", helper, finalImage, sourceCommit, fixture.clone).CombinedOutput(); err != nil {
		t.Fatalf("equal image/source no-op: %v\n%s", err, output)
	}
	if got := gitOutput(t, fixture.clone, "rev-parse", "HEAD"); got != before {
		t.Fatalf("equal image/source created commit %s, want %s", got, before)
	}

	t.Run("removes_only_the_target_pointer_from_a_shared_rule", func(t *testing.T) {
		shared := newHomeInfraFixture(t, seedRunnerImage)
		manifest := runnerManifest(seedRunnerImage)
		manifest = strings.Replace(manifest,
			"        - /spec/template/spec/containers/0/image\n    - group: apps\n      kind: StatefulSet",
			"        - /spec/template/spec/containers/0/image\n        - /spec/replicas\n    - group: apps\n      kind: StatefulSet", 1)
		writeFixtureFile(t, filepath.Join(shared.clone, "apps", "concourse.yaml"), manifest)
		runGit(t, shared.clone, "add", "apps/concourse.yaml")
		runGit(t, shared.clone, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "add shared ignore pointer")

		if output, err := exec.Command("sh", helper, webImage, sourceCommit, shared.clone).CombinedOutput(); err != nil {
			t.Fatalf("helper: %v\n%s", err, output)
		}
		manifest = gitOutput(t, shared.clone, "show", "HEAD:apps/concourse.yaml")
		if !strings.Contains(manifest, "name: concourse-web") || strings.Count(manifest, "/spec/template/spec/containers/0/image") != 1 || strings.Count(manifest, "/spec/replicas") != 2 {
			t.Fatalf("helper did not preserve unrelated pointers while removing only the web image pointer:\n%s", manifest)
		}
	})

	for name, mutate := range map[string]func(string) string{
		"duplicate_target": func(manifest string) string {
			target := "    - group: apps\n      kind: Deployment\n      name: concourse-web\n      namespace: cicd\n      jsonPointers:\n        - /spec/template/spec/containers/0/image\n"
			return strings.Replace(manifest, target, target+target, 1)
		},
		"incomplete_target": func(manifest string) string {
			return strings.Replace(manifest, "      name: concourse-web\n      namespace: cicd\n", "      name: concourse-web\n", 1)
		},
	} {
		t.Run("rejects_"+name, func(t *testing.T) {
			ambiguous := newHomeInfraFixture(t, seedRunnerImage)
			writeFixtureFile(t, filepath.Join(ambiguous.clone, "apps", "concourse.yaml"), mutate(runnerManifest(seedRunnerImage)))
			runGit(t, ambiguous.clone, "add", "apps/concourse.yaml")
			runGit(t, ambiguous.clone, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "make ignore rule ambiguous")
			before := gitOutput(t, ambiguous.clone, "rev-parse", "HEAD")
			if output, err := exec.Command("sh", helper, webImage, sourceCommit, ambiguous.clone).CombinedOutput(); err == nil {
				t.Fatalf("helper accepted ambiguous legacy rule: %s", output)
			}
			if got := gitOutput(t, ambiguous.clone, "rev-parse", "HEAD"); got != before {
				t.Fatalf("rejected update created commit %s, want %s", got, before)
			}
			if got := gitOutput(t, ambiguous.clone, "status", "--porcelain"); got != "" {
				t.Fatalf("rejected update dirtied checkout:\n%s", got)
			}
		})
	}
}

func TestWriteLiveTestedImageHomeInfra(t *testing.T) {
	const testedImage = "registry.home/jetbridge@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	const retestedImage = "registry.home/jetbridge@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	fixture := newHomeInfraFixture(t, seedRunnerImage)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "deploy", "write-live-tested-image-home-infra.sh")
	if output, err := exec.Command("sh", helper, testedImage, sourceCommit, fixture.clone).CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
	want := "SOURCE_COMMIT=" + sourceCommit + "\nTESTED_IMAGE=" + testedImage
	if got := gitOutput(t, fixture.clone, "show", "HEAD:apps/concourse-live-tested-image.env"); got != want {
		t.Fatalf("attestation = %q, want %q", got, want)
	}
	message := gitOutput(t, fixture.clone, "log", "-1", "--format=%B")
	for _, value := range []string{"chore(deploy): attest live-tested web image", sourceCommit, testedImage} {
		if !strings.Contains(message, value) {
			t.Errorf("commit message lacks %q:\n%s", value, message)
		}
	}
	before := gitOutput(t, fixture.clone, "rev-parse", "HEAD")
	if output, err := exec.Command("sh", helper, testedImage, sourceCommit, fixture.clone).CombinedOutput(); err != nil {
		t.Fatalf("equal attestation no-op: %v\n%s", err, output)
	}
	if got := gitOutput(t, fixture.clone, "rev-parse", "HEAD"); got != before {
		t.Fatalf("equal attestation created commit %s, want %s", got, before)
	}
	if output, err := exec.Command("sh", helper, retestedImage, sourceCommit, fixture.clone).CombinedOutput(); err != nil {
		t.Fatalf("same-source retest update: %v\n%s", err, output)
	}
	want = "SOURCE_COMMIT=" + sourceCommit + "\nTESTED_IMAGE=" + retestedImage
	if got := gitOutput(t, fixture.clone, "show", "HEAD:apps/concourse-live-tested-image.env"); got != want {
		t.Fatalf("retest attestation = %q, want %q", got, want)
	}

	for _, args := range [][]string{
		{"registry.home/jetbridge:latest", sourceCommit},
		{"registry.home/jetbridge@sha256:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", sourceCommit},
		{testedImage, "0123"},
	} {
		rejected := newHomeInfraFixture(t, seedRunnerImage)
		if output, err := exec.Command("sh", helper, args[0], args[1], rejected.clone).CombinedOutput(); err == nil {
			t.Fatalf("helper accepted malformed attestation %q: %s", args, output)
		}
		rejected.assertUnchanged(t)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func (f *homeInfraFixture) runHelper(image, source, version string, repo string) ([]byte, error) {
	f.t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		f.t.Fatal(err)
	}
	if repo == "" {
		repo = f.clone
	}
	return exec.Command("sh", filepath.Join(root, "deploy", "write-agent-runner-home-infra.sh"), image, source, version, repo).CombinedOutput()
}

func (f *homeInfraFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	if got := gitOutput(t, f.clone, "rev-parse", "HEAD"); got != f.initial {
		t.Fatalf("local HEAD = %s, want %s", got, f.initial)
	}
	if got := gitOutput(t, f.clone, "status", "--porcelain"); got != "" {
		t.Fatalf("rejected fixture is dirty:\n%s", got)
	}
	if got := gitOutput(t, f.origin, "rev-parse", "refs/heads/main"); got != f.initial {
		t.Fatalf("bare main = %s, want %s", got, f.initial)
	}
}

func TestWriteAgentRunnerHomeInfra(t *testing.T) {
	t.Run("fixture_clone_checks_out_declared_main_branch", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, seedRunnerImage)
		if got := gitOutput(t, fixture.clone, "symbolic-ref", "--short", "HEAD"); got != "main" {
			t.Fatalf("fixture clone HEAD branch = %q, want main", got)
		}
		if _, err := os.Stat(filepath.Join(fixture.clone, "apps", "concourse.yaml")); err != nil {
			t.Fatalf("fixture clone must check out the main target file: %v", err)
		}
	})

	t.Run("success_changes_only_target_and_commits", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, seedRunnerImage)
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, ""); err != nil {
			t.Fatalf("helper: %v\n%s", err, output)
		}
		if got := gitOutput(t, fixture.clone, "diff", "HEAD^", "HEAD", "--name-only"); got != "apps/concourse.yaml" {
			t.Fatalf("changed files = %q", got)
		}
		diff := gitOutput(t, fixture.clone, "diff", "HEAD^", "HEAD", "--", "apps/concourse.yaml")
		var changedLines []string
		for _, line := range strings.Split(diff, "\n") {
			if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) ||
				(strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) {
				changedLines = append(changedLines, line)
			}
		}
		wantChangedLines := []string{
			"-      - { name: CONCOURSE_AGENT_STEP_IMAGE, value: \"" + seedRunnerImage + "\" }",
			"+      - { name: CONCOURSE_AGENT_STEP_IMAGE, value: \"" + bRunnerImage + "\" }",
		}
		if strings.Join(changedLines, "\n") != strings.Join(wantChangedLines, "\n") {
			t.Fatalf("changed lines = %#v, want only %#v\n%s", changedLines, wantChangedLines, diff)
		}
		message := gitOutput(t, fixture.clone, "log", "-1", "--format=%s%n%b")
		for _, want := range []string{"chore(deploy): pin agent runner v0.2.222", sourceCommit, bRunnerImage} {
			if !strings.Contains(message, want) {
				t.Fatalf("commit message lacks %q:\n%s", want, message)
			}
		}
		runGit(t, fixture.clone, "push", "origin", "HEAD:main")
		if got := gitOutput(t, fixture.origin, "show", "main:apps/concourse.yaml"); !strings.Contains(got, bRunnerImage) {
			t.Fatalf("pushed manifest lacks b digest:\n%s", got)
		}
	})

	t.Run("equal_digest_is_noop", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, bRunnerImage)
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, ""); err != nil {
			t.Fatalf("helper: %v\n%s", err, output)
		}
		fixture.assertUnchanged(t)
	})

	t.Run("rejects_malformed_or_mutable_reference", func(t *testing.T) {
		cases := []struct{ image, source, version string }{
			{"ghcr.io/tdmtrader/agent-runner:latest", sourceCommit, runnerVersion},
			{"ghcr.io/example/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", sourceCommit, runnerVersion},
			{"registry.home/agent-runner:latest", sourceCommit, runnerVersion},
			{"registry.home/other-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", sourceCommit, runnerVersion},
			{"registry.home/agent-runner@sha256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", sourceCommit, runnerVersion},
			{"registry.home/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", sourceCommit, runnerVersion},
			{bRunnerImage, "0123", runnerVersion},
			{bRunnerImage, "0123456789ABCDEF0123456789ABCDEF01234567", runnerVersion},
			{bRunnerImage, sourceCommit, "v0.2.222"},
			{bRunnerImage, sourceCommit, "0.2"},
		}
		for _, test := range cases {
			fixture := newHomeInfraFixture(t, seedRunnerImage)
			if output, err := fixture.runHelper(test.image, test.source, test.version, ""); err == nil {
				t.Fatalf("helper accepted image=%q source=%q version=%q: %s", test.image, test.source, test.version, output)
			}
			fixture.assertUnchanged(t)
		}
	})

	t.Run("rejects_duplicate_target", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, seedRunnerImage)
		path := filepath.Join(fixture.clone, "apps", "concourse.yaml")
		writeFixtureFile(t, path, runnerManifest(seedRunnerImage)+"      - { name: CONCOURSE_AGENT_STEP_IMAGE, value: \""+seedRunnerImage+"\" }\n")
		runGit(t, fixture.clone, "add", "apps/concourse.yaml")
		runGit(t, fixture.clone, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "duplicate target")
		before := gitOutput(t, fixture.clone, "rev-parse", "HEAD")
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, ""); err == nil {
			t.Fatalf("helper accepted duplicate target: %s", output)
		}
		if got := gitOutput(t, fixture.clone, "rev-parse", "HEAD"); got != before {
			t.Fatalf("duplicate created a commit: %s", got)
		}
		if got := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main"); got != fixture.initial {
			t.Fatalf("duplicate changed bare main: %s", got)
		}
	})

	t.Run("rejects_missing_target", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, seedRunnerImage)
		path := filepath.Join(fixture.clone, "apps", "concourse.yaml")
		writeFixtureFile(t, path, strings.ReplaceAll(runnerManifest(seedRunnerImage), "CONCOURSE_AGENT_STEP_IMAGE", "CONCOURSE_AGENT_PLATFORM_TOKEN_SECRET"))
		runGit(t, fixture.clone, "add", "apps/concourse.yaml")
		runGit(t, fixture.clone, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "missing target")
		before := gitOutput(t, fixture.clone, "rev-parse", "HEAD")
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, ""); err == nil {
			t.Fatalf("helper accepted missing target: %s", output)
		}
		if got := gitOutput(t, fixture.clone, "rev-parse", "HEAD"); got != before {
			t.Fatalf("missing target created a commit: %s", got)
		}
		if got := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main"); got != fixture.initial {
			t.Fatalf("missing target changed bare main: %s", got)
		}
	})

	t.Run("concurrent_target_change_fails_closed", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, seedRunnerImage)
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, ""); err != nil {
			t.Fatalf("helper: %v\n%s", err, output)
		}
		second := filepath.Join(fixture.dir, "second")
		runGit(t, fixture.dir, "clone", fixture.origin, second)
		secondPath := filepath.Join(second, "apps", "concourse.yaml")
		writeFixtureFile(t, secondPath, runnerManifest(cRunnerImage))
		runGit(t, second, "add", "apps/concourse.yaml")
		runGit(t, second, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "concurrent change")
		runGit(t, second, "push", "origin", "HEAD:main")
		if output, err := exec.Command("git", "-C", fixture.clone, "push", "origin", "HEAD:main").CombinedOutput(); err == nil {
			t.Fatalf("non-force push unexpectedly succeeded: %s", output)
		}
		if got := gitOutput(t, fixture.origin, "show", "main:apps/concourse.yaml"); !strings.Contains(got, cRunnerImage) {
			t.Fatalf("bare main lost concurrent c digest:\n%s", got)
		}
		fresh := filepath.Join(fixture.dir, "fresh")
		runGit(t, fixture.dir, "clone", fixture.origin, fresh)
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, fresh); err != nil {
			t.Fatalf("fresh helper rerun: %v\n%s", err, output)
		}
	})

	t.Run("accepts_linked_worktree_checkout", func(t *testing.T) {
		fixture := newHomeInfraFixture(t, seedRunnerImage)
		linked := filepath.Join(fixture.dir, "linked")
		runGit(t, fixture.clone, "worktree", "add", "-b", "linked", linked)
		if output, err := fixture.runHelper(bRunnerImage, sourceCommit, runnerVersion, linked); err != nil {
			t.Fatalf("helper in linked worktree: %v\n%s", err, output)
		}
		if got := gitOutput(t, linked, "log", "-1", "--format=%s"); got != "chore(deploy): pin agent runner v0.2.222" {
			t.Fatalf("linked worktree commit subject = %q", got)
		}
	})
}

func TestAgentRunnerPipelineWritesVerifiedHomeInfraDigest(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	var resource deployPipelineResource
	for _, candidate := range pipeline.Resources {
		if candidate.Name == "home-infra" {
			resource = candidate
		}
	}
	if resource.Type != "git" {
		t.Fatalf("home-infra type = %q, want git", resource.Type)
	}
	for key, want := range map[string]any{
		"uri": "https://github.com/tdmtrader/home-infra.git", "branch": "main", "username": "x-access-token", "password": "((github-token))", "disable_ci_skip": true,
	} {
		if got := resource.Source[key]; got != want {
			t.Errorf("home-infra source %s = %#v, want %#v", key, got, want)
		}
	}
	if len(resource.Source) != 5 {
		t.Errorf("home-infra source fields = %#v, want only trusted Git resource fields", resource.Source)
	}
	builder := findDeployPipelineTask(t, pipeline, "build-agent-runner-image", "build-and-push-agent-runner")
	if !builder.Privileged {
		t.Fatal("runner builder must remain privileged for DinD")
	}
	for _, input := range builder.Config.Inputs {
		if input.Name == "home-infra" {
			t.Fatal("privileged runner builder must not receive home-infra")
		}
	}
	builderScript := deployPipelineTaskScript(t, builder)
	for _, forbidden := range []string{"git clone", "git push", "GIT_ASKPASS"} {
		if strings.Contains(builderScript, forbidden) {
			t.Errorf("privileged runner builder contains %q", forbidden)
		}
	}
	if !strings.Contains(builderScript, "verified-image.env") {
		t.Fatal("privileged builder does not emit verified metadata")
	}
	if len(builder.Config.Outputs) != 1 || builder.Config.Outputs[0].Name != "runner-image-metadata" {
		t.Fatalf("runner builder outputs = %#v, want verified metadata output", builder.Config.Outputs)
	}
	var update deployPipelineStep
	updateIndex, putIndex := -1, -1
	for jobIndex := range pipeline.Jobs {
		job := &pipeline.Jobs[jobIndex]
		if job.Name != "build-agent-runner-image" {
			continue
		}
		for stepIndex, step := range job.Plan {
			if step.Task == "update-home-infra-agent-runner-image" {
				update, updateIndex = step, stepIndex
			}
			if step.Put == "home-infra" {
				putIndex = stepIndex
				if step.Timeout != "5m" || step.Params["repository"] != "home-infra-updated" || step.Params["rebase"] != true || len(step.Params) != 2 {
					t.Errorf("home-infra put = %#v, want rebase-only 5m put", step)
				}
			}
		}
	}
	if updateIndex < 0 || putIndex <= updateIndex {
		t.Fatalf("writeback ordering update=%d put=%d, want update before put", updateIndex, putIndex)
	}
	if update.Privileged || update.Config.Params != nil || update.Params != nil {
		t.Fatalf("writeback task privilege/params = privileged:%t config:%#v step:%#v", update.Privileged, update.Config.Params, update.Params)
	}
	if got := update.Config.Run.Args; len(got) < 2 || got[0] != "-euc" {
		t.Fatalf("writeback shell args = %#v, want sh -euc", got)
	}
	inputs := make(map[string]bool)
	for _, input := range update.Config.Inputs {
		inputs[input.Name] = true
	}
	if len(inputs) != 3 || !inputs["repo"] || !inputs["home-infra"] || !inputs["runner-image-metadata"] {
		t.Fatalf("writeback inputs = %#v", inputs)
	}
	if len(update.Config.Outputs) != 1 || update.Config.Outputs[0].Name != "home-infra-updated" {
		t.Fatalf("writeback outputs = %#v, want distinct modified repository artifact", update.Config.Outputs)
	}
	updateScript := deployPipelineTaskScript(t, update)
	for _, required := range []string{"sed -n 's/^CONCOURSE_AGENT_STEP_IMAGE=//p'", "SOURCE_COMMIT", "RUNNER_VERSION", "^registry.home/agent-runner@sha256:[a-f0-9]{64}$", "test \"$SOURCE_COMMIT\" = \"$(git -C repo rev-parse HEAD)\""} {
		if !strings.Contains(updateScript, required) {
			t.Errorf("writeback task lacks %q", required)
		}
	}
	requireTextOrder(t, updateScript,
		"cp -a home-infra/. home-infra-updated/",
		"test -e home-infra-updated/.git",
		"sh repo/deploy/write-agent-runner-home-infra.sh \"$CONCOURSE_AGENT_STEP_IMAGE\" \"$SOURCE_COMMIT\" \"$RUNNER_VERSION\" home-infra-updated",
	)
	for _, forbidden := range []string{"source ", ". runner-image-metadata", "set -x", "git push", "--force", "TOKEN", "http://", "https://"} {
		if strings.Contains(updateScript, forbidden) {
			t.Errorf("writeback task contains forbidden %q", forbidden)
		}
	}
	for _, job := range pipeline.Jobs {
		if job.Name != "self-upgrade" {
			continue
		}
		for _, step := range job.Plan {
			if step.Get == "repo" && strings.Join(step.Passed, ",") != "build-image,build-agent-runner-image" {
				t.Errorf("self-upgrade passed = %#v, want both image jobs", step.Passed)
			}
		}
	}
}
