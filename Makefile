.PHONY: test-unit test-fly-integration test-integration test-k8s test-k8s-integration test-k8s-behavioral test-quick test-all

# Unit tests: all packages except integration/e2e suites (~5 min)
# Requires: PostgreSQL running locally
test-unit:
	@echo "==> Running unit tests..."
	ginkgo -r -p --keep-going --flake-attempts=1 \
		--skip-package=./integration,testflight,topgun,./worker/integration,./worker/runtime/integration,./worker/baggageclaim,fly/integration,testhelpers/otel

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
# Requires: Docker, Helm, kubectl. The cluster is created by
# testcontainers-go/modules/k3s from inside the suite -- there is no `kind`
# binary involved anywhere (zero sigs.k8s.io/kind imports in the tree).
# Not viable on macOS: containerd-in-Docker is unstable under Colima at any
# memory size, so this tier is CI-only from a Mac.
test-k8s-integration:
	@echo "==> Running K8s integration tests..."
	@$(MAKE) --no-print-directory check-docker-tools
	go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m

# K8s behavioral tests (~2-3 hours)
# Requires: Docker, Helm, kubectl. One K3s container per parallel process.
# Default 2 procs; override with K8S_PROCS=4 if your machine has the headroom.
test-k8s-behavioral:
	@echo "==> Running K8s behavioral tests (this will take 2-3 hours)..."
	@$(MAKE) --no-print-directory check-docker-tools
	ginkgo --procs=$${K8S_PROCS:-2} -v --timeout=3h ./topgun/k8s_behavioral/

.PHONY: check-docker-tools
check-docker-tools:
	@command -v docker  >/dev/null 2>&1 || { echo "ERROR: docker is required";  exit 1; }
	@command -v helm    >/dev/null 2>&1 || { echo "ERROR: helm is required";    exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is required"; exit 1; }

# All K8s tests
test-k8s: test-k8s-integration test-k8s-behavioral

# Quick: unit tests only (~5 min)
# Good for local development iteration
test-quick: test-unit

# All tests in order of speed
test-all: test-unit test-fly-integration test-integration test-k8s
