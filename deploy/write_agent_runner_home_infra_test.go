package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	seedRunnerImage = "ghcr.io/tdmtrader/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bRunnerImage    = "ghcr.io/tdmtrader/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cRunnerImage    = "ghcr.io/tdmtrader/agent-runner@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
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
	runGit(t, dir, "init", "--bare", origin)
	runGit(t, dir, "init", "-b", "main", seed)
	if err := os.MkdirAll(filepath.Join(seed, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(seed, "apps", "concourse.yaml"), runnerManifest(image))
	runGit(t, seed, "add", "apps/concourse.yaml")
	runGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "seed")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "origin", "HEAD:main")
	runGit(t, dir, "clone", origin, clone)
	return &homeInfraFixture{t: t, dir: dir, origin: origin, clone: clone, initial: gitOutput(t, origin, "rev-parse", "refs/heads/main")}
}

func runnerManifest(image string) string {
	return "web:\n  env:\n      - { name: CONCOURSE_AGENT_STEP_IMAGE, value: \"" + image + "\" }\n"
}

func writeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
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
			{"ghcr.io/tdmtrader/agent-runner@sha256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", sourceCommit, runnerVersion},
			{"ghcr.io/tdmtrader/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", sourceCommit, runnerVersion},
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
				if step.Timeout != "5m" || step.Params["repository"] != "home-infra" || step.Params["rebase"] != true || len(step.Params) != 2 {
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
	updateScript := deployPipelineTaskScript(t, update)
	for _, required := range []string{"sed -n 's/^CONCOURSE_AGENT_STEP_IMAGE=//p'", "SOURCE_COMMIT", "RUNNER_VERSION", "write-agent-runner-home-infra.sh", "test \"$SOURCE_COMMIT\" = \"$(git -C repo rev-parse HEAD)\""} {
		if !strings.Contains(updateScript, required) {
			t.Errorf("writeback task lacks %q", required)
		}
	}
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
