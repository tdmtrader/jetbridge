package deploy

import (
	"regexp"
	"strings"
	"testing"
)

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
