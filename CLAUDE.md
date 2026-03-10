# Agent Instructions

## Running Tests

PostgreSQL must be running locally for unit and integration tests. Check with `pg_isready`.

### Quick Reference

| Command | What it runs | Time | Prerequisites |
|---------|-------------|------|---------------|
| `make test-unit` | 79 Ginkgo suites (atc, fly, skymarshal, go-concourse, tracing) | ~3 min | PostgreSQL |
| `make test-ci-agent` | ci-agent Go module (`cd ci-agent && go test ./...`) | ~2 min | None |
| `make test-quick` | Unit + ci-agent combined | ~5 min | PostgreSQL |
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

The `atc/db` suite is the largest (~1007 specs, ~90s). It uses a template database for fast setup. If you see `database "testdb_template" already exists`, another test process is still running — wait for it or kill it.

### Key Notes

- Unit tests run in parallel (`-p` flag, 9 procs by default). Do not use `--race` — it causes parallel compilation failures (`fork/exec db.test: no such file or directory`).
- The `atc/db/worker_cache_test.go` uses `Eventually` with 10s timeouts and 500ms refresh intervals. These are timing-sensitive — do not reduce timeouts.
- K8s behavioral tests have ~3/117 flaky specs due to GC timing. This is expected.
- `testhelpers/otel` is excluded from `make test-unit` — it requires external Tempo/Loki services.
- `fly/integration` builds the fly binary and tests it against a mock ATC. The mock version must match `versions.go` (currently `0.1.0`).
