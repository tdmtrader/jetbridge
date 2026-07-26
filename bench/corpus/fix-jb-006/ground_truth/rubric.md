# Rubric — fix-jb-006 (negative `run_retention` values)

Behavioural checklist. Score intent and behaviour, not diff similarity. The
reference change (`reference.diff`) is one correct implementation, not the only
one.

## Must (each is pass/fail)

1. **Rejects negative `keep_last`.** A config with `template: true` and
   `run_retention: {keep_last: -1}` (any negative value) produces a
   config-validation error naming `run_retention.keep_last`.
2. **Rejects negative `ttl_days`.** Same for `run_retention: {ttl_days: -7}`,
   naming `run_retention.ttl_days`.
3. **Independent checks.** A config with both fields negative reports *both*
   errors, not just the first. (A short-circuit that stops at the first bad
   field fails this.)
4. **Rejection happens in config validation**, on the path shared by
   `fly set-pipeline`, `fly validate-pipeline` and the config-save endpoint —
   i.e. inside `atc/configvalidate` (or something it calls), not in the
   archival SQL, not in the DB layer, and not in a fly-only client-side check.
5. **No false positives.** All of these remain valid:
   `keep_last: 0`; `ttl_days: 0`; only one of the two keys set; `run_retention`
   absent entirely; ordinary positive values (e.g. the existing
   "accepts a valid template with params and retention" spec with
   `KeepLast: 5`).
6. **Existing template-only rule intact.** `run_retention` on a non-template
   pipeline still produces the
   `run_retention is only allowed on template pipelines` error, and a config
   that is both non-template *and* negative can surface both problems (the new
   check must not be nested inside the `!c.Template` branch).
7. **No public signature changes.** `configvalidate.Validate(atc.Config)
   ([]atc.ConfigWarning, []string)` keeps its signature; `atc.RunRetentionConfig`
   keeps its field names, types and `omitempty` JSON tags. No new exported
   symbols are required.
8. **Test coverage added** in `atc/configvalidate` (Ginkgo, same suite) covering
   at least the negative-`keep_last` and negative-`ttl_days` cases. The suite
   must still run without PostgreSQL.

## Must not

- Change `RunRetentionConfig` field types (e.g. to `uint`, or to pointers) —
  that silently changes the wire format and the `omitempty` behaviour the
  archival query's `run_retention ? 'keep_last'` JSONB presence test depends on.
- Clamp, normalise or rewrite negative values instead of rejecting them.
- Touch `atc/db/pipeline_run_factory.go`'s archival query or any other runtime
  consumer.
- Add validation for unrelated config keys, refactor `validateParamsSchema`, or
  otherwise expand scope beyond the two range checks and their spec.
- Introduce a PostgreSQL dependency into the `atc/configvalidate` suite.

## Judgement notes

- **Score must-8 from the submitted diff, never from the grading run.** The
  mechanical gate installs its oracle as a *new* file
  (`atc/configvalidate/zz_bench_fix_jb_006_test.go`, from
  `ground_truth/withheld_tests/`) precisely so the agent's own spec in
  `validate_test.go` is neither clobbered nor mistaken for the oracle. If a
  grading run is ever seen patching or overwriting `validate_test.go`, the run
  is invalid for must-8 — re-grade it from the diff.
- The graded spec is namespaced (`bench-graded: rejects negative run_retention
  values`). An agent-authored spec with a similar name is not selected by the
  focus regex and never contributes to the mechanical result.
- Exact error wording is *not* required for a judge pass; the mechanical
  fail-to-pass spec does string-match the reference wording, so a semantically
  correct fix with different phrasing can fail mechanically while passing this
  rubric. Record both outcomes when they disagree.
- This is a declared **calibration anchor**. Judge it honestly, but do not let a
  pass stand as capability evidence: the task states the expected error strings
  and the validation layer outright, so musts 1-4 are largely transcription.
  Credit reasoning that is *visible* — e.g. the agent independently checking the
  archival query's behaviour, or the interaction with the template-only rule —
  rather than treating a green gate as understanding.
- A fix that additionally validates something sensible and adjacent (e.g.
  rejecting a negative value in some other retention-shaped field that exists at
  the cut) is not automatically a scope violation, but note it — the reference
  did not.
