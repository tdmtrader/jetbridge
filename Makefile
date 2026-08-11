.PHONY: test-unit test-dev-mcp test-bench-harness test-fly-integration test-integration test-hangar-integration test-k8s test-k8s-integration test-k8s-behavioral test-quick test-all build-agent-broker-image test-agent-broker-smoke check-docker-tools

AGENT_BROKER_IMAGE ?= concourse-agent-broker:dev

# Build the linux/amd64 managed broker companion. This packages the reviewed
# inputs but does not waive the promotion blocker recorded in the operator
# guide. Published profiles must use the registry-reported @sha256 digest,
# never this local tag.
build-agent-broker-image:
	docker build --platform linux/amd64 \
		--file deploy/agent-broker/Dockerfile \
		--tag "$(AGENT_BROKER_IMAGE)" \
		.

# Explicit local/CI smoke gate. It uses fake native harness processes and a
# fake durable authority, so it needs no provider credential or PostgreSQL.
# The real PostgreSQL and Kubernetes gates are tracked separately in the
# implementation report.
test-agent-broker-smoke:
	@test "$$CONCOURSE_AGENT_BROKER_SMOKE" = "1" || { echo "ERROR: set CONCOURSE_AGENT_BROKER_SMOKE=1 to run the broker smoke gate"; exit 1; }
	go test ./agent/broker/adapter -run 'Test(ExecuteRunsWithoutShellAndDecodesTheNativeStream|NativeAdaptersBuildControlledInvocations|PreflightAcceptsOnlyThePackagedReleaseVersionFixtures)$$' -count=1
	go test ./agent/broker -run 'TestEngine(RunsConsultationThroughDurablePhases|CapturesReviewWorkspaceAfterAdmissionAndRunsExactCapture)$$' -count=1
	go test ./agent/broker/mcp -run 'TestServer(ExposesOnlyTheTwoNeutralTools|CallsConsultAgentSynchronously)$$' -count=1

# Unit tests: all packages except integration/e2e suites (~5 min)
# Requires: PostgreSQL running locally
#
# `bench` is skipped, not excused: bench/corpus/*/ground_truth/withheld_tests
# are SEALED fixtures — verbatim copies of tests as they existed at each case's
# terminal artifact, deliberately frozen at a past tree state. They are graded
# against a materialized pre_state, never against HEAD, so compiling them here
# only reports that the tree has moved on since the case was harvested.
test-unit:
	@echo "==> Running unit tests..."
	ginkgo -r -p --keep-going --flake-attempts=1 \
		--skip-package=./integration,testflight,topgun,./worker/integration,./worker/runtime/integration,./worker/baggageclaim,fly/integration,testhelpers/otel,agent/schema,ci-agent,bench
	cd agent/schema && go test ./... -count=1

# Retained dev-mcp server (see ci-agent/RETAINED.md)
# Requires: nothing
test-dev-mcp:
	@echo "==> Running dev-mcp server tests..."
	cd ci-agent && go test ./... -count=1

# Bench harness: out-of-band graders for bench/corpus (~1 sec)
# Requires: nothing. Separate module, so `make test-unit` never compiles it.
test-bench-harness:
	@echo "==> Running bench harness tests..."
	cd bench/harness && go test ./... -count=1

# Fly integration tests (~10 min)
# Requires: nothing (uses mock HTTP server)
test-fly-integration:
	@echo "==> Running fly integration tests..."
	ginkgo -r --keep-going ./fly/integration/

# ATC integration tests (~10 min)
# Requires: PostgreSQL running locally
test-integration:
	@echo "==> Running ATC integration tests..."
	ginkgo -r --keep-going -p ./atc/integration/

# Hangar durable GCS contract (~2 min)
# Requires: a running Docker daemon, or CONCOURSE_HANGAR_TEST_GCS_ENDPOINT
# pointed at the in-cluster-compatible emulator endpoint.
test-hangar-integration:
	@echo "==> Running Hangar integration contracts..."
	@command -v docker >/dev/null 2>&1 || { test -n "$$CONCOURSE_HANGAR_TEST_GCS_ENDPOINT" || { echo "ERROR: docker is required unless CONCOURSE_HANGAR_TEST_GCS_ENDPOINT is set"; exit 1; }; }
	@if test -z "$$CONCOURSE_HANGAR_TEST_GCS_ENDPOINT"; then docker info >/dev/null 2>&1 || { echo "ERROR: a running Docker daemon is required"; exit 1; }; fi
	go test -tags=integration ./agent/hangar -run '^Test(FakeGCSContainerRegistersCleanupBeforeReturningStartError|GCSStoreFakeServer)$$' -count=1 -v

# Shared prerequisite check for the Docker-backed K8s tiers.
# These suites create their cluster with testcontainers (rancher/k3s), NOT KinD --
# no `kind` binary is invoked anywhere in topgun/, so it is not checked for.
# There is no local Docker daemon on the dev Mac; see docs/docker-on-theborg.md.
check-docker-tools:
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: the docker CLI is required"; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo "ERROR: helm is required"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is required"; exit 1; }
	@docker info >/dev/null 2>&1 || { \
		echo "ERROR: no reachable Docker daemon (DOCKER_HOST=$${DOCKER_HOST:-unset})."; \
		echo "       Docker runs on theborg, not locally:"; \
		echo "         ./hack/borg-docker.sh up && eval \"\$$(./hack/borg-docker.sh env)\""; \
		echo "       See docs/docker-on-theborg.md"; exit 1; }

# K8s integration tests (~30 min)
# Requires: a reachable Docker daemon, Helm, kubectl. Creates a K3s cluster
# via testcontainers. NOTE: currently CI-only -- testcontainers reaches the K3s
# API server on a published port, which is not routable from the dev Mac when
# the daemon is the theborg dind pod. See docs/docker-on-theborg.md.
test-k8s-integration: check-docker-tools
	@echo "==> Running K8s integration tests..."
	go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m

# K8s behavioral tests (~2-3 hours)
# Requires: a reachable Docker daemon, Helm, kubectl.
# Creates one testcontainers K3s cluster per parallel process.
# Default 2 procs; override with K8S_PROCS=4 if your machine has enough resources.
# Same CI-only caveat as test-k8s-integration.
test-k8s-behavioral: check-docker-tools
	@echo "==> Running K8s behavioral tests (this will take 2-3 hours)..."
	ginkgo --procs=$${K8S_PROCS:-2} -v --timeout=3h ./topgun/k8s_behavioral/

# All K8s tests
test-k8s: test-k8s-integration test-k8s-behavioral

# Quick: unit tests only (~3 min)
# Good for local development iteration
test-quick: test-unit

# All tests in order of speed
test-all: test-unit test-fly-integration test-integration test-k8s

AGENT_RUNNER_IMAGE ?= concourse-agent-runner:dev
.PHONY: build-agent-runner-image test-agent-runner-smoke
build-agent-runner-image:
	docker build --platform linux/amd64 \
		--file deploy/agent-runner/Dockerfile \
		--tag "$(AGENT_RUNNER_IMAGE)" \
		.

test-agent-runner-smoke:
	@test "$$CONCOURSE_AGENT_RUNNER_SMOKE" = "1" || { echo "ERROR: set CONCOURSE_AGENT_RUNNER_SMOKE=1 to run the runner smoke gate"; exit 1; }
	docker run --rm --platform linux/amd64 --entrypoint /usr/local/bin/agent-runner-image-smoke "$(AGENT_RUNNER_IMAGE)"
