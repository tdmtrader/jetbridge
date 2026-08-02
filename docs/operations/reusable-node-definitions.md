# Reusable node definitions

Reusable nodes are immutable, versioned, single-step execution definitions.
They keep the implementation of a task, agent, validator, or publisher
together while allowing each workflow to map its own artifact names into and
out of the node.

A workflow always references an exact released integer version. Importing or
releasing a newer node never changes a workflow, a live promotion, or a
historical run.

## Author a node package

A node is a directory whose entry point is `node.yaml`. Referenced prompts,
skills, and other assets must be inside that directory:

```text
code-review/
├── node.yaml
├── prompts/
│   └── review.md
└── skills/
    └── review/
        └── SKILL.md
```

The schema-1 source declares logical ports, supported string parameters, any
platform capability sidecars, and exactly one leaf:

```yaml
schema_version: 1
name: code-review
description: Review one repository state relative to another.
inputs:
  - {name: before, type: repository/v1}
  - {name: after, type: repository/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MINIMUM_SEVERITY, default: medium}
capabilities:
  dev-mcp:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/dev-mcp@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      command: [/usr/local/bin/dev-mcp]
      ports: [{containerPort: 8080, protocol: TCP}]
step:
  agent: review
  function_id: review
  prompt_file: prompts/review.md
  model: sonnet
  skills: [review]
  capabilities: [dev-mcp]
```

`model` is passed to the agent runtime verbatim and is frozen into the node
version, so an invalid value is permanent for that version and fails only
once a pod is running and calls the model API — nothing at import, release,
or run-create time validates it. Prefer the runtime CLI's short aliases
(`sonnet`, `opus`, `haiku`) over a full model identifier. `claude-sonnet` is
not a valid value; it returns `API Error: 404 model: claude-sonnet` from the
running pod, and by then the run has already spent a pod launch.

`budget_slice_usd` requires an agent runtime that supports a per-run budget
cap — the runner passes it through as `--max-budget-usd`. With a non-zero
deployment daily budget cap enabled, every agent leaf needs a positive
`budget_slice_usd`, so the two settings must be rolled out together:
enabling the cap without giving every leaf a slice makes ordinary budget
reservation fail closed — and a slice is independently the step's hard cap
regardless of whether any deployment cap is set at all: the runner passes
`--max-budget-usd` for any positive `budget_slice_usd`, cap or no cap. As
currently pinned, the `@anthropic-ai/claude-code@2.0.1` CLI in
`deploy/agent-runner/Dockerfile` does not implement `--max-budget-usd` at
all, so a step that declares `budget_slice_usd` fails against that pin today
— with or without a deployment cap. This is a property of the pinned CLI
release, not a permanent platform limitation — moving the pin to a CLI
version that supports the flag fixes it without any node change.

Node parameters are supplied to the step as **environment variables**, not
interpolated into the prompt text: `CompiledNodeDefinition.Instantiate`
writes each resolved parameter into the step's environment
(`agent/workflow/node_definition.go`). A prompt that writes
`${MINIMUM_SEVERITY}` gets that literal string, not the parameter's value —
write "read the `MINIMUM_SEVERITY` environment variable" instead.

The node compiler captures the prompt, selected skill tree, model, logical
contract, parameter defaults, and resolved capability image/command/ports.
Capability images must be digest-pinned. The common agent-runner image is not a
node override: ATC supplies that trusted execution harness at admission.
Secrets are never stored in a node version.

Nodes cannot contain sequencing, parallelism, hooks, retries, human waits,
resource acquisition, or nested nodes. Put those relationships in the
workflow, where they remain visible.

## Writing the step prompt

The runtime prepends its own instructions to the node's prompt, describing
whichever output mechanism is active for this step: the managed
output-builder MCP tools (`describe_output`, `write_output`,
`validate_output`) when the builder is enabled, or the resolved
record-authority environment variables (`AGENT_INPUT_<PORT>_SNAPSHOT_TYPE`,
`AGENT_OUTPUT_<PORT>_RECORD_TYPE`, and similar) when it is not. **Do not
hardcode either mechanism in the node prompt.** A prompt that names
environment variables the builder path does not set makes the agent write
empty values into its record, which fails sealing only after the step has
spent its whole budget. Write "use the platform-provided output mechanism
described above" and spend the prompt on what a good result looks like, not
on how to deliver it.

