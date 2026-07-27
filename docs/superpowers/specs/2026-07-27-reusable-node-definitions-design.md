# First-Class Reusable Node Definitions

**Status:** Approved conceptual design
**Date:** 2026-07-27

## Context

Agentic workflows frequently repeat the same execution step across many
workflows. Today, the closest Concourse-native analogue is a task file: it
defines a reusable execution recipe with logical inputs, outputs, and
parameters, while each pipeline maps its own artifacts into and out of that
recipe.

Agentic nodes need the same composition model plus stronger identity,
versioning, provenance, and upgrade behavior. A node may include an agent
model, prompt, skills, MCP capabilities, and other implementation assets whose
exact values must remain reproducible. Workflows also need to adopt shared-node
updates deliberately without changing existing revisions or historical runs.

This design establishes conceptual alignment while related workflow, snapshot,
capability, and execution work remains in flight. It intentionally does not
prescribe a code-level implementation plan.

## Design Summary

A reusable node is a first-class, immutable, atomic execution definition. It
resembles a Concourse task file in how workflows map logical inputs, outputs,
and declared parameters. It resembles an imported pipeline configuration in
that all authored dependencies are resolved and captured as part of an
immutable version.

A workflow remains the authoritative unit of composition and execution. It
references exact node versions, maps values to their ports, supplies declared
parameters, and composes nodes through visible Concourse graph structure.

Node updates never mutate workflows. A released node version can be applied to
selected consuming workflows through an explicit upgrade action. Each
successful application creates a validated, immutable, unpromoted workflow
revision that can be inspected and promoted separately.

## Reusable Node Definition

A reusable node definition has a stable identity, such as `code-review`, and a
platform-assigned sequence of immutable versions:

```text
code-review@4
code-review@5
code-review@6
```

Each version defines exactly one visible execution step and contains:

- its task or agent implementation;
- model and harness selection;
- prompts and system instructions;
- skills and context;
- runtime image and command;
- MCP and sidecar capability definitions;
- typed logical input and output ports;
- explicitly supported parameters and defaults;
- output validation requirements; and
- exact immutable identities for every referenced asset.

The model, prompt, skills, image, command, and capabilities are part of the
node. A workflow cannot override them. Changing them creates another immutable
version or a separate node definition.

### Atomic execution

One reusable node is one visible task or agent execution, potentially with
sidecars. A node cannot contain a hidden execution graph.

Agents, deterministic tasks, validators, evaluators, publishers, and other
explicit side-effecting leaf operations use the same outer definition,
versioning, testing, and composition model. Their kinds may impose different
runtime and authorization behavior.

Sequencing, parallelism, retries, hooks, conditional paths, and human waits
remain explicit workflow-owned composition. If many workflows use the same
three nodes in sequence, each workflow still declares those three nodes.
Reusable composite workflows are out of scope.

## Authoring and Import

A node is authored as a small source package. Its manifest declares one
execution step, typed ports, parameters, and referenced assets. Prompts, skill
trees, task configuration, and similar assets may remain separate files for
authoring convenience.

Import resolves the package from an exact source revision and captures
everything needed to reproduce it:

- referenced files and directories;
- images by immutable digest;
- model and harness selection;
- MCP and sidecar capability definitions;
- typed contracts and parameter defaults; and
- source repository, commit, and path provenance.

Unresolved or mutable dependencies fail import. Runtime execution never
rereads the source repository, follows a mutable image tag, resolves a
capability channel, or otherwise changes the meaning of an imported version.

Secret values are not captured inside a node version. The node records its
platform-managed capability requirements, and the trusted runtime supplies
authorized credentials when it executes. Direct invocation does not bypass
the ordinary authorization rules for publishers or other side-effecting
nodes.

## Version Identity

Versions are monotonically increasing integers allocated by the platform
within a node definition. Authors do not assign semantic versions.

The resolved node content has a canonical identity. Reimporting identical
fully resolved content is idempotent and returns the existing version. Any
resolved-content change creates a new version.

Compatibility with an earlier version is declared and validated separately
from the integer version number. This avoids trying to classify prompt, model,
skill, or capability changes as semantic-version major, minor, or patch
increments.

## Public Contract and Parameters

Each node version owns its typed input/output and parameter contract.

A workflow node instance may supply only:

- a stable workflow-local name;
- an exact reusable-node version;
- mappings from workflow values to the node's logical inputs;
- mappings from the node's logical outputs into the workflow; and
- values for parameters explicitly declared by the node version.

Declared parameters provide limited, intentional configurability similar to
Concourse task parameters. They are part of the node's public contract.
Workflows cannot patch undeclared implementation details.

Repository-specific or invocation-specific values should enter through typed
inputs or declared parameters. The reusable node is not tied to the artifact
names, repository, or other local structure of a particular workflow.

## Compatibility

A successor is structurally compatible when every valid invocation of the
earlier version remains valid without changing its workflow composition:

- existing input ports retain their type and required/optional status;
- new input ports are optional;
- existing output ports remain available with the same types;
- existing parameter values remain valid;
- new parameters have defaults; and
- no new workflow-supplied authority or binding is required.

The platform verifies structural compatibility. It does not claim to prove
behavioral equivalence. A model, prompt, skill, or implementation change can be
structurally compatible even though behavior changes materially. Adoption
remains explicit regardless of compatibility.

