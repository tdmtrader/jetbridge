# Root-cause the `resource_config_scope` FK-violation flake — the contradiction is not resolving

**Track:** `resource_config_scope_fk_leak_fix_20260530` (bugfix, active)
**Branch:** `jetbridge`
**Supersedes:** `resource_config_scope_gc_race_20260408`

## Symptom

The `k8s-e2e` pipeline's `k8s-behavioral-tests` job flakes on the
custom-resource-type specs. Two of the last seven nightly runs failed:

- build #100 — `runs a pipeline with custom resource types`
  (`topgun/k8s_behavioral/e2e_scenarios_test.go:468`)
- build #99 — `6.1: single custom type backed by registry-image resolves and works`

In both, `fly check-resource` exits 2 because the check build errors:

```
selected worker: k8s-concourse
save versions: ERROR: insert or update on table "resource_config_versions"
violates foreign key constraint "resource_config_versions_resource_config_scope_id_fkey"
(SQLSTATE 23503)
errored
```

`k8s-integration-tests` (#173) was green in the same chain.

## Where the investigation stands

This is a GC race that a **prior track already fixed**: GC deletes a
`resource_config_scope` mid-check, so the following `INSERT` into
`resource_config_versions` violates its FK. Commits `59c43a31ff` and `9bb8537a6a`
added guards in `atc/exec/check_step.go` that call `db.IsForeignKeyViolation(err)`
and, on a hit, finish the check gracefully instead of erroring the build.

A full day of diagnosis has gone into this and it has produced a contradiction
rather than an answer. Read
[`evidence/diagnostic-record.md`](evidence/diagnostic-record.md) first — it is
the diagnostic record as written, including the hypotheses that were tested and
**refuted**, the reproduction output, and the list of things ruled out. In
summary:

- the detection helper is provably correct against a real Postgres error, both
  raw and wrapped;
- a real-DB regression test drives the actual `SaveVersions` flow with the scope
  deleted underneath it and the guard fires — green;
- both FK surfaces that can propagate to a build are guarded;
- the leading environmental hypothesis that was checked came back **refuted**.

Yet the deployed build still errored on a guarded path.

The record ends on that unresolved contradiction, with a decision to stop doing
static analysis and settle it at runtime: instrument the harness, push, and run
the chain. That has now happened.
[`evidence/instrumented-run.md`](evidence/instrumented-run.md) is what came back.

## What is being asked

1. **Explain the contradiction.** Name the root cause of why the deployed guard
   did not prevent the build from erroring, and say what evidence in the two
   attached documents establishes it. State where the evidence points, whether
   that is inside the code under investigation or outside it; either conclusion
   is acceptable and useful, provided it is argued rather than assumed.
2. **Apply the corresponding fix**, as small as it can be while actually being
   effective end-to-end.
3. **State how the fix will be confirmed** on the next run — specifically, what
   should appear in the output that did not appear this time.

## Constraints

- Do not weaken or remove the existing FK guards, and do not change the
  signature or semantics of `db.IsForeignKeyViolation`.
- Adding a test-side retry wrapper around `fly check-resource` is **not** an
  acceptable primary fix. It is acceptable only as belt-and-suspenders after a
  real fix lands.
- Redesigning GC ordering or the `resource_config_scope` lifecycle is out of
  scope.
- Whatever you conclude, record it in the track's record file
  `forge/tracks/resource_config_scope_fk_leak_fix_20260530/cgx.md` — the
  acceptance criteria require the root cause to be documented there alongside the
  fix.

## Deliverable

A short written diagnosis (root cause, the decisive evidence, the mechanism, the
fix, and how to verify it) plus the change itself. If more than one hypothesis
survives the evidence, rank them and say what single observation would separate
them.

Where to put it: append a new dated section to
`forge/tracks/resource_config_scope_fk_leak_fix_20260530/cgx.md` and summarise
the same conclusion in your reply. If your conclusion is that some part of the
code under investigation needs no change, write that conclusion in the same
place, with the same explicitness a proposed change would get — "no change here,
and here is why" is a result to be recorded, not an omission.
