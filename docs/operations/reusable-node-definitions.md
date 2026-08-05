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
  budget_slice_usd: 5
  skills: [review]
  capabilities: [dev-mcp]
```

The node compiler captures the prompt, selected skill tree, model, logical
contract, parameter defaults, and resolved capability image/command/ports.
Capability images must be digest-pinned. The common agent-runner image is not a
node override: ATC supplies that trusted execution harness at admission.
Secrets are never stored in a node version.

The `registry.example/...@sha256:aaaa...` image above is deliberately
non-runnable sample syntax; replace it with a real digest-pinned image or omit
that capability. For portability, omit `model` by default. Add a model or
broker selector only when the target's deliberately configured broker catalog
requires one.

A positive `budget_slice_usd` depends on the deployment's pinned runner image
having passed its load-bearing CLI smoke gate, including
`--max-budget-usd`. The runner always applies that positive cap. Zero is the
explicit uncapped runner value; it is not a portable spending limit.

Bundled skills are immutable and discoverable in the model session, but the
platform cannot guarantee that the model reads or invokes one. Put
contract-critical record mechanics in the initial authority and managed output
builder, not only in skill text.

Nodes cannot contain sequencing, parallelism, hooks, retries, human waits,
resource acquisition, or nested nodes. Put those relationships in the
workflow, where they remain visible.

## Create or capture exact typed inputs

Create a typed snapshot directly from a local directory and retain the exact
snapshot ID returned by the command:

```sh
BEFORE_JSON="$(fly -t TARGET agent snapshots create \
  --type repository/v1 \
  --from ./fixture/before \
  --json)"
BEFORE_SNAPSHOT_ID="$(printf '%s\n' "$BEFORE_JSON" | jq -er '.id')"
```

For a retained pipeline resource version, pass every exact version field back
to the capture command. Repeat `-v key:value` when the resource version has
multiple fields; never substitute a moving `latest` value:

```sh
CAPTURE_JSON="$(fly -t TARGET agent snapshots capture-resource \
  -p PIPELINE -r RESOURCE -v ref:EXACT-COMMIT \
  --type repository/v1 \
  --json)"
AFTER_SNAPSHOT_ID="$(printf '%s\n' "$CAPTURE_JSON" | jq -er '.snapshot.id')"
```

The command waits for the durable capture by default. Record both
`.snapshot.id` and `.execution.pipeline_run_id`; use the snapshot ID as the
node input.

## Sealing a historical revision

`repository/v1` requires complete, non-shallow history. For a clone that means
**every ref**, so a naive clone of an old commit also ships every descendant of
it — including work you may not intend the consumer to see. A sealed repository
carries everything reachable from its refs; pruning is how you control that.

To seal exactly one revision and nothing after it:

```sh
git clone --no-local <source> pre-state
cd pre-state
git branch -f pre-state <TARGET-SHA>
git checkout pre-state
for b in $(git branch --format='%(refname:short)' | grep -v '^pre-state$'); do
  git branch -D "$b"
