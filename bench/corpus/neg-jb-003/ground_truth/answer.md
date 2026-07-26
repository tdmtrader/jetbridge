# Answer key — neg-jb-003

**Verdict: working as intended. There is no defect and no change is warranted.**

All references below are as of the pre-state commit
`644184e3f011369f3da77dc82caee200bd8fd196` (2026-07-19T13:46:04-07:00).

---

## 1. The mechanism

The `repo` git resource in `deploy/concourse-pipeline.yml` declares
`ignore_paths`. Lines 1-15 at pre-state, verbatim:

```yaml
resources:
- name: repo
  type: git
  source:
    uri: https://github.com/tdmtrader/jetbridge.git
    branch: jetbridge
    # Docs-only churn must not trigger the self-release chain (every push
    # restarts web ~10-12 min later and double-spends in-flight dogfood
    # builds — see ci/dogfood/FINDINGS.md "Pushing to jetbridge mid-run").
    # Deliberately narrow: ci/** and deploy/** still trigger.
    ignore_paths:
    - docs/**
    - ci/dogfood/FINDINGS.md
    - notes/**
    - forge/**
```

Note the near-miss: `deploy/borg-pipeline.yml` declares a *same-named* `repo`
git resource on the same URI and branch with **no** `ignore_paths`, and its own
`build-and-vet` job. That is a different pipeline (its chain is
`build-and-vet` → … → `deploy` → `promote-to-main`; it has no `tag-rc` and no
`self-upgrade`). The pipeline the reporter describes is the one with `tag-rc`
and `self-upgrade`, i.e. `deploy/concourse-pipeline.yml`, and that is the file
whose resource carries the filter.

`ignore_paths` is a property of the **`git` resource type**, not of the ATC.
The resource's `check` walks the commits on `branch: jetbridge` and does not
emit a version for a commit whose changed paths all match `ignore_paths`. So:

- The reported commit `644184e3f0` touches exactly one file,
  `docs/superpowers/plans/agentic-platform/2026-07-19-ux4-scoping.md`
  (`git show --stat 644184e3f0` — 1 file, +17). It matches `docs/**`.
- The check therefore produces **no new resource version**. This is consistent
  with everything the reporter observed: the checker is alive, checks are not
  erroring, and the last-check timestamp keeps advancing — the check runs and
  succeeds, it just has nothing new to report.
- `build-and-vet` is the only job that gets `repo` with `trigger: true`
  (`deploy/concourse-pipeline.yml`, `- name: build-and-vet` → `- get: repo` /
  `trigger: true`). No new version means no trigger, so no build. Every
  downstream job is gated on `passed: [build-and-vet]`, so the whole chain
  stays idle.

Nothing is being dropped. The version was never created.

## 2. Why the filter exists (design intent)

Documented in two places that both exist at pre-state:

- **The comment immediately above the stanza** (quoted in §1): docs churn must
  not trigger the self-release chain because every push restarts web ~10-12
  minutes later and double-spends in-flight dogfood builds.
- **`ci/dogfood/FINDINGS.md` §"Loop / harness friction"** (line 58 ff.),
  "Pushing to `jetbridge` mid-dogfood-run restarts web and double-spends the
  agent": build **525330**'s log shows the implement task ran TWICE — two full
  Claude sessions, two `dogfood-implement: pass` results — plus three gate
  executions, because a morning docs push (`748a797a1b`) triggered the release
  chain whose `self-upgrade` restarted the web node at 15:58Z, mid-gate. The
  build survived the restart (build-survival working), but the resumed build
  re-ran the implement step from scratch because the worked-repo volume died
  with the old task pod. Each such restart costs a full agent run out of a
  shared rate-limit window. The same entry lists the filter as leftward fix
  candidate (a): "path-filter the release chain's git resource to ignore
  docs-only commits (`docs/**`, `ci/dogfood/FINDINGS.md`)".

