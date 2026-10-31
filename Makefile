.PHONY: test-unit test-elm test-brine-guards test-fly-integration test-k8s test-k8s-integration test-k8s-behavioral test-quick test-all

# Unit tests: every package except the excluded tiers (~9 min)
# Requires: PostgreSQL binaries on PATH (the suites start their own postmaster)
#
# The package and exclusion lists live in hack/unit-tests.sh, which the
# pipeline's unit-tests job runs too (with --ci). Change them there.
test-unit:
	@echo "==> Running unit tests..."
	hack/unit-tests.sh

# Elm frontend tests (~30 sec)
# Requires: yarn install (elm-test comes from node_modules)
#
# This tier existed but was wired into nothing, so it rotted: by the time it was
# first run on this branch two suites no longer compiled against the Session
# record. Keep it in test-quick -- it is the cheapest tier we have, and a
# frontend that does not compile is a frontend that does not ship.
test-elm:
	@echo "==> Running Elm tests..."
	yarn --silent run test

# Fly integration tests (~35 sec)
# Requires: nothing (uses mock HTTP server)
#
# The pipeline's unit-tests job runs this target after the unit tier, so it is
# also the CI invocation. The Ginkgo CLI comes from go.mod through go run: the
# CI test runner has no ginkgo on PATH, and the module pins the version.
test-fly-integration:
	@echo "==> Running fly integration tests..."
	go run github.com/onsi/ginkgo/v2/ginkgo -r -p --keep-going ./fly/integration/

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

# Brine guard tests (~1 sec)
# Requires: nothing -- no cluster, no build. Nested module (its own go.mod), so
# root go test never reaches it; run with a cd into it.
test-brine-guards:
	@echo "==> Running brine guard tests..."
	cd atc/worker/jetbridge/brine && go test ./steps/ -run 'Guard|Import|TempDir|ActiveFeatureCorpus|Definition|NoStepLine' -count=1

.PHONY: check-docker-tools
check-docker-tools:
	@command -v docker  >/dev/null 2>&1 || { echo "ERROR: docker is required";  exit 1; }
	@command -v helm    >/dev/null 2>&1 || { echo "ERROR: helm is required";    exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is required"; exit 1; }

# All K8s tests
test-k8s: test-k8s-integration test-k8s-behavioral

# Quick: unit + Elm + brine guards (~5 min)
# Good for local development iteration
test-quick: test-unit test-elm test-brine-guards

# All tests in order of speed
test-all: test-unit test-elm test-fly-integration test-k8s
