# Agent Instructions

## Docker Runs on theborg

**Default every Docker step to theborg unless told otherwise.** Do not call a step blocked
just because `docker` cannot connect — bring up the dind pod instead.

```bash
./hack/borg-docker.sh up             # docker:dind pod on theborg's k3s + local forward
eval "$(./hack/borg-docker.sh env)"  # DOCKER_HOST=tcp://127.0.0.1:12375 (linux/amd64)
./hack/borg-docker.sh down           # when finished — theborg hosts the live Concourse
```

Colima *is* installed locally (profile `default`, re-created 2026-08-06; an earlier note
claiming it was removed on 2026-08-02 was wrong). It is stopped by default and is **not**
the default target — use it only when the user asks for a local run, e.g. something whose
published ports must be reachable from this Mac. Note its `diffdisk` is a sparse image: it
reports 100 GB to `ls` while occupying only what is written (`du` gives the true figure), so
it is rarely the cause of low disk.

```bash
colima start --cpu 4 --memory 8      # resizes the existing stopped profile, non-destructive
```

theborg has no Docker on the host either (it is k3s/containerd). **Never install Docker on the theborg host** — it rewrites the host iptables FORWARD policy and can break k3s/flannel on a live cluster.

Known limit: ports published by containers live in the pod's netns and are **not** reachable from this Mac, so the testcontainers-based K8s suites remain CI-only. Full detail, including the two untested ways to close that gap: `docs/docker-on-theborg.md`.

The `jetbridge` `live` tests need no Docker — they talk to theborg's k3s directly in a throwaway namespace.

## Running Tests

PostgreSQL must be running locally for unit and integration tests. Check with `pg_isready`.

### Quick Reference

| Command | What it runs | Time | Prerequisites |
|---------|-------------|------|---------------|
| `make test-unit` | 121 Ginkgo suites (atc, agent, fly, skymarshal, go-concourse, tracing) | ~8 min | PostgreSQL |
| `make test-quick` | Unit tests only (alias for `test-unit`) | ~8 min | PostgreSQL |
| `make test-dev-mcp` | Retained dev-mcp server module (see ci-agent/RETAINED.md) | ~3 sec | None |
| `make test-fly-integration` | Fly CLI against mock ATC (576 specs) | ~30 sec | None |
| `make test-integration` | ATC integration with real Postgres (21 specs) | ~12 sec | PostgreSQL |
| `make test-k8s-integration` | K8s integration via testcontainers K3s (117 specs) | ~23 min | Docker, Helm, kubectl — **CI-only from this Mac** |
| `make test-k8s-behavioral` | Full K8s behavioral (2 parallel K3s containers, `K8S_PROCS=4` for more) | ~2-3 hrs | Docker, Helm, kubectl — **CI-only from this Mac** |
| `make test-all` | All tiers in order | ~2.5+ hrs | All of the above |

### Running a Single Package

```bash
ginkgo ./atc/db/                          # one package
ginkgo -r ./atc/api/                      # package + subpackages
ginkgo --focus="test name" ./atc/db/      # single test by name
```

### Running atc/db Tests

The `atc/db` suite is the largest (~1300 specs, ~2-3 min). It uses a template database for fast setup. If you see `database "testdb_template" already exists`, another test process is still running — wait for it or kill it.

### Key Notes

- Unit tests run in parallel (`-p` flag, 9 procs by default). Do not use `--race` — it causes parallel compilation failures (`fork/exec db.test: no such file or directory`).
- The `atc/db/worker_cache_test.go` uses `Eventually` with 10s timeouts and 500ms refresh intervals. These are timing-sensitive — do not reduce timeouts.
- K8s behavioral tests have ~3/117 flaky specs due to GC timing. This is expected.
- `testhelpers/otel` is excluded from `make test-unit` — it requires external Tempo/Loki services.
- `fly/integration` builds the fly binary and tests it against a mock ATC. The mock version must match `versions.go` (currently `0.1.0`).
