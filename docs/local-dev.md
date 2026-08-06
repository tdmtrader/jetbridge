# Local development and test environments

## PostgreSQL-backed local tests

The shared `concourse-test-postgres` service is the narrow local Docker
exception. It uses the existing Colima runtime only for PostgreSQL-backed test
packages; `make test-postgres-up` never starts Colima itself.

```bash
make test-postgres-up
eval "$(./hack/test-postgres.sh env)"
make test-quick
make test-integration
```

The named service deliberately stays running so independent database-backed
package commands can overlap. Every spec owns a cloned database; verify that
contract with `make test-postgres-concurrency`. It guarantees PostgreSQL
isolation only—identical integration suites can still contend on application
HTTP ports.

`make test-unit`, `make test-quick`, and `make test-integration` need the
existing Colima runtime and shared PostgreSQL service. Packages that do not
use PostgreSQL, such as `make test-dev-mcp` and `make test-fly-integration`,
do not need either one.

## Docker and Kubernetes workflows

All other Docker work—including image builds, Docker CLI use, and Kubernetes
test suites—uses theborg. Follow [Docker on theborg](docker-on-theborg.md):

```bash
./hack/borg-docker.sh up
eval "$(./hack/borg-docker.sh env)"
```

Do not start or install another local Docker provider for those workflows.
Container ports published inside theborg's Docker-in-Docker pod are not
reachable from this Mac, so the testcontainers-based Kubernetes integration
and behavioral suites remain CI-only from this environment. Tear the pod down
with `./hack/borg-docker.sh down` when Docker work is complete.
