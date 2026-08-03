package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	replayedReleaseDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	replayedTestedImage   = "registry.home/jetbridge@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestConcourseStableTagReplayReusesPublishedSourceImage(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	release := findDeployPipelineTask(t, pipeline, "release", "tag-push-release")
	fixture := newReleaseReplayFixture(t, "JetBridge 0.2.223 (Concourse 11.5.0)")

	output, err := fixture.run(t, deployPipelineTaskScript(t, release))
	if err != nil {
		t.Fatalf("stable release replay: %v\n%s", err, output)
	}

	wantMetadata := "CONCOURSE_WEB_IMAGE=registry.home/jetbridge@" + replayedReleaseDigest +
		"\nSOURCE_COMMIT=" + fixture.sourceCommit
	if got := strings.TrimSpace(readReleaseFixtureFile(t, filepath.Join(fixture.dir, "release-image-metadata", "verified-image.env"))); got != wantMetadata {
		t.Fatalf("replayed release metadata = %q, want verified reused digest %q", got, wantMetadata)
	}
	if got := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main"); got != fixture.sourceCommit {
		t.Fatalf("replayed release left main at %s, want source %s", got, fixture.sourceCommit)
	}
}

func TestConcourseStableTagReplayRejectsUnverifiedPublishedImage(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	release := findDeployPipelineTask(t, pipeline, "release", "tag-push-release")
	fixture := newReleaseReplayFixture(t, "JetBridge 0.2.224 (Concourse 11.5.0)")

	output, err := fixture.run(t, deployPipelineTaskScript(t, release))
	if err == nil {
		t.Fatalf("stable release replay accepted a source-addressed image with the wrong version:\n%s", output)
	}
	if !strings.Contains(string(output), "verified release image version") {
		t.Fatalf("stable release replay failed for the wrong reason: %v\n%s", err, output)
	}
	if got := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main"); got != fixture.initialMain {
		t.Fatalf("rejected replay advanced main to %s, want unchanged %s", got, fixture.initialMain)
	}
	metadata := filepath.Join(fixture.dir, "release-image-metadata", "verified-image.env")
	if _, statErr := os.Stat(metadata); !os.IsNotExist(statErr) {
		t.Fatalf("rejected replay produced release metadata at %s", metadata)
	}
}

