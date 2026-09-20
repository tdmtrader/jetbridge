# Building the CI test runner

The runner packages the Linux amd64 toolchain, PostgreSQL 17, Helm, envtest,
and the Brine CLI/engine. The pipeline supplies the source under test as its
`repo` input; the image contains no application checkout or private Brine source.

Brine's authoritative revision is the replacement pseudo-version in
`atc/worker/jetbridge/brine/go.mod`. Resolve its full commit and pass it as the
required `BRINE_COMMIT` build argument. The Dockerfile fetches that exact commit,
checks it, builds the CLI and engine with Cargo's lockfile, and records it at
`/etc/brine-commit`.

Use a native amd64 Docker/BuildKit builder with access to GitHub and the existing
internal registry. In the CI cluster, the documented Docker-in-Docker pattern
requires MTU 1450; use an isolated disposable builder, its Unix socket, resource
limits and no service-account token. Delete it after the run so private source
and intermediate build caches are removed. Publish only the final `runner`
stage; do not export builder caches or intermediate images.

## Tags

- `v10` — contract 5, brine-private `1e9345da`.
- `v11` — same Brine pin; adds what the brine harness's local tier needs on
  the runner: `gcc`/`libc6-dev` (compiles `scripts/netns-exec.c` for the
  unprivileged user+net namespace the adapter runs in), a pinned upstream
  BusyBox at `/usr/local/bin/busybox` (real fetch-script applets), and a
  pinned `otelcol` (real OTLP collector for the trace scenarios). Published
  2026-09-19 (manifest digest `sha256:1e1fa432a1d3…`); every job in
  `deploy/concourse-pipeline.yml` pins it. The registry ingress serves a
  Traefik default certificate, so `docker push` fails TLS verification;
  `docker save` the image and `crane push` the tarball over the plain-HTTP
  ingress instead, then confirm the tag's digest with `crane digest`.

## Build

The example names the current tag. Once a tag is published, treat it as
immutable: task images use `PullIfNotPresent`. A subsequent image change needs
a new tag and updated consuming pipeline references.

```sh
set -eu
set +x
runner_tag=v11
runner_image="registry.home/concourse-test-runner:${runner_tag}"
brine_module_pin=$(awk '/^replace github.com\/brine-dev\/brine-go =>/ { print $NF }' atc/worker/jetbridge/brine/go.mod)
test -n "$brine_module_pin"
brine_short=${brine_module_pin##*-}
brine_commit=$(gh api "repos/MarkDucommun/brine-private/commits/${brine_short}" --jq .sha)
case "$brine_commit" in "$brine_short"*) ;; *) exit 1 ;; esac
runner_context=$(mktemp -d)
trap 'unset GH_TOKEN; rm -rf "$runner_context"' EXIT
cp go.mod go.sum "$runner_context/"
cp deploy/Dockerfile.test-runner "$runner_context/Dockerfile"
export GH_TOKEN=$(gh auth token)
docker build --platform linux/amd64 --target runner \
  --secret id=gh,env=GH_TOKEN \
  --build-arg "BRINE_COMMIT=${brine_commit}" \
  -t "$runner_image" "$runner_context"
unset GH_TOKEN
```

The GitHub credential is supplied through a BuildKit secret, and Git reads it
through `GIT_ASKPASS`. Do not put it in build arguments, URLs or traced commands.
The runner's public root-module cache can be populated without that credential.

## Verify, publish and exercise

Before pushing, run the image and check `/etc/brine-commit` against the resolved
SHA; record hashes for `brine` and `brine-engine`. Check `go`, PostgreSQL, Helm,
kubectl, Docker and both envtest executables. Confirm `/brine`, Git credentials
and the build secret are absent from the final image.

Push the final image with `docker push "$runner_image"`, record the returned
manifest digest, and verify that the registry serves that digest for the tag.
Then run the pipeline's tasks against an immutable candidate revision:

```sh
hack/ci-check.sh <candidate-ref> build-and-vet unit-tests brine
```

The Brine task needs its existing `GITHUB_TOKEN` pipeline variable for the
private Go adapter dependency; that credential is not baked into the runner.
It also needs four pipeline variables that do not exist until someone creates
them (the task errors on an undefined `((var))` before a scenario runs), and
one chart value on the cluster it runs in:

| Pipeline variable | Task env | Value |
|---|---|---|
| `brine-allow-hostpath-tests` | `BRINE_ALLOW_HOSTPATH_TESTS` | `1` to approve the artifact-handoff hostPath fixture on this cluster |
| `brine-allow-hostport-tests` | `BRINE_ALLOW_HOSTPORT_TESTS` | `1` to approve that fixture's hostPort daemon |
| `brine-artifact-node` | `BRINE_LIVE_ARTIFACT_NODE` | the one node approved to carry the fixture's hostPath |
| `brine-artifact-daemon-port` | `BRINE_LIVE_ARTIFACT_DAEMON_PORT` | an unused TCP port in 49152–60999 on that node |

The live tier runs under the task pod's ServiceAccount and creates its own
namespaces, so the chart must be deployed with `rbac.brineLive=true`
(`deploy/chart/templates/brine-live-rbac.yaml`); a cluster still rendering a
chart revision without that template grants nothing and every live scenario
403s. `hack/ci-check.sh` forwards each `((var))` from an environment
variable of the same name upper-cased with `_` for `-`
(`BRINE_ALLOW_HOSTPATH_TESTS`, `BRINE_ARTIFACT_NODE`, ...), and names any
that are missing before it runs the job.
A passing build or `brine --version` alone does not establish contract-5
compatibility: the Brine CLI must actually execute the current adapter and
produce a non-empty passing verdict.

Apply the consuming pipeline configuration together with the compatible adapter
migration. Pointing an old flags-based adapter at the contract-5 runtime would
mix the two protocols. Publishing the runner image does not deploy JetBridge or
apply the pipeline configuration.
