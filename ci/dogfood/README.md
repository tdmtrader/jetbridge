# Dogfood plan-task runner (agentic-platform Phase 0)

Runs a bounded slice of a workstream plan from `docs/superpowers/plans/agentic-platform/`
BY AN AGENT ON JETBRIDGE (the live Concourse at concourse.home): agent executes the
plan tasks verbatim → test-quick gate → diff-aware review published on the build page →
branch pushed. See `docs/superpowers/plans/agentic-platform/ROADMAP.md` §"Execution
protocol: dogfooding".

## Quickstart

```sh
fly -t cicd login -c https://concourse.home    # once (target per FLY_TARGET, default cicd)
ci/dogfood/dispatch.sh docs/superpowers/plans/agentic-platform/03-pipeline-runs.md 3-6
# prints the fly watch command and web URL; on success the branch
# agent/dogfood-03-pipeline-runs-3-6 exists and the build page shows the review.
# Then: read the review + diff, run `make test-quick` locally (postgres up), merge.
```

Pieces: `deploy/dogfood-pipeline.yml` (pipeline), `ci-agent/phases/dogfood-implement.yaml`
(+ `phases/prompts/dogfood/tasks.md`), this script. Vars: `((plan_file))`, `((task_range))`,
`((base_branch))`, `((branch_name))`. If the web node lacks instanced pipelines, re-run
with `DOGFOOD_FLAT=1`.

## Safety notes

- **Cost per run is uncapped** (same posture as the live agent-review job — budgets land
  in wave 1). Expect roughly the cost of an interactive Claude Code session per task:
  a 3–4-task slice is typically single-digit dollars on `((agent-model))`, but a slice
  that loops on a failing test can burn much more. Keep TASK_RANGE small (2–6 tasks);
  the phase hard-stops at TIMEOUT=120m.
- **Review-before-merge protocol.** An `agent/dogfood-*` branch NEVER merges unreviewed:
  (1) read the published review on the build page and the branch diff; (2) run
  `make test-quick` locally with PostgreSQL up — the CI gate excludes postgres-backed
  suites (atc/db, atc/gc, ...) because the test-runner image has no postgres; (3) run the
  plan's own Execution-notes suites for the packages touched; (4) merge by hand.
- **Migration merge-order rule.** The migrator is version-pointer based: a lower-numbered
  migration merged AFTER a higher-numbered one deploys is never applied. Before merging
  any agent branch containing a migration, list every unmerged sibling branch's migration
  numbers and merge (and let theborg deploy) in ascending order — or hold deploys until
  all are merged. (ROADMAP §6, first bullet.)

## Operational notes

- **Always dispatch via `dispatch.sh`, not `fly trigger-job` alone.** The runner pipeline's
  task params and guard scripts live in the *server-stored* pipeline config. `trigger-job`
  reuses that stored config; it does NOT pick up edits to `deploy/dogfood-pipeline.yml` or
  a newer `((base_branch))` commit's pipeline structure. `dispatch.sh` re-runs
  `set-pipeline` every time, so it always applies the latest config. If you must hand-run a
  job after editing the pipeline YAML, re-run `set-pipeline` first. (Note: the git resource
  DOES fetch the latest repo commit, so agent/phase *code* is always current — only the
  pipeline's own params/steps are pinned to the last `set-pipeline`.)
- **Write-workload permissions (F25).** The implement task sets `AGENT_SKIP_PERMISSIONS=1`
  and `IS_SANDBOX=1` so headless claude can write files as root. Read-only phases (review)
  omit these and keep the default posture. If a future claude version changes the
  permission-bypass flag, the single point of change is `ci-agent/llm/client.go`.
- **A blocked/no-op agent fails loudly.** The implement task exits non-zero if it produces
  zero commits, and the push task independently refuses to push an empty branch. A green
  build therefore means the agent actually did work — but still read the diff before merging.
