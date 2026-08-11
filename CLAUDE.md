# Agent Instructions

## Running Tests

PostgreSQL must be running locally for unit and integration tests. Check with `pg_isready`.

### Quick Reference

| Command | What it runs | Time | Prerequisites |
|---------|-------------|------|---------------|
| `make test-unit` | 69 Ginkgo suites (atc, fly, skymarshal, go-concourse, tracing) | ~3 min | PostgreSQL |
| `make test-elm` | Elm frontend (2972 specs) | ~30 sec | `yarn install` |
| `make test-quick` | Unit + Elm | ~5 min | PostgreSQL, yarn |
| `make test-fly-integration` | Fly CLI against mock ATC (591 specs) | ~30 sec | None |
| `make test-integration` | ATC integration with real Postgres (21 specs) | ~12 sec | PostgreSQL |
| `make test-k8s-integration` | K8s integration via testcontainers K3s | ~23 min | Docker, Helm, kubectl |
| `make test-k8s-behavioral` | Full K8s behavioral (2 parallel K3s containers, `K8S_PROCS=4` for more) | ~2-3 hrs | Docker, Helm, kubectl |
| `make test-all` | All tiers in order | ~2.5+ hrs | All of the above |

The K8s tiers create their cluster from inside the suite via
`testcontainers-go/modules/k3s`. There is no `kind` binary involved anywhere —
the tree has zero `sigs.k8s.io/kind` imports. Neither tier is viable on macOS
(containerd-in-Docker is unstable under Colima at any memory size), so run them
in CI.

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
- `fly/integration` builds the fly binary and tests it against a mock ATC. The mock's `atcVersion` (`0.1.0`, in `fly/integration/suite_test.go`) is a self-contained fixture — it is deliberately *not* tied to `versions.go`, and does not need updating when the release version moves. The specs that care about version skew set `flyVersion` from it explicitly.
- The three release version strings — the `VERSION` file, `JetBridgeVersion` in `versions.go`, and `appVersion` in `deploy/chart/Chart.yaml` — must agree. `TestVersionDeclarationsAgree` enforces it; nothing syncs them automatically.
- `web/public/elm.js` is a gitignored build intermediate. The tracked, served bundle is `web/public/elm.min.js` (see `web/public/index.html`), and `web/handler.go` embeds the whole `public` directory. After changing any Elm source, run `yarn run build` or the bundle goes stale.