A node author may deliberately release a breaking successor. A breaking
version can change ports, parameter contracts, or external obligations, but
the break must be declared. For example, adding an MCP that requires a new
workflow-provided input or authority is breaking. Adding a completely
self-contained MCP can remain structurally compatible because it creates no
new consumer obligation.

A breaking version remains part of the same stable node definition when it is
still the successor to that reusable operation. It does not require a separate
semantic-operation object.

## No Separate Semantic Operation Contract

The platform does not introduce a second contract identity such as
`code-review/v1` alongside `code-review@5`.

The stable node-definition identity already groups its versions, and the exact
version identifies what ran. A bespoke or forked review implementation may
have sufficiently different semantics that forcing it into a common contract
or combining its metrics would be misleading.

A fork may record `derived_from: code-review@5` for provenance without
inheriting identity, compatibility, or substitutability. Optional categories
and labels may support catalog search and reporting, but they are descriptive
and non-authoritative.

This replaces the earlier proposal for a separate semantic-operation identity
and conformance contract.

## Testing and Release

Import and release are distinct lifecycle actions.

Import creates an immutable version. Any imported version can be invoked
directly, whether it is unreleased, released, superseded, or deprecated. A
direct invocation binds exact typed input snapshots, supplies declared
parameters, and executes through the same machinery used when the node is
embedded in a workflow.

Conceptually, this is a one-node workflow run. It produces the ordinary logs,
metrics, provenance, validation results, and sealed outputs. Authors can run
different versions against the same input snapshots or fixtures without
creating a separate testing execution system.

Before release, the platform can present:

- the resolved-content diff from an earlier version;
- port and parameter contract changes;
- model, prompt, skill, image, and capability changes;
- direct test runs and comparisons; and
- the declared compatibility classification.

Release makes a version available in the normal catalog, usable for new
workflow composition, and eligible for the consumer-upgrade action. Release
does not change any workflow.

Release requires a complete, valid node version but does not impose a
mandatory regression threshold. Test evidence informs an explicit human
release decision. Testing remains available after release.

Released versions may be deprecated to discourage new use, but they remain
addressable while referenced by workflow revisions or historical runs.
Deprecation never rewrites consumers.

## Workflow Resolution

A workflow revision references exact node versions. Publication resolves every
node and records the fully resolved execution configuration in the immutable
workflow revision.

Runs never consult a latest version or release channel. A run records the
exact workflow revision and node versions it executes. New node releases
therefore cannot affect active or historical runs.

Workflow control flow remains completely visible. A reusable node behaves as a
leaf in that graph, with its logical ports mapped to workflow values in the
same spirit as Concourse task input and output mapping.

## Consumer Upgrade Action

Releasing a successor exposes an upgrade action that discovers workflows
consuming older versions. The operator selects the workflows to which the new
node version should be applied.

For a compatible update, the platform:

1. starts from each selected workflow's promoted revision;
2. replaces the designated node-version references;
3. resolves the complete workflow;
4. validates all contracts, mappings, and execution requirements; and
5. creates a new immutable, unpromoted workflow revision.

One workflow's failed upgrade does not prevent valid revisions from being
created for other selected workflows.

For a breaking update, the platform identifies the new obligations. Each
workflow must be recomposed to provide new mappings, parameters, or authority.
No invalid or partially resolved workflow revision is persisted. The new
immutable revision is created only after the recomposed workflow validates.

An unpromoted revision is complete and immutable, not an editable draft. If it
needs further changes, those changes produce another revision. The currently
promoted revision remains the default for new workflow runs until an operator
explicitly promotes its replacement.

Creating upgraded revisions and promoting them are separate actions. Operators
may inspect tests and diffs, promote only a subset, or leave any workflow
pinned to an older node version indefinitely.

## Identity and Observability

The platform retains three useful identities:

1. **Node definition**, such as `code-review`, spanning its version history.
2. **Exact node version**, such as `code-review@5`, identifying precisely what
   executed.
3. **Workflow-local node instance**, such as `small-fix/review-change`,
   preserving that node's logical role across workflow revisions.

A node's first-class catalog and detail surfaces should expose:

- imported, released, and deprecated versions;
- resolved contents and source provenance;
- contract and compatibility changes;
- direct test runs and their outputs;
- workflows and exact instances consuming each version;
- runtime metrics by exact version and definition lineage; and
- available upgrades and their adoption status.

Workflow history uses the stable local instance identity to follow a logical
step across workflow revisions even when its exact reusable-node version
changes.

## Non-Goals

This design does not introduce:

- reusable composite workflows or hidden sub-DAGs;
- dynamic latest-node resolution at run time;
- workflow-local overrides of node implementation details;
- a separate semantic-operation contract hierarchy;
- automatic mutation or promotion of consuming workflows;
- mandatory quality thresholds for releasing a version; or
- a distinct execution engine for node tests.

## Conceptual Success Criteria

The design is realized when:

- a complete atomic node can be imported as an immutable first-class version;
- any imported version can be invoked directly with typed inputs and declared
  parameters;
- an imported version can be explicitly released for reuse;
- workflows compose released nodes through port mapping, parameters, and exact
  version pinning;
- every run identifies the exact resolved node content it executed;
- a compatible upgrade creates validated, immutable, unpromoted revisions for
  selected consumers;
- a breaking upgrade clearly identifies consumers that require recomposition;
- existing workflow revisions and runs remain unchanged and reproducible; and
- node lineage, version history, tests, consumers, and upgrade adoption remain
  visible without inventing cross-definition equivalence.
