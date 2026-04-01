.PHONY: test-unit test-ci-agent test-fly-integration test-integration test-k8s test-k8s-integration test-k8s-behavioral test-quick test-all proto build-native-agent

# Unit tests: all packages except integration/e2e suites (~5 min)
# Requires: PostgreSQL running locally
test-unit:
	@echo "==> Running unit tests..."
	ginkgo -r -p --keep-going --flake-attempts=1 \
		--skip-package=./integration,testflight,topgun,./worker/integration,./worker/runtime/integration,./worker/baggageclaim,ci-agent,fly/integration,testhelpers/otel

# CI-agent module tests (~2 min)
# Requires: nothing (self-contained)
test-ci-agent:
	@echo "==> Running ci-agent tests..."
	cd ci-agent && go test ./... -count=1 -timeout 5m

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

# K8s integration tests (~30 min)
# Requires: Docker, KinD, Helm, kubectl
# Creates a KinD cluster automatically
test-k8s-integration:
	@echo "==> Running K8s integration tests..."
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required"; exit 1; }
	@command -v kind >/dev/null 2>&1 || { echo "ERROR: kind is required"; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo "ERROR: helm is required"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is required"; exit 1; }
	go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m

# K8s behavioral tests (~2-3 hours)
# Requires: Docker, KinD, Helm, kubectl
# Creates one KinD cluster per parallel process.
# Default 2 procs; override with K8S_PROCS=4 if your machine has enough resources.
test-k8s-behavioral:
	@echo "==> Running K8s behavioral tests (this will take 2-3 hours)..."
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required"; exit 1; }
	@command -v kind >/dev/null 2>&1 || { echo "ERROR: kind is required"; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo "ERROR: helm is required"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is required"; exit 1; }
	ginkgo --procs=$${K8S_PROCS:-2} -v --timeout=3h ./topgun/k8s_behavioral/

# All K8s tests
test-k8s: test-k8s-integration test-k8s-behavioral

# Quick: unit + ci-agent only (~7 min)
# Good for local development iteration
test-quick: test-unit test-ci-agent

# All tests in order of speed
test-all: test-unit test-ci-agent test-fly-integration test-integration test-k8s

# Proto generation for native agent gRPC service
proto:
	protoc --go_out=. --go_opt=module=github.com/concourse/concourse \
	       --go-grpc_out=. --go-grpc_opt=module=github.com/concourse/concourse \
	       proto/native_agent.proto

# Build the native agent binary
build-native-agent:
	go build -o native-agent ./cmd/native-agent/
