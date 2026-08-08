# Local development and test environments

## PostgreSQL-backed local tests

Database-backed tests use one externally managed machine-wide PostgreSQL
service. This repository never starts, stops, or recreates it. By default the
runner connects to `127.0.0.1:15432` as `postgres`; set
`CONCOURSE_TEST_POSTGRES_DSN` to select another admin DSN.

```bash
make test-postgres-status
make test-quick
make test-integration
```

Every suite creates one migrated template and every spec creates its own named
clone, so independent database-backed packages may overlap safely. Verify that
contract with `make test-postgres-concurrency`. It guarantees PostgreSQL
isolation only—identical integration suites can still contend on application
HTTP ports.

Packages that do not use PostgreSQL, such as `make test-dev-mcp` and
`make test-fly-integration`, do not need the service.

## Docker and Kubernetes workflows

This Mac has no local Docker provider. Do not install or start Colima. All
Docker work—including image builds and Docker CLI use—runs through the
Docker-in-Docker pod on theborg. Follow [Docker on theborg](docker-on-theborg.md):

```bash
./hack/borg-docker.sh up
eval "$(./hack/borg-docker.sh env)"
```

Container ports published inside that pod are not reachable from this Mac, so
the testcontainers-based Kubernetes integration and behavioral suites remain
CI-only here. Tear the pod down with `./hack/borg-docker.sh down` when Docker
work is complete; theborg hosts the live Concourse and has encountered disk
pressure before.