func TestConcourseReleaseWritebackRefreshesBeforeApplyingReplayedDigest(t *testing.T) {
	const finalImage = "registry.home/jetbridge@" + replayedReleaseDigest
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	writeback := findDeployPipelineTask(t, pipeline, "release", "update-home-infra-release-image")
	fixture := newHomeInfraFixture(t, seedRunnerImage)

	earlier := filepath.Join(fixture.dir, "earlier-release")
	runGit(t, fixture.dir, "clone", fixture.origin, earlier)
	helper := filepath.Join(".", "write-web-image-home-infra.sh")
	helperSource := readReleaseFixtureFile(t, helper)
	if err := os.MkdirAll(filepath.Join(fixture.dir, "repo", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeReleaseFixtureFile(t, filepath.Join(fixture.dir, "repo", "deploy", "write-web-image-home-infra.sh"), helperSource, 0o755)
	if output, err := exec.Command("sh", helper, finalImage, sourceCommit, earlier).CombinedOutput(); err != nil {
		t.Fatalf("earlier release writeback: %v\n%s", err, output)
	}
	runGit(t, earlier, "push", "origin", "HEAD:main")
	remoteHead := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main")

	metadataDir := filepath.Join(fixture.dir, "release-image-metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeReleaseFixtureFile(t, filepath.Join(metadataDir, "verified-image.env"),
		"CONCOURSE_WEB_IMAGE="+finalImage+"\nSOURCE_COMMIT="+sourceCommit+"\n", 0o600)
	if err := os.Mkdir(filepath.Join(fixture.dir, "home-infra-updated"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-euc", deployPipelineTaskScript(t, writeback))
	cmd.Dir = fixture.dir
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN=test-token")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("replayed release writeback: %v\n%s", err, output)
	}
	updated := filepath.Join(fixture.dir, "home-infra-updated")
	if got := gitOutput(t, updated, "rev-parse", "HEAD"); got != remoteHead {
		t.Fatalf("replayed writeback HEAD = %s, want refreshed remote head %s", got, remoteHead)
	}
	if got := gitOutput(t, updated, "status", "--porcelain"); got != "" {
		t.Fatalf("replayed writeback left refreshed checkout dirty:\n%s", got)
	}
}

func TestConcourseReleaseImageUsesFinalStampedServer(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	buildScript := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "build-image", "build-and-push-local"))
	releaseScript := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "tag-push-release"))

	for _, required := range []string{
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`RC_IMAGE="registry.home/jetbridge:rc-${SOURCE_COMMIT}"`,
		`-t "${RC_IMAGE}"`,
		`docker push "${RC_IMAGE}"`,
		`RC_LDFLAGS="-s -w -X github.com/concourse/concourse.Version=${NEXT_VERSION}-rc -X github.com/concourse/concourse.JetBridgeVersion=${NEXT_VERSION}-rc"`,
		`FINAL_LDFLAGS="-s -w -X github.com/concourse/concourse.Version=${NEXT_VERSION} -X github.com/concourse/concourse.JetBridgeVersion=${NEXT_VERSION}"`,
		`COPY concourse-linux-amd64 /usr/local/concourse/bin/concourse`,
		`COPY concourse-linux-amd64-final /usr/local/concourse/bin/concourse-final`,
	} {
		if !strings.Contains(buildScript, required) {
			t.Errorf("build-image script lacks release contract %q", required)
		}
	}
	requireTextOrder(t, buildScript,
		`cp -a ../web-public/. web/public/`,
		`go build -ldflags "${RC_LDFLAGS}" -o concourse-linux-amd64 ./cmd/concourse`,
		`go build -ldflags "${FINAL_LDFLAGS}" -o concourse-linux-amd64-final ./cmd/concourse`,
		`kubectl cp concourse-linux-amd64 cicd/${BUILDER_POD}:/tmp/concourse-linux-amd64`,
		`kubectl cp concourse-linux-amd64-final cicd/${BUILDER_POD}:/tmp/concourse-linux-amd64-final`,
		`COPY concourse-linux-amd64-final /usr/local/concourse/bin/concourse-final`,
		`docker build`,
	)

	if regexp.MustCompile(`(?m)go build[^\n]*\./cmd/concourse`).MatchString(releaseScript) {
		t.Fatal("release task rebuilds the server from its raw checkout")
	}
	for _, required := range []string{
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`RC_IMMUTABLE_IMAGE="${ATTESTED_IMAGE}"`,
		`FROM __RC_IMAGE__`,
		`sed -i "s|__RC_IMAGE__|${RC_IMMUTABLE_IMAGE}|g" /tmp/Dockerfile.release`,
		`FINAL_TAG="registry.home/jetbridge:release-${SOURCE_COMMIT}"`,
		`FINAL_IMAGE="registry.home/jetbridge@${FINAL_DIGEST}"`,
		`RUN mv /usr/local/concourse/bin/concourse-final /usr/local/concourse/bin/concourse`,
		`COPY fly-assets /usr/local/concourse/fly-assets`,
		`--entrypoint /usr/local/concourse/bin/concourse`,
		`EXPECTED_FINAL_VERSION="JetBridge ${NEXT_VERSION} (Concourse ${UPSTREAM_VERSION})"`,
		`test "${FINAL_VERSION_OUTPUT}" = "${EXPECTED_FINAL_VERSION}"`,
		`grep -Fq -- "-rc"`,
	} {
		if !strings.Contains(releaseScript, required) {
			t.Errorf("release script lacks final-image contract %q", required)
		}
	}
	requireTextOrder(t, releaseScript,
		`docker build`,
		`FINAL_VERSION_OUTPUT=$(kubectl exec`,
		`--entrypoint /usr/local/concourse/bin/concourse`,
		`grep -Fq -- "-rc"`,
		`test "${FINAL_VERSION_OUTPUT}" = "${EXPECTED_FINAL_VERSION}"`,
		`PUSH_OUTPUT=$(kubectl exec -n cicd ${BUILDER_POD} -- docker push "${FINAL_TAG}")`,
		`FINAL_IMAGE="registry.home/jetbridge@${FINAL_DIGEST}"`,
		`git config --global user.email`,
		`git tag -a "v${NEXT_VERSION}"`,
	)
}

func TestConcourseReleaseVersionIsBoundToTheTestedSource(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	release := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "tag-push-release"))

	for _, required := range []string{
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`git tag --points-at "${SOURCE_COMMIT}"`,
		`STABLE_RELEASE_TAG`,
		`RC_RELEASE_TAG`,
		`NEXT_VERSION="${STABLE_RELEASE_TAG#v}"`,
		`RC_VERSION="${RC_RELEASE_TAG#v}"`,
		`RC_VERSION="${RC_VERSION%-rc}"`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release version is not source-bound; missing %q", required)
		}
	}
	if strings.Contains(release, `NEXT_VERSION="${MAJOR}.${MINOR}.$((PATCH + 1))"`) {
		t.Fatal("release still advances from the latest stable tag, so a retry after partial publication skips a version")
	}
}

