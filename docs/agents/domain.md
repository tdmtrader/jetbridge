# Domain Docs

How the engineering skills consume this repo's domain documentation.

## Before exploring, read these

- **`CONTEXT-MAP.md`** at the repo root. It indexes one glossary per bounded
  context and lists the term collisions between them. Read the map, then every
  `CONTEXT.md` relevant to the topic:
  - `atc/CONTEXT.md` (Core), `atc/agent/CONTEXT.md` (Agentic),
    `atc/worker/jetbridge/CONTEXT.md` (JetBridge runtime), `hangar/CONTEXT.md`
    (Hangar).
- **`docs/architecture/*-model.md`**: relationships, lifecycles and
  store-enforced invariants, one file per context. Glossaries are terms only.
- **`docs/adr/`**: system-wide decisions, numbered. There are no per-context
  ADR directories; every ADR lives here.

If any of these files are missing on the branch you are on, proceed silently.
`/domain-modeling` creates them lazily. Do not scaffold empty ones.

## Layout (multi-context)

```
/
├── CONTEXT-MAP.md
├── docs/adr/
├── docs/architecture/<context>-model.md
├── atc/CONTEXT.md
├── atc/agent/CONTEXT.md
├── atc/worker/jetbridge/CONTEXT.md
└── hangar/CONTEXT.md
```

## Use the glossary's vocabulary

Name a concept the way its context's glossary does, in issue titles, test
names, hypotheses and proposals. Standing rulings: a build is never a "run";
receipt, daemon, lease and hold are always qualified; Hangar's capability is a
warrant, never a grant; `hangar/` and `atc/hangaroutput` may not use core's
product words (a test scans for them).

A concept missing from every glossary is a signal: either you are inventing
language the project does not use, or there is a real gap for
`/domain-modeling`. Glossary claims about enforcement must be checked against
the test they cite; the first drafts had ~30 contradictions with code.

## Flag ADR conflicts

If your output contradicts an ADR, say so explicitly instead of overriding it:

> _Contradicts ADR-0002 (durable tier fails open, Hangar fails closed), but worth reopening because…_

## Hearth decisions vs ADRs

A hearth **decision** artifact records a track-level trade-off; an ADR records
a standing architectural rule the code must keep honouring. Something can be
both. When a track's decision should outlive the track, write the ADR in the
same commit.
