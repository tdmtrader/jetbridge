# Task 8 report — reusable-node vertical slice and operator contract

## Scope

Task 8 closes the first reusable-node slice with:

- a checked-in atomic `code-review` schema-1 node package;
- a pure-Go end-to-end lifecycle proof over production compiler, renderer,
  binder, release, promotion, and selected-upgrade behavior;
- design-alignment and operator documentation; and
- explicit deferrals for UI, generalized experiment integration, and
  source-owning nodes.

The existing multi-step `code-review-v3` workflow seed remains unchanged.

## TDD evidence

The vertical test was written before the node seed existed:

```text
GOCACHE=/private/tmp/concourse-go-cache go test ./agent/reusablenode \
  -run TestReusableNodeVerticalSlice -count=1
```

RED failed at the intended first missing seam:

```text
load code-review node seed:
lstat ../workflow/seeds/code-review-node-v1: no such file or directory
```

The seed contract tests were then added before the seed directory:

```text
GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow \
  -run 'TestOnlySupportedEngineeringSeedsRemain|TestCodeReviewReusableNodeSeedFreezesItsAtomicImplementation' \
  -count=1
```

RED proved both the missing catalog entry and missing source directory. After
adding the source package, the exact focused seed command passed.

## Seed contract

`agent/workflow/seeds/code-review-node-v1` freezes:

- logical `before`/`after` repository inputs and one `review/v1` output;
- the declared string parameter `MINIMUM_SEVERITY=medium`;
- model `claude-sonnet`;
- the complete review prompt;
- the selected `skills/review` tree;
- a digest-pinned dev-MCP sidecar image, fixed command, TCP port, and
  `dev-mcp/v1` contract; and
- one visible `agent` leaf with no hidden DAG.

Compilation erases source capability aliases after resolving their sidecar
authority. The common agent-runner image remains trusted server-selected
admission data; it is not a node-authored override.

## End-to-end proof

The vertical test:

1. imports unreleased `code-review@1`;
2. creates a fresh direct exact run with two typed snapshot bindings and an
   explicit non-default parameter;
3. verifies the rendered leaf retains the frozen prompt/model/skill tree and
   dev-MCP authority;
4. releases version 1;
5. imports and promotes a workflow with `uses: code-review@1`;
6. verifies one visible mapped leaf and one exact binding keyed by immutable
   workflow definition ID;
7. changes only the prompt, imports and directly runs unreleased version 2;
8. releases version 2 as structurally compatible with predecessor 1;
9. applies version 2 only to the selected workflow;
10. verifies the generated revision is immutable and unpromoted with exact
    version-2 binding and rewritten authored source; and
11. verifies the live revision remains on version 1 while the historical
    version-1 run retains its exact node identity, canonical rendered config
    and hash, and snapshot bindings.

The local adapter intentionally does not claim SQL durability. It indexes
bindings by immutable workflow definition ID and coherently models a fresh
`created=true` run through admitting, execution attachment, and the
admitting-to-running CAS. Durable evidence remains:

- Task 5 migration/DB specs for literal kind columns, composite foreign keys,
  atomic definition-plus-binding insertion, idempotent binding comparison, and
  paged consumer discovery;
- Task 6 DB specs for exact kind-scoped node runs and completed-run selection;
  and
- the accepted production factories wired by Tasks 2, 5, and 6.

## Implementation decisions recorded

- The reusable node is a separate seed; existing workflow seeds are not
  rewritten or upgraded automatically.
- Model, prompt, skills, parameters, and node-owned capability
  image/command/ports are version content.
- The platform-owned agent-runner image remains server-selected.
- A workflow expansion remains one visible leaf and retains the authored exact
  `uses:` source for future upgrades.
- Compatible successor additions preserve existing consumer mappings: omitted
  new optional inputs and unmapped new outputs stay outside the consumer
  namespace, while new defaulted parameters resolve on the leaf without
  changing authored bindings.
- Compatible selected upgrades create unpromoted revisions; promotion stays a
  separate workflow action.
- One selected-upgrade response is capped at 4 MiB encoded. Breaking upgrades
  share one immutable obligation graph and preflight the bound before
  per-workflow work; oversized batches receive stable HTTP 422
  `response_limit_exceeded` guidance to select fewer workflows.
- UI, node-backed experiment matrices, and source-owning node grammar remain
  explicitly deferred.

## Integration findings

The high-fidelity vertical RED found two cross-task gaps that narrower tests did
not exercise:

1. A fresh direct node run originally changed `RenderedFunction.TemplateName`
   to the `agent-node-...` namespace after initial validation. Resume validated
   the rendered target again using the ordinary computed template name and
   rejected its own durable render as
   `ErrCorruptPartialAdmission: durable parameterized config hash mismatch`.
   The stored JSON was valid, byte-for-byte canonical, and retained the exact
   renderer hash. The existing Task 6 test used a preterminal
   concurrent-winner shortcut and never traversed fresh `created=true` resume.
