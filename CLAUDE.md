# Agent Instructions

## Docker Runs on theborg

This Mac has **no Docker daemon** (Colima was removed 2026-08-02). Every step
that needs Docker runs against theborg. Do not propose `colima start`, install a
local Docker provider, or report a step blocked merely because `docker` cannot
connect.

```bash
./hack/borg-docker.sh up
eval "$(./hack/borg-docker.sh env)"
./hack/borg-docker.sh down
```

theborg has no Docker on the host either; it is k3s/containerd. Never install
Docker on the theborg host because it rewrites the host iptables FORWARD policy
and can break k3s/flannel on a live deployment. See
`docs/docker-on-theborg.md` for the full contract and limitations.

Container ports published inside the Docker-in-Docker pod are not reachable
from this Mac. The testcontainers-based Kubernetes integration and behavioral
suites therefore remain CI-only here. Jetbridge `live` tests need no Docker;
they use theborg's k3s directly in a throwaway namespace.

## Running Tests

PostgreSQL must already be running for database-backed unit and integration
tests. This repository does not start, stop, or provision that machine-wide
service. The default admin DSN is
`host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable`; set
`CONCOURSE_TEST_POSTGRES_DSN` to use another service. Verify readiness with
`make test-postgres-status` or `pg_isready -h 127.0.0.1 -p 15432 -U postgres`.

### Quick Reference

| Command | What it runs | Time | Prerequisites |
|---------|-------------|------|---------------|
| `make test-unit` | 155 Ginkgo suites (atc, agent, fly, skymarshal, go-concourse, tracing) | ~30 min | PostgreSQL |
| `make test-quick` | Unit tests only (alias for `test-unit`) | ~30 min | PostgreSQL |
| `make test-dev-mcp` | Retained dev-mcp server module (see ci-agent/RETAINED.md) | ~3 sec | None |
| `make test-fly-integration` | Fly CLI against mock ATC | ~30 sec | None |
| `make test-integration` | ATC integration with real PostgreSQL | ~12 sec | PostgreSQL |
| `make test-k8s-integration` | K8s integration via testcontainers K3s | ~23 min | Docker, Helm, kubectl — CI-only from this Mac |
| `make test-k8s-behavioral` | Full K8s behavioral suite | ~2-3 hrs | Docker, Helm, kubectl — CI-only from this Mac |
| `make test-all` | All tiers in order | ~2.5+ hrs | All of the above |

### Running a Single Package

```bash
ginkgo ./atc/db/
ginkgo -r ./atc/api/
ginkgo --focus="test name" ./atc/db/
```

### Running atc/db Tests

The `atc/db` suite is the largest. It creates one suite-owned template and a
unique cloned database for each spec, so independent database-backed commands
may run concurrently against the shared service.

### Shared PostgreSQL concurrency regression

`make test-postgres-concurrency` proves that two independent PostgreSQL-backed
packages overlap safely: every spec owns a distinct cloned database while the
external machine-wide service remains untouched. This guarantees PostgreSQL
isolation only; identical integration suites can still contend on application
HTTP ports.

### Key Notes

- Unit tests run in parallel (`-p`, nine processes by default). Do not use
  `--race`; it causes parallel compilation failures.
- `atc/db/worker_cache_test.go` uses 10-second `Eventually` timeouts and
  500-millisecond refresh intervals. Do not reduce them.
- K8s behavioral tests have a small number of GC-timing flakes.
- `testhelpers/otel` is excluded from `make test-unit`; it requires external
  Tempo/Loki services.
- `fly/integration` builds fly and tests it against a mock ATC. Its mock version
  must match `versions.go`.