func TestConcourseReleaseConsumesLiveTestedImmutableImage(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	release := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "tag-push-release"))
	attestation := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "validate-live-tested-image"))

	for _, required := range []string{
		`deployment/concourse-web`,
		`daemonset/concourse-artifact-daemon`,
		`concourse.ci/source-commit`,
		`concourse.ci/image-digest`,
		`test "${web_source}" = "${SOURCE_COMMIT}"`,
		`test "${web_image}" = "${web_digest}"`,
		`test "${daemon_image}" = "${daemon_digest}"`,
		`ATTESTED_IMAGE=$(sed -n 's/^TESTED_IMAGE=//p' release-tested-image/attestation.env)`,
		`ATTESTED_SOURCE=$(sed -n 's/^SOURCE_COMMIT=//p' release-tested-image/attestation.env)`,
		`test "${ATTESTED_SOURCE}" = "${SOURCE_COMMIT}"`,
		`RC_IMMUTABLE_IMAGE="${ATTESTED_IMAGE}"`,
		`docker pull "${RC_IMMUTABLE_IMAGE}"`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release does not consume the exact live-tested digest; missing %q", required)
		}
	}
	if strings.Contains(release, `docker pull "${RC_IMAGE}"`) || strings.Contains(release, `registry.home/jetbridge:rc-${SOURCE_COMMIT}`) || strings.Contains(release, `concourse.ci/tested-rc-image`) {
		t.Fatal("release re-resolves a mutable source tag instead of consuming the tested digest")
	}
	for _, required := range []string{
		`apps/concourse-live-tested-image.env`,
		`SOURCE_COMMIT`,
		`TESTED_IMAGE`,
		`test "${ATTESTED_SOURCE}" = "$(git -C repo rev-parse HEAD)"`,
	} {
		if !strings.Contains(attestation, required) {
			t.Errorf("release attestation validation lacks %q", required)
		}
	}
}

func TestK8sLiveTestsPublishAttestationOnlyAfterPassing(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	live := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "k8s-live-tests", "k8s-live-integration-tests"))
	writeback := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "k8s-live-tests", "write-home-infra-live-tested-image"))

	for _, required := range []string{
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`registry.home/jetbridge@sha256:[a-f0-9]{64}`,
		`concourse.ci/source-commit`,
		`concourse.ci/image-digest`,
		`printf 'SOURCE_COMMIT=%s\nTESTED_IMAGE=%s\n'`,
		`> ../live-tested-image-metadata/attestation.env`,
	} {
		if !strings.Contains(live, required) {
			t.Errorf("live-test task lacks post-test attestation contract %q", required)
		}
	}
	requireTextOrder(t, live,
		`go test -tags live`,
		`printf 'SOURCE_COMMIT=%s\nTESTED_IMAGE=%s\n'`,
		`> ../live-tested-image-metadata/attestation.env`,
	)
	for _, required := range []string{
		`live-tested-image-metadata/attestation.env`,
		`sh repo/deploy/write-live-tested-image-home-infra.sh`,
		`home-infra-updated`,
	} {
		if !strings.Contains(writeback, required) {
			t.Errorf("live-test attestation writeback lacks %q", required)
		}
	}
}

