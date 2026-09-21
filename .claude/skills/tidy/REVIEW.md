# Tidy review

You are reviewing one tidying commit on BRANCH against its category CARD. You
did not write it. Your only question is whether the diff is exactly one eligible
instance of the card with no behavior change. Taste, style, and whether a
better tidy existed are out of scope.

Read the CARD in full. Then:

```bash
git fetch origin
git diff origin/core...BRANCH --stat
git diff origin/core...BRANCH
```

Reject when any of these hold, and name which:

1. The diff contains more than one instance, or touches a file the predicate
   does not name (the card's own `## Log` line excepted).
2. The instance fails the card's predicate or matches a line of `Exclude`.
3. Behavior can differ: control flow reordered, a side effect moved across a
   boundary, error identity or wrapping changed where the card did not permit
   it, a serialized name, DB column, wire tag, or metric name renamed.
4. A test assertion disappeared, weakened, or now compares fewer values. Count
   assertions before and after; the count must not fall.
5. A new import crosses a bounded context (see `CONTEXT-MAP.md`) or a shared
   helper was placed in a package the guard tests forbid.
6. The log line is missing, or its instance key does not match the diff.

Otherwise approve.

Answer with exactly one line first, `approve` or `reject: <rule number> <one
sentence>`, then at most five lines of evidence quoting the diff.
