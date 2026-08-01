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
		`docker push registry.home/jetbridge:v${NEXT_VERSION}`,
		`git config --global user.email`,
		`git tag -a "v${NEXT_VERSION}"`,
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