func TestConcoursePipelineDeploysResolvedDigestToEveryRuntime(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	resolve := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "self-upgrade", "resolve-and-write-home-infra-web-image"))
	selfUpgrade := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "self-upgrade", "trigger-rollout"))
	verify := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "verify-upgrade", "check-running-version"))
	release := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "tag-push-release"))
	releaseWriteback := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "update-home-infra-release-image"))
	releaseRollout := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "verify-release-rollout"))

	for _, required := range []string{
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`RC_IMAGE="registry.home/jetbridge:rc-${SOURCE_COMMIT}"`,
		`--image="${RC_IMAGE}"`,
		`--image-pull-policy=Always`,
		`IMAGE_ID=$(kubectl get pod`,
		`WEB_IMAGE="registry.home/jetbridge@${DIGEST}"`,
		`sh deploy/write-web-image-home-infra.sh "${WEB_IMAGE}" "${SOURCE_COMMIT}" ../home-infra-updated`,
		`CONCOURSE_WEB_IMAGE=%s\nSOURCE_COMMIT=%s\nRC_IMAGE=%s\n`,
	} {
		if !strings.Contains(resolve, required) {
			t.Errorf("self-upgrade resolver lacks immutable deployment contract %q", required)
		}
	}
	for _, required := range []string{
		`Waiting for GitOps to deploy ${WEB_IMAGE} from ${RC_IMAGE}`,
		`deployment/concourse-web`,
		`daemonset/concourse-artifact-daemon`,
		`concourse.ci/source-commit`,
		`concourse.ci/image-digest`,
	} {
		if !strings.Contains(selfUpgrade, required) {
			t.Errorf("self-upgrade rollout lacks durable GitOps check %q", required)
		}
	}
	for _, required := range []string{
		`EXPECTED_RC_IMAGE="registry.home/jetbridge:rc-${SOURCE_COMMIT}"`,
		`concourse.ci/source-commit`,
		`concourse.ci/image-digest`,
		`concourse-artifact-daemon`,
	} {
		if !strings.Contains(verify, required) {
			t.Errorf("verify-upgrade lacks repository/image coupling check %q", required)
		}
	}
	for _, required := range []string{
		`CONCOURSE_WEB_IMAGE=%s\nSOURCE_COMMIT=%s\n`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release lacks immutable deployment contract %q", required)
		}
	}
	if !strings.Contains(releaseWriteback, `sh repo/deploy/write-web-image-home-infra.sh "${WEB_IMAGE}" "${SOURCE_COMMIT}" home-infra-updated`) {
		t.Error("release GitOps writeback does not pin the final immutable image")
	}
	for _, required := range []string{
		`deployment/concourse-web`,
		`daemonset/concourse-artifact-daemon`,
		`concourse.ci/source-commit`,
		`concourse.ci/image-digest`,
	} {
		if !strings.Contains(releaseRollout, required) {
			t.Errorf("release rollout lacks immutable workload check %q", required)
		}
	}
}

func TestConcourseReleasePublishesMainFailClosed(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	releaseScript := deployPipelineTaskScript(t, findDeployPipelineTask(t, pipeline, "release", "tag-push-release"))

	if !strings.Contains(releaseScript, "sh deploy/push-jetbridge-main.sh .") {
		t.Fatal("release task does not invoke the fail-closed main publication helper")
	}
	if regexp.MustCompile(`(?m)^[[:space:]]*git[[:space:]]+push[[:space:]]+(?:--force|-f)[^\n]*HEAD:refs/heads/main[[:space:]]*$`).MatchString(releaseScript) {
		t.Fatal("release task force-pushes main")
	}
	requireTextOrder(t, releaseScript,
		`git push origin "v${NEXT_VERSION}"`,
		"sh deploy/push-jetbridge-main.sh .",
		"CONCOURSE_WEB_IMAGE=%s\\nSOURCE_COMMIT=%s\\n",
	)
}

type releaseReplayFixture struct {
	dir          string
	origin       string
	initialMain  string
	sourceCommit string
	fakeBin      string
	realGit      string
	version      string
}

