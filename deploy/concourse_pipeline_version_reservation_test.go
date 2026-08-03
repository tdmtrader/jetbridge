package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestConcourseRCTagsReserveDistinctImmutableVersions(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	tagTask := findDeployPipelineTask(t, pipeline, "tag-rc", "create-rc-tag")
	script := deployPipelineTaskScript(t, tagTask)
	if strings.Contains(script, "git remote set-url origin") {
		t.Fatal("RC tag task embeds credentials in the remote URL, so the reservation fixture cannot safely execute")
	}
	if regexp.MustCompile(`(?m)^[[:space:]]*git[[:space:]]+tag[[:space:]]+-f`).MatchString(script) {
		t.Fatal("RC tag task can move an existing version reservation")
	}
	if regexp.MustCompile(`(?m)^[[:space:]]*git[[:space:]]+push[[:space:]]+(?:-f|--force)`).MatchString(script) {
		t.Fatal("RC tag task force-pushes a version reservation")
	}
	if strings.Contains(script, "--force-with-lease") {
		t.Fatal("RC tag task can replace a version reservation with force-with-lease")
	}

	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	repo := filepath.Join(dir, "repo")
	runGit(t, dir, "-c", "init.defaultBranch=main", "init", "--bare", origin)
	runGit(t, dir, "init", "-b", "main", repo)
	writeReleaseFixtureFile(t, filepath.Join(repo, "version.txt"), "stable\n", 0o644)
	runGit(t, repo, "add", "version.txt")
	runGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "stable")
	stableSource := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "tag", "v0.2.223")
	runGit(t, repo, "remote", "add", "origin", origin)
	runGit(t, repo, "push", "origin", "HEAD:main", "refs/tags/v0.2.223")

	runTagTaskRaw := func() ([]byte, error) {
		cmd := exec.Command("sh", "-ec", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GITHUB_TOKEN=test-token")
		return cmd.CombinedOutput()
	}
	runTagTask := func(label string) {
		t.Helper()
		if output, err := runTagTaskRaw(); err != nil {
			t.Fatalf("%s RC reservation: %v\n%s", label, err, output)
		}
	}

	writeReleaseFixtureFile(t, filepath.Join(repo, "version.txt"), "candidate one\n", 0o644)
	runGit(t, repo, "add", "version.txt")
	runGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "candidate one")
	firstSource := gitOutput(t, repo, "rev-parse", "HEAD")
	runTagTask("first candidate")
	if got := gitOutput(t, origin, "rev-parse", "refs/tags/v0.2.224-rc"); got != firstSource {
		t.Fatalf("first RC reservation = %s, want source %s", got, firstSource)
	}

	// A resource cache can retain an RC tag after it has been deleted remotely.
	// Once the corresponding stable version exists, that stale local tag must
	// not be treated as remote authority or resurrected by an old source.
	runGit(t, repo, "push", "origin", ":refs/tags/v0.2.224-rc")
	runGit(t, repo, "tag", "v0.2.224", stableSource)
	runGit(t, repo, "push", "origin", "refs/tags/v0.2.224")
	runTagTask("candidate with deleted cached reservation")
	if output, err := exec.Command("git", "--git-dir", origin, "rev-parse", "--verify", "refs/tags/v0.2.224-rc").CombinedOutput(); err == nil {
		t.Fatalf("deleted RC reservation was resurrected as %s", strings.TrimSpace(string(output)))
	}
	if got := gitOutput(t, origin, "rev-parse", "refs/tags/v0.2.225-rc"); got != firstSource {
		t.Fatalf("replacement RC reservation = %s, want source %s", got, firstSource)
	}

	writeReleaseFixtureFile(t, filepath.Join(repo, "version.txt"), "candidate two\n", 0o644)
	runGit(t, repo, "add", "version.txt")
	runGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "candidate two")
	secondSource := gitOutput(t, repo, "rev-parse", "HEAD")
	runTagTask("second candidate")
	if got := gitOutput(t, origin, "rev-parse", "refs/tags/v0.2.225-rc"); got != firstSource {
		t.Fatalf("second candidate moved v0.2.225-rc to %s, want preserved %s", got, firstSource)
	}
	if got := gitOutput(t, origin, "rev-parse", "refs/tags/v0.2.226-rc"); got != secondSource {
		t.Fatalf("second RC reservation = %s, want source %s", got, secondSource)
	}

	beforeReplay := gitOutput(t, origin, "show-ref", "--tags")
	runTagTask("idempotent replay")
	if got := gitOutput(t, origin, "show-ref", "--tags"); got != beforeReplay {
		t.Fatalf("RC reservation replay mutated tags:\n%s\nwant:\n%s", got, beforeReplay)
	}

	// A reservation that appeared since the candidate's prior view is never
	// moved. A fresh attempt observes it and allocates the next patch instead.
	runGit(t, repo, "tag", "v0.2.227-rc", firstSource)
	runGit(t, repo, "push", "origin", "refs/tags/v0.2.227-rc")
	writeReleaseFixtureFile(t, filepath.Join(repo, "version.txt"), "candidate three\n", 0o644)
	runGit(t, repo, "add", "version.txt")
	runGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "candidate three")
	thirdSource := gitOutput(t, repo, "rev-parse", "HEAD")
	runTagTask("candidate after conflicting reservation")
	if got := gitOutput(t, origin, "rev-parse", "refs/tags/v0.2.227-rc"); got != firstSource {
		t.Fatalf("allocator moved conflicting v0.2.227-rc to %s, want preserved %s", got, firstSource)
	}
	if got := gitOutput(t, origin, "rev-parse", "refs/tags/v0.2.228-rc"); got != thirdSource {
		t.Fatalf("post-conflict RC reservation = %s, want source %s", got, thirdSource)
	}

	// An already released source must not pass tag-rc without an RC that the
	// downstream source-bound consumers require.
	runGit(t, repo, "checkout", "--detach", stableSource)
	beforeStableReplay := gitOutput(t, origin, "show-ref", "--tags")
	if output, err := runTagTaskRaw(); err == nil {
		t.Fatalf("stable source unexpectedly passed RC reservation:\n%s", output)
	}
	if got := gitOutput(t, origin, "show-ref", "--tags"); got != beforeStableReplay {
		t.Fatalf("stable-source rejection mutated tags:\n%s\nwant:\n%s", got, beforeStableReplay)
	}
}

