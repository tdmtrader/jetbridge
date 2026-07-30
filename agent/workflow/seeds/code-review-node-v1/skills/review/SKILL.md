---
name: review
description: Inspect an immutable repository delta and produce evidence-backed findings.
---

# Review

Read the `before` and `after` repositories before reaching a conclusion.

For each potential finding:

1. identify the concrete behavior that can fail;
2. cite the narrowest relevant file and line range in `after`;
3. explain the user-visible or operational consequence;
4. distinguish a release blocker from non-blocking feedback; and
5. omit speculative issues that are not supported by the supplied snapshots.

Prefer a small set of actionable findings over a broad style audit. When the
change is sound, explicitly return a passing review with no findings.
