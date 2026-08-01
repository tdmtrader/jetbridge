---
name: review
description: Inspect an immutable repository delta and produce evidence-backed findings.
---

# Review

Review the change, not the aspirations around it.

1. Inventory the changed paths and identify the behavior each change can alter.
2. Trace each risk from a triggering state or input to an externally visible
   failure path.
3. Read callers, invariants, and nearby tests before declaring a defect.
4. Compare `before` when attribution matters. Separate introduced, aggravated,
   and pre-existing issues.
5. Cite the smallest useful file and line range in `after`.
6. Try to falsify every candidate finding. Drop style complaints, speculation,
   and unverified assumptions as false positives.
7. Rank surviving findings by user impact and likelihood, then apply
   `MINIMUM_SEVERITY`.

Prefer a short, high-confidence review. A clean review is valid when no finding
survives falsification. Do not hide a blocker because its fix is non-local, and
do not pad the result with low-value feedback.

## Exact output shape

When the managed output builder is unavailable, write `record.json` directly
with this exact shape. Replace every angle-bracket value with the literal value
from the platform authority block at the top of the initial prompt. Do not add,
rename, or move fields.

```json
{
  "record_version": "1.0.0",
  "type": "<AGENT_OUTPUT_REVIEW_RECORD_TYPE>",
  "schema": "<AGENT_OUTPUT_REVIEW_RECORD_SCHEMA>",
  "subjects": [
    {
      "id": "after",
      "role": "primary",
      "input": "after",
      "type": "<AGENT_INPUT_AFTER_SNAPSHOT_TYPE>",
      "digest": "<AGENT_INPUT_AFTER_SNAPSHOT_DIGEST>"
    },
    {
      "id": "before",
      "role": "context",
      "input": "before",
      "type": "<AGENT_INPUT_BEFORE_SNAPSHOT_TYPE>",
      "digest": "<AGENT_INPUT_BEFORE_SNAPSHOT_DIGEST>"
    }
  ],
  "body": {
    "conclusion": "changes-required",
    "summary": "Evidence-bounded review summary.",
    "findings": [
      {
        "id": "finding-1",
        "severity": "high",
        "blocking": true,
        "category": "correctness",
        "title": "Short finding title",
        "description": "Trigger, behavior, and impact.",
        "evidence": [
          {
            "subject": "after",
            "locator": {
              "kind": "file-lines",
              "path": "relative/path.go",
              "start": 1,
              "end": 2
            }
          }
        ],
        "recommendation": "Smallest safe correction."
      }
    ]
  }
}
```

Allowed conclusions are `accept`, `changes-required`, and `inconclusive`.
An accepted clean review uses an empty `findings` array; changes-required needs
at least one blocking finding. Every finding id is lexicographically sorted.