done
git remote remove origin
git tag -l | xargs -r git tag -d
git for-each-ref --format='%(refname)' refs/remotes | xargs -r -n1 git update-ref -d
git reflog expire --expire=now --all
git gc --prune=now
```

Then assert the prune worked. This check is the point of the procedure:

```sh
git cat-file -e <A-SHA-THAT-CAME-AFTER>   # MUST fail
```

Only then seal it:

```sh
fly -t TARGET agent snapshots create --type repository/v1 --from ./pre-state --json
```

`git clone` writes only `core.*`, `remote.*` and `branch.*` config keys, all of
which the validator's allowlist accepts; removing the remote leaves a clean
config. A 220 MB repository seals in roughly 40 seconds.

## Writing to inputs

Typed inputs are mounted **writable**, and that is deliberate. A node that
cannot write to its repository input cannot build it, run its tests, or install
anything.

Editing an input cannot affect the sealed snapshot or any other run. The mount
is a per-run copy at `<artifactDaemonHostPath>/steps/<handle>/<subdir>`, keyed
by the step's own container handle, and the artifact daemon materializes it by
byte copy — no hardlinks, so no shared inodes with the cache. (Contrast a `cache`
volume, which is keyed stably by job and step precisely so that it *is* shared.)

**One exception.** An input named as the base subject of a
`repository-change/v1` output is re-read from its mount and re-canonicalized
when the record is written — `repository-change/v1` is the only contract that
reopens input content. If the tree no longer hashes to the digest it was given,
the write fails:

```
output builder: input "repository" canonical digest does not match its authority
```

This is not corruption; the sealed snapshot is untouched and other runs are
unaffected. It means the node broke *its own* output. A node that must both edit
a repository and seal a `repository-change/v1` against it should work in a copy:

```sh
cp -a repository work
chmod -R u+w work
cd work
```

## Import and record the exact version

Import validates the complete directory and allocates the next integer version.
Importing byte-identical resolved content returns the existing version:

```sh
IMPORT_OUTPUT="$(fly -t TARGET agent nodes import ./code-review)"
printf '%s\n' "$IMPORT_OUTPUT"
NODE_VERSION="$(printf '%s\n' "$IMPORT_OUTPUT" | awk '$1 == "imported" && $2 == "code-review" && $3 == "version" {print $4}')"
fly -t TARGET agent nodes list
fly -t TARGET agent nodes show code-review "$NODE_VERSION" --json
```

An imported version is immutable but is not yet eligible for workflow
composition. Do not assume that an import allocated a new integer: retain and
use the exact returned version. It can be inspected and run directly before
release.

## Test an exact version directly

Bind every required logical input to an exact snapshot ID and supply only
declared parameters:

```sh
RUN_JSON="$(fly -t TARGET agent nodes run code-review "$NODE_VERSION" \
  --input before="$BEFORE_SNAPSHOT_ID" \
  --input after="$AFTER_SNAPSHOT_ID" \
  --param MINIMUM_SEVERITY=high \
  --idempotency-key="code-review-v${NODE_VERSION}-fixture" \
  --json)"
RUN_ID="$(printf '%s\n' "$RUN_JSON" | jq -er '.workflow_run_id')"
```

The run uses the ordinary durable workflow-run machinery. It records the node
definition ID, exact version and content hash, typed snapshot bindings,
rendered configuration, logs, metrics, outputs, and provenance. A repeated
idempotency key must describe the same effective version, inputs, and
parameters.

Direct testing is available for unreleased, released, superseded, and
deprecated versions:

```sh
fly -t TARGET agent nodes show-run code-review "$RUN_ID"
DETAIL_JSON="$(fly -t TARGET agent nodes show-run code-review "$RUN_ID" --json)"
BUILD_ID="$(printf '%s\n' "$DETAIL_JSON" | jq -er '.planned_build_id')"
fly -t TARGET watch -b "$BUILD_ID"

while :; do
  DETAIL_JSON="$(fly -t TARGET agent nodes show-run code-review "$RUN_ID" --json)"
  RUN_STATUS="$(printf '%s\n' "$DETAIL_JSON" | jq -er '.status')"
  case "$RUN_STATUS" in
    succeeded|failed|errored|aborted) break ;;
    *) sleep 2 ;;
  esac
done
test "$RUN_STATUS" = succeeded
OUTPUT_ID="$(printf '%s\n' "$DETAIL_JSON" | jq -er '.outputs[] | select(.port == "review") | .snapshot.id')"
fly -t TARGET agent snapshots show "$OUTPUT_ID" --json
fly -t TARGET agent snapshots download "$OUTPUT_ID" --to ./review-output.tar
tar -xOf ./review-output.tar record.json | jq .
```

Plain `show-run` prints the same planned build ID and an exact copyable
`fly -t TARGET watch -b BUILD-ID` command. Keep the version unreleased until
the run is terminal, the complete build log has been inspected, and the
downloaded typed output (including the complete `record.json`) is valid.

## Release

Release is an explicit lifecycle action and does not update a workflow:

```sh
fly -t TARGET agent nodes release code-review "$NODE_VERSION" --compatibility=compatible
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

The server does not enforce a successful test run as a release prerequisite;
the inspect-before-release sequence above is the recommended operator gate.
Direct testing remains available after release.
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
