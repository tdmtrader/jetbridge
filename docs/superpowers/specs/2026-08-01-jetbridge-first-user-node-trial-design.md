# Jetbridge First-User Node Trial Design

## Goal

Use Jetbridge as its first reusable-node author: import, run, inspect, iterate,
and release two evidence-backed atomic nodes on the authenticated `home` target.
The trial must produce useful node packages and a candid record of platform and
guide findings, not merely exercise unit-level compiler APIs.

## Scope

The trial covers two agent nodes:

- `code-review`: two immutable `repository/v1` inputs named `before` and
  `after`, producing one `review/v1` output named `review`.
- `log-diagnosis`: one required `log-bundle/v1` input named `logs`, one optional
  `deployment-snapshot/v1` input named `deployment`, and one `diagnosis/v1`
  output named `diagnosis`.

These contracts match the compiled snapshot registry and the existing schema-3
workflow seeds. Sequencing, workflow composition, publishers, human waits, new
snapshot types, and platform implementation changes are outside this trial.

## First-User Method

The existing `agent/workflow/seeds/code-review-node-v1` package is the first
artifact attempted exactly as shipped. Its import result is evidence: if it
fails, the failure is recorded before the sample is repaired. The repaired
code-review package and a new log-diagnosis package will live with the reusable
node seeds so future users have complete examples.

Each node goes through the same bounded loop:

1. Compile the complete source directory locally through the production node
   compiler tests.
2. Import the source directory with `fly -t home agent nodes import`.
3. Inspect the immutable imported version with `fly agent nodes show`.
4. Materialize a neutral benchmark fixture and create exact typed snapshots
   with `fly agent snapshots create`.
5. Run the unreleased exact node version with a unique idempotency key.
6. Inspect the durable run, logs, output snapshot, and typed record.
7. Compare behavior with the benchmark ground truth and make at most one
   evidence-driven behavioral revision per node.
8. Import the revision as a new immutable version and repeat the focused run.
9. Release only a version that runs successfully and produces a valid, useful
   typed record. Do not compose a workflow.

Live state created by the trial is intentionally durable and auditable. Node
versions and snapshots are not deleted. Failed versions remain unreleased;
successful final versions may be released as `compatible` first releases.

## Node Packages

Each package contains:

```text
<node>/
├── node.yaml
├── prompts/
│   └── <task>.md
└── skills/
    └── <method>/
        └── SKILL.md
```

The prompt owns the exact typed-output contract, environment variables,
subject ordering, and hermeticity constraints. The skill owns the reasoning
method: evidence collection, prioritization, falsification, and reporting. This
separation makes behavioral iteration observable: output-contract failures are
prompt problems; weak analysis is a method problem.

Neither node declares a capability sidecar initially. Repository and log
inspection use the trusted agent execution harness and mounted immutable
inputs. This avoids the shipped sample's placeholder `registry.example` image
becoming an artificial live-run failure unrelated to node behavior.

The code-review node keeps `MINIMUM_SEVERITY` with default `medium`. The skill
requires reviewing the actual base-to-candidate change, checking callers and
tests around risky edits, suppressing speculative findings, and emitting a
clear no-findings disposition when appropriate.

The log-diagnosis node has no caller parameter in its first version. Its skill
requires a timeline, symptom clustering, evidence/hypothesis separation,
alternative-hypothesis falsification, confidence calibration, and bounded next
actions that never contact live systems.

## Fixtures and Evaluation

Code review uses a corpus case whose declared signature exactly matches the
node contract, starting with `review-jb-003`. The before and after repositories
are materialized from the case's exact refs into neutral temporary directories
without exposing `case.yaml`, `notes.md`, or `ground_truth`. The run is compared
afterward against `expected_findings.yaml` and the rubric. Project memory and
this conversation are not mounted into the live node, preserving the corpus's
exposure boundary.

Log diagnosis starts from a compatible RCA case and transforms only its exposed
evidence into the documented `log-bundle/v1` file layout. Harness-side case
metadata and ground truth remain absent. If none of the five RCA fixtures can
be represented without inventing evidence, a small synthetic log bundle based
on their exposed evidence is used for platform execution, while qualitative
evaluation remains explicitly non-benchmark.

Iteration is driven by concrete discrepancies: invalid output, missed expected
finding or hypothesis, unsupported claim, poor prioritization, or an avoidable
operator action. Prompt wording is not changed merely for style.

## Error Handling and Stop Conditions

Authoring and import failures are recorded verbatim enough to be actionable,
then addressed in the node package when possible. A failed live run is inspected
through node-run details and ordinary Concourse build logs before deciding
whether the issue belongs to the node, fixture, credentials, runtime, or guide.

The trial continues around non-blocking failures. It stops only if the target
cannot execute agent nodes at all, required credentials are unavailable after
authentication, or no valid typed input can be created. Those conditions are
reported with the last successful layer of evidence.

## Verification

Verification is proportional and bounded:

- focused node compiler and seed tests;
- focused Fly reusable-node command tests when source packaging changes;
- exact live import/show/run/show-run commands for both nodes;
- typed output download and record inspection for successful runs;
- one local diff-hygiene pass;
- a blocking-only review of the authored node packages and findings document.

No repository-wide acceptance suite, Kubernetes suite, or unrelated semantic-
rebase review is required for this node-authoring trial.

## Findings Record

`JETBRIDGE_FIRST_USER_FINDINGS.md` at the repository root is updated throughout
the trial. It contains a chronological log and consolidated sections for what
worked, pain points, documentation gaps, effective node-authoring patterns,
benchmark/corpus observations, and follow-up opportunities. It distinguishes
observed behavior from inference and does not opportunistically modify platform
code to hide first-user friction.
