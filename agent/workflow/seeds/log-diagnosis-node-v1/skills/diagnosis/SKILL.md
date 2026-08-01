---
name: diagnosis
description: Diagnose an immutable log bundle with ranked, falsifiable hypotheses.
---

# Diagnosis

Treat the captured bundle as evidence, not as a transcript to summarize.

1. Inventory files, time ranges, services, request or correlation identifiers,
   and explicit gaps in the capture.
2. Build a compact event timeline. Normalize timestamps before claiming order
   and distinguish one causal chain from repeated copies of the same symptom.
3. Separate the earliest observed failure from downstream retries, timeouts,
   circuit-breaker activity, and cleanup noise.
4. Generate competing hypotheses that explain the earliest failure. For each,
   state what evidence should exist if it is true and actively look for evidence
   that would falsify it.
5. Anchor evidence and counterevidence to the smallest useful log line range.
   Do not convert assumptions or absent data into positive evidence.
6. Rank surviving hypotheses by explanatory coverage and contradiction cost.
   Confidence is a calibrated 0..1 estimate, not a substitute for evidence.
7. Choose `identified` only when the rank-1 cause is directly supported;
   otherwise use `suspected` or `inconclusive` honestly.
8. Recommend reversible, bounded actions that discriminate among hypotheses or
   mitigate the supported cause. Never claim an action was performed.

Prefer a short diagnosis with explicit uncertainty over a long single-story
narrative. Preserve relevant counterevidence even when one hypothesis leads.

## Exact output shape

When the managed output builder is unavailable, write `record.json` directly
with this exact shape. Replace every angle-bracket value with the literal value
from the platform authority block at the top of the initial prompt. Do not add,
rename, or move fields.

```json
{
  "record_version": "1.0.0",
  "type": "<AGENT_OUTPUT_DIAGNOSIS_RECORD_TYPE>",
  "schema": "<AGENT_OUTPUT_DIAGNOSIS_RECORD_SCHEMA>",
  "subjects": [
    {
      "id": "logs",
      "role": "primary",
      "input": "logs",
      "type": "<AGENT_INPUT_LOGS_SNAPSHOT_TYPE>",
      "digest": "<AGENT_INPUT_LOGS_SNAPSHOT_DIGEST>"
    }
  ],
  "body": {
    "summary": "Evidence-bounded summary.",
    "conclusion": "suspected",
    "hypotheses": [
      {
        "id": "h1",
        "rank": 1,
        "statement": "A falsifiable causal statement.",
        "confidence": {
          "value": 0.7,
          "scale": "unit-interval",
          "direction": "higher-is-better"
        },
        "evidence": [
          {
            "subject": "logs",
            "locator": {
              "kind": "log-lines",
              "path": "relative.log",
              "start": 1,
              "end": 2
            }
          }
        ],
        "counterevidence": []
      }
    ],
    "actions": [
      {
        "id": "a1",
        "priority": "next",
        "description": "A bounded discriminating action.",
        "addresses": ["h1"],
        "rationale": "Why this action changes confidence or mitigates risk."
      }
    ]
  }
}
```

If the optional deployment input is present, add its subject with id/input
`deployment`, role `context`, and its exact platform type/digest before `logs`.
Allowed conclusions are `identified`, `suspected`, and `inconclusive`; allowed
action priorities are `immediate`, `next`, and `optional`. Every list of entity
ids is lexicographically sorted, while hypothesis ranks are contiguous from 1.