The filter is **deliberately narrow**. `ci/**` and `deploy/**` are *not*
ignored — pipeline and agent-loop changes must keep deploying. Only
`docs/**`, `ci/dogfood/FINDINGS.md`, `notes/**` and `forge/**` are filtered.

Provenance: the stanza was added by commit
`34025022b8aad2b39e85a90abff231cfe0da66d7`
("ci(deploy): ignore docs-only churn on the jetbridge release trigger",
2026-07-11), which is an **ancestor** of the pre-state and whose message states
all of the above. See `ground_truth/design-intent.diff`.

## 3. The dropped-notification theory: refuted

The reporter's theory is historically real but does not apply here, and it is
refutable from the pre-state tree without a cluster:

- **The scheduler cannot drop a *specific* job's work, by construction.**
  `atc/scheduler/runner.go` `jobsToSchedule` calls
  `s.jobFactory.JobsToSchedule()` — a **full scan** — and carries an explicit
  comment saying why: *"We always perform a full scan rather than targeting
  specific job IDs from the NOTIFY payload because the notification channel can
  overflow (non-blocking send with capacity 1), causing dropped notifications.
  A full scan ensures that any notification—even for a different job—will pick
  up all pending work."* This is the March-2026 fix
  (`de76b735540f919311896569ed5fe576e4643092`, "fix(scheduler): restore polling
  fallback to prevent dropped notifications") still in place, plus the
  component runner's polling interval. A dropped NOTIFY delays scheduling by at
  most one interval; it cannot suppress one job indefinitely while other
  pipelines schedule normally.
- **More decisively, the scheduler is downstream of the missing artifact.**
  The scheduler acts on resource *versions*. No version exists for this commit,
  so there is nothing for the scheduler to have dropped. Debugging the
  scheduler is the wrong layer.

## 4. The silent logs are a log-level artifact, not evidence of a stall

`atc/scheduler/runner.go` emits its lifecycle lines at **Debug**:
`sLog.Debug("start")` / `defer sLog.Debug("done")` in `Run`, and
`logger.Debug("schedule")` / `logger.Debug("could-not-find-job-to-reload")` in
`scheduleJob`. The only non-Debug output on the happy path is via
`metric.SchedulingJobDuration{...}.Emit`; everything else above Debug is
`logger.Error(...)` on failure. At `--log-level=info` a *perfectly healthy*
scheduler is therefore silent. "Nothing in the logs" is the expected steady
state, not a symptom. To see scheduling activity the operator must run web at
`--log-level=debug`.

## 5. What the reporter should do

- Nothing, in code. This is the designed behavior.
- If they want the commit to deploy, push it together with (or follow it with)
  a change outside the ignored set — `ci/**` and `deploy/**` deliberately still
  trigger — or trigger `build-and-vet` manually (`fly trigger-job`).
- Deliverable shape (per the task's "How to send it back"): a `DECISION.md` at
  the repository root carrying the diagnosis, the refutation and an explicit
  "no change needed", with **no tracked file modified**. `DECISION.md` itself
  is the answer, not a change.
- Verify locally, without a cluster: `git show --stat 644184e3f0` shows the one
  `docs/` file; match it against the `ignore_paths` globs. On a cluster the
  confirmation is `fly check-resource -r <pipeline>/repo` returning success with
  no new version, and `fly resource-versions` showing the newest version still
  pinned to the last non-docs commit.

## 6. What a correct submission must NOT do

- **Must not** propose removing, narrowing away, or "fixing" `ignore_paths`, or
  adding an exception so docs pushes trigger. That reintroduces the documented
  loop-friction failure (build 525330: implement run twice, one agent run
  burned per restart).
- **Must not** name a scheduler / notification-bus / `JobsToSchedule` defect as
  the cause, and must not patch `atc/scheduler/*`, `atc/db/notifications_bus.go`
  or the component runner.
- **Must not** raise the resource-checking interval, add a webhook, or
  otherwise change check cadence — the check cadence is not the problem.
