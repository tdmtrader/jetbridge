# MCP sidecar image packaging convention

Owner: dev-mcp workstream. Contract: 00-shared-contracts.md §8.5.
Consumers: gateway-mcp (`mcp-gateway`).

## Convention

| Aspect | Rule |
|---|---|
| Registry/name | `ghcr.io/tdmtrader/mcp-<name>` (`mcp-dev-concourse`, `mcp-platform`, `mcp-gateway`) |
| Version tag | the shipping repo's release tag when HEAD is tagged, else the short sha; `latest` is never pushed nor referenced (workflow-definition import validation rejects untagged/`latest` images) |
| Entrypoint | bare static Go binary (`ENTRYPOINT ["/usr/local/bin/<name>"]`, no flags, no hardcoded paths) serving streamable-HTTP MCP (`POST /mcp`) on `MCP_LISTEN_ADDR` (defaults: `:7780` dev, `:7781` platform, `:7782` gateway) |
| Workspace discovery | §8.5 CWD convention (co-signed dev-mcp, agent-step, harvest-step): images never hardcode a workspace path (no `/workspace`); every path-valued flag defaults relative to the process CWD (dev-mcp: `--config dev-mcp.yml`, `--workdir .`); the owning exec implementation sets the sidecar's `WorkingDir` to the workspace artifact's mount path inside the hashed build workdir (jetbridge falls back to the main container's Dir when no workspace artifact exists) |
| Health | `GET /healthz` → 200, used as the pod readiness probe |
| User | non-root (uid 1000) — **MCP sidecar images only**; main-step runner images (`agent-runner`, `harvest-runner`) run as **root** like every other step image, because jetbridge hostPath step volumes are kubelet-created root:root 0755 and fsGroup is ignored for hostPath (§8.5 scoping, 2026-07-09) |
| Gate | the image's contract-test kit MUST pass against the built image before push ("push on green") |

## CI job template

Plan 04 Task 13 (`docs/superpowers/plans/agentic-platform/04-dev-mcp.md`,
a live-cluster task) will add the `build-mcp-dev-image` job to
`deploy/concourse-pipeline.yml` as the copyable template: DinD pod →
`docker build` → run the container against the repo workspace → run the
contract kit against it (`go test ./agent/devmcp/e2e/ -run
TestLiveImageContract` with `DEV_MCP_ENDPOINT` set) → `docker push` on
green. New sidecar images will copy that job, swap the Dockerfile and the
contract-kit invocation (gateway ships its own kit per
the spec's testing approach).

## Known v1 limitations (mcp-dev-concourse)

- The image carries go + ginkgo + node/yarn but NO PostgreSQL: `run_tests`
  on the `atc`/`fly` components needs a reachable Postgres (CLAUDE.md), so
  the Task 13 CI job will exercise the self-contained `ci-agent` component
  instead.
  Full-suite gates against atc run where Postgres exists (harvest pods can
  mount one later; out of dev-mcp scope).
- `lint(web)` (`yarn run analyse`) requires `node_modules` installed in the
  workspace; run `yarn install` in the workspace first if you need it.
