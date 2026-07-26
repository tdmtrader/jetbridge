# `run_retention` accepts negative values and silently eats every run

**Type:** bug
**Component:** atc — pipeline config / template pipeline runs
**Reported:** 2026-07-11

## Context

Template pipelines (`template: true`) carry a top-level `run_retention` key:

```yaml
template: true
run_retention:
  keep_last: 10
  ttl_days: 7
```

It is mirrored onto `pipelines.run_retention` when the config is saved and read
back by the run-lifecycle component's archival sweep, which archives completed
runs beyond `keep_last` and completed runs older than `ttl_days`.

## Symptom

An operator fat-fingered a retention value and lost their run history.

```yaml
template: true
run_retention:
  keep_last: -1
```

`fly set-pipeline` accepted this without a warning or an error. So did
`run_retention: {ttl_days: -5}`. The pipeline saved cleanly and looked normal.

The damage showed up later, from the archival sweep rather than from the
command that introduced the bad value:

- a negative `keep_last` makes the "beyond the last K runs" predicate true for
  every completed run, so the whole run history of that template is archived on
  the next sweep;
- a negative `ttl_days` pushes the age cutoff into the *future*, so every
  completed run immediately counts as expired.

Nothing surfaces the cause. From the operator's side the runs simply disappear
some time after a successful `set-pipeline`, with no message tying the two
events together.

## Expected behaviour

A negative `keep_last` or `ttl_days` is a configuration error and should be
reported when the pipeline config is validated — i.e. `fly set-pipeline`,
`fly validate-pipeline`, and the config PUT endpoint all reject it up front —
instead of being accepted and misbehaving later at archival time.

The message should name the offending key and follow the style of the
neighbouring config-validation errors, e.g.:

```
run_retention.keep_last must not be negative
run_retention.ttl_days must not be negative
```

## Constraints

- Zero, positive and absent values must keep working exactly as they do today:
  `keep_last: 0`, `ttl_days: 0`, one key set and the other omitted, and no
  `run_retention` block at all are all still valid.
- The existing rule that `run_retention` is only allowed on `template: true`
  pipelines must not change, and a config that violates both rules should be
  able to report both errors.
- Both keys must be checked independently — a config that sets both to negative
  values reports both errors.
- No public signature changes to `configvalidate.Validate` or to
  `atc.RunRetentionConfig` (including its JSON tags — `keep_last`/`ttl_days`
  stay `omitempty`).
- The fix belongs at config-validation time. Repairing or clamping an
  already-saved bad config in the archival query is explicitly out of scope.
- Add a spec to the existing package suite covering the new behaviour. The
  affected suite runs without PostgreSQL; keep it that way.