Two rules earn their place in almost any exploring agent node's prompt:

- **Instruct an early provisional write, then refinement.** Record writing
  is idempotent — the last successful write before the step ends wins — so
  an agent that writes its best current answer early and keeps refining it
  cannot lose everything to the turn cap. Without this instruction, an
  exploring agent reliably spends its whole turn budget and produces
  nothing: measured on a diagnosis node, this single instruction turned an
  80-turn, zero-output run into a correct, well-anchored diagnosis.
- **State the record contract's own vocabulary.** For a review-shaped node
  (`review/v1`), the prompt should say so explicitly: severities are
  `observation`, `low`, `medium`, `high`, `critical`; `high` and `critical`
  findings must be `blocking: true`, `observation` findings must not be; an
  `accept` conclusion cannot carry a blocking finding, and
  `changes-required` requires at least one; every entity list (findings,
  hypotheses, actions, and so on) must be sorted by `id`. A prompt that
  invents its own vocabulary, or leaves it unstated, produces records that
  read well and fail validation.

## Import and inspect

Import validates the complete directory and allocates the next integer
version:

```sh
fly -t TARGET agent nodes import ./code-review
fly -t TARGET agent nodes list
fly -t TARGET agent nodes show code-review 1 --json
```

Version numbers are allocated **per node name**, not team-global: import
takes a per-name Postgres advisory lock and assigns the next version as
`COALESCE(MAX(version),0)+1` scoped to that name
(`atc/db/agent_nodes_factory.go`). A script cannot safely predict "my next
version" as `N+1` even with no other writers, because import is also
**content-hash idempotent** — the hash is over the byte-identical *source*
directory, taken before compilation, so re-importing an unchanged source
directory returns the existing version without allocating a new one. Use
`--json` on `import` and `release` and read back the version each call
actually acted on:

```sh
VERSION=$(fly -t TARGET agent nodes import ./code-review --json | jq -r .version)
fly -t TARGET agent nodes release code-review "$VERSION" --compatibility=compatible --json | jq -r .version
```

An imported version is immutable but is not yet eligible for workflow
composition. It can be inspected and run directly before release.

## Test an exact version directly

Bind every required logical input to an exact snapshot ID and supply only
declared parameters:

```sh
fly -t TARGET agent nodes run code-review 1 \
  --input before=101 \
  --input after=102 \
  --param MINIMUM_SEVERITY=high \
  --idempotency-key=code-review-v1-fixture
```

The run uses the ordinary durable workflow-run machinery. It records the node
definition ID, exact version and content hash, typed snapshot bindings,
rendered configuration, logs, metrics, outputs, and provenance. A repeated
idempotency key must describe the same effective version, inputs, and
parameters.

Direct testing is available for unreleased, released, superseded, and
deprecated versions:

```sh
fly -t TARGET agent nodes runs code-review 1 --status=succeeded
fly -t TARGET agent nodes show-run code-review RUN-ID --json
```

For a run that ended `failed`, `errored`, or `aborted`, the non-JSON form of
`show-run` also prints a `failure:` line with the terminal error (when one
was captured) and a `full log:` line with the exact `fly watch` command for
the underlying build, so a doomed run's cause no longer requires fishing
`planned_build_id` out of `--json` by hand:

```sh
fly -t TARGET agent nodes show-run code-review RUN-ID
# failure: API Error: 404 model: claude-sonnet
# full log: fly -t TARGET watch -b 4821
```

To stop an in-flight run without dropping to `fly abort-build`, cancel it
directly:

```sh
fly -t TARGET agent nodes cancel-run code-review RUN-ID
```

## Release

Release is an explicit lifecycle action and does not update a workflow:

```sh
fly -t TARGET agent nodes release code-review 1 --compatibility=compatible
```

The first release establishes the lineage. Every later release names the latest
previously released version as its predecessor and declares one of:

- `compatible`: every existing mapping remains valid. Existing ports retain
  their type and required status, new inputs are optional, existing outputs
  remain, new outputs need not be mapped, existing parameters remain accepted,
  and new parameters have defaults. The server rejects a false compatible
  declaration.
- `breaking`: the release is deliberate, but consumers require explicit
  recomposition. The upgrade action reports added, removed, and changed input,
  output, and parameter obligations and writes no partial revision.