func newReleaseReplayFixture(t *testing.T, version string) *releaseReplayFixture {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "repo")
	runGit(t, dir, "-c", "init.defaultBranch=main", "init", "--bare", origin)
	runGit(t, dir, "init", "-b", "main", seed)
	if err := os.MkdirAll(filepath.Join(seed, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeReleaseFixtureFile(t, filepath.Join(seed, "versions.go"), "package concourse\n\nvar ConcourseVersion = \"11.5.0\"\n", 0o644)
	writeReleaseFixtureFile(t, filepath.Join(seed, "README.md"), "initial\n", 0o644)
	writeReleaseFixtureFile(t, filepath.Join(seed, "deploy", "push-jetbridge-main.sh"),
		readReleaseFixtureFile(t, "push-jetbridge-main.sh"), 0o755)
	runGit(t, seed, "add", ".")
	runGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "origin", "HEAD:main")
	initialMain := gitOutput(t, seed, "rev-parse", "HEAD")

	writeReleaseFixtureFile(t, filepath.Join(seed, "release-source.txt"), "tested source\n", 0o644)
	runGit(t, seed, "add", "release-source.txt")
	runGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "release source")
	sourceCommit := gitOutput(t, seed, "rev-parse", "HEAD")
	runGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "tag", "-a", "v0.2.223", "-m", "Release v0.2.223")
	runGit(t, seed, "push", "origin", "HEAD:jetbridge", "refs/tags/v0.2.223")
	runGit(t, dir, "clone", "--branch", "jetbridge", origin, repo)

	for _, output := range []string{"release-tested-image", "release-image-metadata"} {
		if err := os.Mkdir(filepath.Join(dir, output), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeReleaseFixtureFile(t, filepath.Join(dir, "release-tested-image", "attestation.env"),
		"SOURCE_COMMIT="+sourceCommit+"\nTESTED_IMAGE="+replayedTestedImage+"\n", 0o600)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(dir, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeReleaseFixtureFile(t, filepath.Join(fakeBin, "git"), `#!/bin/sh
set -eu
if test "${1-}" = config && test "${2-}" = --global; then
  exit 0
fi
if test "${1-}" = remote && test "${2-}" = set-url; then
  exit 0
fi
exec "${TEST_REAL_GIT}" "$@"
`, 0o755)
	writeReleaseFixtureFile(t, filepath.Join(fakeBin, "go"), `#!/bin/sh
echo "FATAL: stable release replay attempted to rebuild Go artifacts: $*" >&2
exit 97
`, 0o755)
	writeReleaseFixtureFile(t, filepath.Join(fakeBin, "tar"), `#!/bin/sh
echo "FATAL: stable release replay attempted to rebuild fly archives: $*" >&2
exit 98
`, 0o755)
	writeReleaseFixtureFile(t, filepath.Join(fakeBin, "kubectl"), `#!/bin/sh
set -eu
case "$*" in
  *"get deployment/concourse-web"*"concourse.ci/source-commit"*|*"get daemonset/concourse-artifact-daemon"*"concourse.ci/source-commit"*)
    printf '%s' "${TEST_SOURCE_COMMIT}"
    ;;
  *"get deployment/concourse-web"*"concourse.ci/image-digest"*|*"get daemonset/concourse-artifact-daemon"*"concourse.ci/image-digest"*)
    printf '%s' "${TEST_RELEASE_IMAGE}"
    ;;
  *"get deployment/concourse-web"*|*"get daemonset/concourse-artifact-daemon"*)
    printf '%s' "${TEST_RELEASE_IMAGE}"
    ;;
  *"docker info"*)
    ;;
  *"docker pull ${TEST_RELEASE_TAG}"*)
    printf 'release pull complete\nDigest: %s\n' "${TEST_RELEASE_DIGEST}"
    ;;
  *"docker pull ${TEST_RELEASE_IMAGE}"*)
    printf 'immutable pull complete\nDigest: %s\n' "${TEST_RELEASE_DIGEST}"
    ;;
  *"docker image inspect"*|*"docker inspect"*)
    printf '%s\n' "${TEST_RELEASE_IMAGE}"
    ;;
  *"docker run "*" --version"*)
    printf '%s\n' "${TEST_RELEASE_VERSION}"
    ;;
  *"docker build"*|*"docker push"*)
    echo "FATAL: stable release replay attempted image mutation: $*" >&2
    exit 99
    ;;
  *"delete pod"*|*"apply -n cicd -f -"*|*"wait --for=condition=ready"*)
    ;;
  *)
    echo "FATAL: unexpected kubectl call: $*" >&2
    exit 96
    ;;
esac
`, 0o755)

	return &releaseReplayFixture{
		dir:          dir,
		origin:       origin,
		initialMain:  initialMain,
		sourceCommit: sourceCommit,
		fakeBin:      fakeBin,
		realGit:      realGit,
		version:      version,
	}
}

func (f *releaseReplayFixture) run(t *testing.T, script string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("sh", "-exc", script)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(),
		"PATH="+f.fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_TOKEN=test-token",
		"TEST_REAL_GIT="+f.realGit,
		"TEST_RELEASE_DIGEST="+replayedReleaseDigest,
		"TEST_RELEASE_IMAGE=registry.home/jetbridge@"+replayedReleaseDigest,
		"TEST_RELEASE_TAG=registry.home/jetbridge:release-"+f.sourceCommit,
		"TEST_RELEASE_VERSION="+f.version,
		"TEST_SOURCE_COMMIT="+f.sourceCommit,
	)
	return cmd.CombinedOutput()
}

func readReleaseFixtureFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func writeReleaseFixtureFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func requireTextOrder(t *testing.T, text string, markers ...string) {
	t.Helper()
	position := 0
	for _, marker := range markers {
		relative := strings.Index(text[position:], marker)
		if relative < 0 {
			t.Fatalf("missing %q after byte %d", marker, position)
		}
		position += relative + len(marker)
	}
}