2. The workflow Fly importer performed plain client-side
   `CompileDefinition`, so a workflow containing `uses:` was rejected before
   the node-aware server store could resolve it.

Both are corrected with focused production regressions:

- `bca5489a93` validates a fresh direct node run against its ordinary canonical
  render before applying the node template namespace at persistence time. The
  regression exercises a real `created=true` admitting-to-running path rather
  than a preterminal shortcut.
- `6e5df7b909` makes both directory and single-file Fly workflow imports resolve
  exact released nodes through the authenticated node catalog before local
  compilation. `61a93c3eea` additionally rejects incomplete or inconsistent
  release metadata returned by that catalog.

The bounded acceptance review also closed two upgrade-edge gaps:

- `1f63846429` preserves existing authored bindings when a compatible
  successor adds optional inputs, outputs, or defaulted parameters; and
- `b877682cc1` shares breaking obligations, preflights the 4 MiB aggregate
  result ceiling before per-workflow work, and maps an oversized response to
  stable HTTP 422 batching guidance.

The full Fly integration suite then found an integration-fixture omission,
not another product request: one lifecycle spec starts three separate Fly
processes, and each process validates the target through `GET /api/v1/info`.
The fixture modeled only the first validation. Commit `5af4a85b2a` adds the
two missing validation handlers before the release and deprecation requests;
the focused lifecycle spec and all 669 Fly integration specs pass.

## Verification

Final focused evidence:

- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow -run
  'TestEverySeedPortTypeHasABuiltInValidator|TestSeedPromptsDeclareSubjectsInLexicographicOrder|TestOnlySupportedEngineeringSeedsRemain|TestCodeReviewReusableNodeSeedFreezesItsAtomicImplementation|TestCompileDefinitionWithNodesComposesAgentFromMappedSuccessorPorts|TestCompileDefinitionWithNodesComposesTaskFromMappedSuccessorPorts|TestNodeUpgradeComposesCompatibleSuccessorAdditionsWithoutChangingBindings|TestNodeUpgradeResultResponseBudgetAcceptsAtBoundaryAndRejectsOver|TestNodeUpgradeBreakingBudgetRejectsBeforeWorkflowWork|TestNodeUpgradeBreakingResultsShareImmutableObligations'
  -count=1` (`ok`, 0.450s)
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/reusablenode -run
  TestReusableNodeVerticalSlice -count=1` (`ok`, 1.923s)
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflowrun -run
  TestBindAndCreateRunsExactUnreleasedNodeVersion -count=1` (`ok`, 1.546s)
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./fly/commands -run
  TestImportWorkflowDirResolvesExactReleasedReusableNodes -count=1` (`ok`,
  1.107s)
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/api/nodeupgrades
  -run
  'TestNodeUpgradeRejectsOversizedBackendResultWithBoundedClientError|TestNodeUpgradeMapsResponseBudgetSentinelToBoundedClientError'
  -count=1` (`ok`, 0.471s)
- PASS: tracked and new Task 8 files pass `git diff --check`/no-index
  whitespace checks.

Final milestone evidence:

- PASS:
  `go test -p=2 ./agent/workflow/... ./agent/workflowrun
  ./agent/reusablenode ./agent/api/nodes ./agent/api/noderuns
  ./agent/api/nodeupgrades ./fly/commands -count=1`.
- PASS: `go test -p=2 ./atc/builds ./atc/exec ./atc/atccmd -count=1`.
- PASS outside the filesystem/network sandbox:
  `go test -p=2 ./atc/api ./atc/wrappa -count=1`. The sandboxed attempt failed
  only because `httptest` could not bind localhost.
- PASS outside the network sandbox: `make test-dev-mcp`.
- PASS outside the network sandbox: `make test-fly-integration`
  (`669 Passed`, `0 Failed`).
- PASS: `helm lint deploy/chart`, `git diff --check`, exact origin ancestry,
  and clean reusable-node paths/index.

PostgreSQL at the fixed local test port `127.0.0.1:5434` returned no response,
so the DB integration target and repository-wide `make test-unit` prerequisite
were unavailable. They were not repeatedly retried. The accepted Task 2, 5,
and 6 reports retain the exact host database and migration evidence for node
kind fencing, lifecycle storage, bindings, consumers, and runs.

The first focused Fly rerun exhausted the filesystem while linking. Only the
task-created, regenerable shared Go build cache was cleared; the focused rerun
and full integration suite then passed. Unrelated in-flight working-tree
changes and caches owned by other work were preserved.
