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
  model: claude-sonnet
  skills: [review]
  capabilities: [dev-mcp]
```

The node compiler captures the prompt, selected skill tree, model, logical
contract, parameter defaults, and resolved capability image/command/ports.
Capability images must be digest-pinned. The common agent-runner image is not a
node override: ATC supplies that trusted execution harness at admission.
Secrets are never stored in a node version.

Nodes cannot contain sequencing, parallelism, hooks, retries, human waits,
resource acquisition, or nested nodes. Put those relationships in the
workflow, where they remain visible.

## Import and inspect

Import validates the complete directory and allocates the next integer version.
Importing byte-identical resolved content returns the existing version:

```sh
fly -t TARGET agent nodes import ./code-review
fly -t TARGET agent nodes list
fly -t TARGET agent nodes show code-review 1 --json
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
run, release, deprecate, restore, and upgrade.

Use the exact HTTP endpoints when automating the same flow:

```text
GET  /api/v1/agent/nodes
POST /api/v1/agent/nodes/:name/versions
PUT  /api/v1/agent/nodes/:name/versions/:version/release
POST /api/v1/agent/nodes/:name/versions/:version/runs
GET  /api/v1/agent/nodes/:name/versions/:version/consumers
POST /api/v1/agent/nodes/:name/versions/:version/upgrades
```

Node import accepts only a complete JSON manifest object,
`{"files":{"node.yaml":"...","prompts/review.md":"...", ...}}`; it does not
accept a raw-YAML convenience body. Direct-run and upgrade bodies are likewise
strict and reject unknown fields.

Node and workflow names are kind-scoped. A node run cannot reuse or address a
workflow run through a shared name, run ID, or idempotency key.