func TestConcourseVersionedBuildsUseExactSourceRCTag(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	build := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "build-image", "build-and-push-local"))
	runnerJob := findDeployPipelineJob(t, pipeline, "build-agent-runner-image")
	runner := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "build-agent-runner-image", "build-and-push-agent-runner"))
	verify := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "verify-upgrade", "check-running-version"))
	release := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "tag-push-release"))

	if len(runnerJob.Plan) == 0 || len(runnerJob.Plan[0].Passed) != 1 || runnerJob.Plan[0].Passed[0] != "tag-rc" {
		t.Fatalf("runner image repo gate = %#v, want exact source passed through tag-rc", runnerJob.Plan[0].Passed)
	}
	for label, script := range map[string]string{"build-image": build, "agent-runner": runner} {
		requireTextOrder(t, script,
			`git fetch --tags --force --prune --prune-tags origin`,
			`SOURCE_COMMIT=$(git rev-parse HEAD)`,
			`git tag --points-at "${SOURCE_COMMIT}" --list 'v0.2.*-rc'`,
		)
		for _, required := range []string{
			`SOURCE_COMMIT=$(git rev-parse HEAD)`,
			`git tag --points-at "${SOURCE_COMMIT}" --list 'v0.2.*-rc'`,
			`test "${RC_RELEASE_COUNT}" = 1`,
			`NEXT_VERSION="${RC_RELEASE_TAG#v}"`,
			`NEXT_VERSION="${NEXT_VERSION%-rc}"`,
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s does not derive its version from the source-bound RC reservation; missing %q", label, required)
			}
		}
		if strings.Contains(script, "LATEST_TAG") {
			t.Errorf("%s recomputes its version from the latest stable tag", label)
		}
	}
	for _, required := range []string{
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`git tag --points-at "${SOURCE_COMMIT}" --list 'v0.2.*-rc'`,
		`test "${RC_RELEASE_COUNT}" = 1`,
		`EXPECTED="${RC_RELEASE_TAG#v}"`,
	} {
		if !strings.Contains(verify, required) {
			t.Errorf("verify-upgrade does not derive its expectation from the source-bound RC reservation; missing %q", required)
		}
	}
	requireTextOrder(t, verify,
		`git fetch --tags --force --prune --prune-tags origin`,
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`git tag --points-at "${SOURCE_COMMIT}" --list 'v0.2.*-rc'`,
	)
	for label, script := range map[string]string{"build-image": build, "agent-runner": runner, "verify-upgrade": verify} {
		if strings.Contains(script, "LATEST_TAG") || strings.Contains(script, `$((PATCH + 1))`) {
			t.Errorf("%s still derives a global latest-stable version instead of its exact source reservation", label)
		}
	}
	if strings.Contains(release, `git push origin --delete "v${NEXT_VERSION}-rc"`) {
		t.Fatal("release deletes the immutable source-bound RC reservation")
	}
}

func findDeployPipelineJob(t *testing.T, pipeline deployPipeline, jobName string) deployPipelineJob {
	t.Helper()
	for _, job := range pipeline.Jobs {
		if job.Name == jobName {
			return job
		}
	}
	t.Fatalf("pipeline has no job %q", jobName)
	return deployPipelineJob{}
}