Behavioral changes such as a new prompt or model can be structurally compatible.
Adoption is still explicit.

Testing is not a release prerequisite and remains available after release.
Deprecation discourages new use without withdrawing the exact version:

```sh
fly -t TARGET agent nodes deprecate code-review 1
fly -t TARGET agent nodes restore code-review 1
```

## Compose a workflow

A workflow references a released version and owns all mappings and control
flow:

```yaml
schema_version: 3
name: review-change
signature_version: 1
inputs:
  - {name: base, type: repository/v1}
  - {name: candidate, type: repository/v1}
outputs:
  - {name: review, type: review/v1, from: review}
plan:
  - node: review-change
    uses: code-review@1
    input_mapping: {before: base, after: candidate}
    output_mapping: {review: review}
    params: {MINIMUM_SEVERITY: high}
```

`node` is the stable workflow-local step identity. `uses` accepts only
`NAME@EXACT-INTEGER`; there is no `latest` or release channel. Compilation
expands the reference into one visible agent/task/publisher leaf and records an
exact binding keyed by the immutable workflow definition ID. The authored
manifest keeps the `uses:` reference for later inspection and upgrades.

Import creates an immutable workflow revision. Promotion remains explicit:

```sh
fly -t TARGET agent workflows import ./review-change
fly -t TARGET agent workflows set-live review-change 1
```

Do not use `--set-live` when an imported revision should be inspected before it
becomes the default for new runs.

## Discover consumers and apply an upgrade

After releasing a successor, inspect exact consumers:

```sh
fly -t TARGET agent nodes consumers code-review 1 --json
```

Apply the successor only to selected workflows:

```sh
fly -t TARGET agent nodes upgrade code-review 2 \
  --workflow=review-change \
  --workflow=another-review-flow
```

For a compatible successor, each successful selection creates one validated,
immutable, unpromoted workflow revision. A failure in one selected workflow
does not roll back successful revisions for other workflows. Repeating the same
upgrade is idempotent and reports the existing revision as unchanged.

The generated revision preserves the consumer's authored boundary. It does not
add mappings for newly optional inputs or newly available outputs, so those
ports remain outside that workflow's artifact namespace. A newly defaulted
parameter is applied by the node leaf without adding a workflow binding.

One upgrade request may return at most 4 MiB of encoded result data. If a large
selection or breaking contract diff exceeds that bound, the API returns HTTP
422:

```json
{"error":"response_limit_exceeded","message":"node upgrade result exceeds the 4 MiB response limit; select fewer workflows"}
```

Split the `--workflow` selections into smaller commands and retry each batch.
Batches are independently validated and idempotent, and every created revision
remains unpromoted. An oversized breaking request is rejected during preflight,
before live-workflow reads, binding reads, or revision imports.

Inspect each generated revision and promote only the ones intended to become
live:

```sh
fly -t TARGET agent workflows show review-change 2 --json
fly -t TARGET agent workflows set-live review-change 2
```

The upgrade action never promotes. Until the final command, new workflow runs
continue to use the previous live revision and its older exact node version.
Existing workflow revisions and historical runs are never rewritten.

For a breaking successor, edit the complete workflow source to satisfy the
reported obligations, import it as another immutable revision, validate it,
and promote it separately.

## Authorization and audit boundary

Reusable-node routes use the main-team agent authorization boundary. Viewers
may inspect definitions, versions, runs, and consumers. Members may import,
run, cancel a run, release, deprecate, restore, and upgrade.

Use the exact HTTP endpoints when automating the same flow:

```text
GET  /api/v1/agent/nodes
POST /api/v1/agent/nodes/:name/versions
PUT  /api/v1/agent/nodes/:name/versions/:version/release
POST /api/v1/agent/nodes/:name/versions/:version/runs
POST /api/v1/agent/nodes/:name/runs/:run-id/cancel
GET  /api/v1/agent/nodes/:name/versions/:version/consumers
POST /api/v1/agent/nodes/:name/versions/:version/upgrades
```

Node import accepts only a complete JSON manifest object,
`{"files":{"node.yaml":"...","prompts/review.md":"...", ...}}`; it does not
accept a raw-YAML convenience body. Direct-run and upgrade bodies are likewise
strict and reject unknown fields.

Node and workflow names are kind-scoped. A node run cannot reuse or address a
workflow run through a shared name, run ID, or idempotency key.
