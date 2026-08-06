# Agent Instructions

## Running Tests

PostgreSQL must be running locally for unit and integration tests. Provision the
shared test service with `make test-postgres-up`, then check it with
`pg_isready -h 127.0.0.1 -p 15432 -U postgres`.

### Shared test PostgreSQL exception

`hack/test-postgres.sh` is the narrow exception to the repository Docker
provider rule: it uses the already-running local `colima` Docker context for
the dedicated `concourse-test-postgres` test container. It never starts Colima.
Every other Docker workflow retains the repository's documented provider rule.

### Quick Reference

| Command | What it runs | Time | Prerequisites |
|---------|-------------|------|---------------|
| `make test-unit` | 121 Ginkgo suites (atc, agent, fly, skymarshal, go-concourse, tracing) | ~8 min | PostgreSQL |
| `make test-quick` | Unit tests only (alias for `test-unit`) | ~8 min | PostgreSQL |
| `make test-dev-mcp` | Retained dev-mcp server module (see ci-agent/RETAINED.md) | ~3 sec | None |
| `make test-fly-integration` | Fly CLI against mock ATC (576 specs) | ~30 sec | None |
| `make test-integration` | ATC integration with real Postgres (21 specs) | ~12 sec | PostgreSQL |
| `make test-k8s-integration` | K8s integration via KinD cluster (117 specs) | ~23 min | Docker, KinD, Helm, kubectl |
| `make test-k8s-behavioral` | Full K8s behavioral (2 parallel KinD clusters, `K8S_PROCS=4` for more) | ~2-3 hrs | Docker, KinD, Helm, kubectl |
| `make test-all` | All tiers in order | ~2.5+ hrs | All of the above |

### Running a Single Package

```bash
ginkgo ./atc/db/                          # one package
ginkgo -r ./atc/api/                      # package + subpackages
ginkgo --focus="test name" ./atc/db/      # single test by name
```

### Running atc/db Tests

The `atc/db` suite is the largest (~1300 specs, ~2-3 min). It uses a
suite-owned template and unique cloned database for each spec, so independent
database-backed commands may run concurrently against the shared service.

### Key Notes

- Unit tests run in parallel (`-p` flag, 9 procs by default). Do not use `--race` — it causes parallel compilation failures (`fork/exec db.test: no such file or directory`).
- The `atc/db/worker_cache_test.go` uses `Eventually` with 10s timeouts and 500ms refresh intervals. These are timing-sensitive — do not reduce timeouts.
- K8s behavioral tests have ~3/117 flaky specs due to GC timing. This is expected.
- `testhelpers/otel` is excluded from `make test-unit` — it requires external Tempo/Loki services.
- `fly/integration` builds the fly binary and tests it against a mock ATC. The mock version must match `versions.go` (currently `0.1.0`).
