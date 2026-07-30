# Task 4 — executable frozen skill runtime

## Design

- Compilation attaches each agent's exact selected `skills/<name>/...` tree to
  `AgentStep.SkillFiles`; the function-wide `SkillFiles` remains the durable
  union identity.
- Rendering validates safe paths, the 512 KiB global bound, exact per-agent
  trees, and the absence of a logical `skills` input. Extraction carries only
  the selected leaf's map.
- Planning copies the per-agent map. Runtime makes a deterministic in-memory
  tar with relative paths, streams it into a real worker artifact volume, and
  mounts that volume read-only at the logical `skills` path. It is not a DAG
  artifact and never rereads source.
- Checkpoint provenance uses a `compiled` skill mode that binds selected names
  plus canonical sorted frozen file bytes, without requiring an external
  `skills` input. Mapped repository lookup remains physical while runtime
  authority and mount paths stay logical.

## TDD evidence

- RED: `TestCompileV3CollectsWholeSelectedSkillTrees` did not compile because
  `AgentStep.SkillFiles` did not exist.
- GREEN: the compiler test passed after per-agent compilation was added.
- RED: full render/extraction rejected valid frozen skill maps as unsupported.
- GREEN: full render and extraction now accept internally consistent frozen
  maps and extraction retains only the selected leaf.
- RED: `TestFrozenAgentSkillArtifact*` did not compile because the immutable
  tar artifact did not exist.
- GREEN: deterministic sorted read-only tar, safe-path refusal, real
  worker-volume materialization, StreamIn failure handling, and mapped
  typed-input/output behavior pass.

## Verification

Passed on 2026-07-29:

`go test ./agent/workflow ./atc/builds ./atc/exec -count=1`

`git diff --check` passed.

## Caveat

The generated worker volume is read-only but ordinary (not `Private`): the
Jetbridge private-input path intentionally skips normal artifact hydration.
This preserves executable hydration without adding a new worker seam; skills
contain compiled instruction content rather than secrets.

## Round 2 review fixes

- RED: frozen `skills` output collisions were rejected only incidentally by
  typed-port coverage; mapped human review returned the logical candidate to
  the authoritative repository gate.
- GREEN: compiler, render/extract, executor, and checkpoint provenance now
  reject `skills` as either logical input or output for frozen skill agents.
  Review requirement rendering maps candidate, validation, and validation-base
  names into the repository namespace; runtime still mounts the logical port.
- Focused evidence passed:
  `go test ./agent/workflow ./atc/exec -run
  'TestCompileV3RejectsInvalidAgentAssets|TestFullFunctionTargetRejectsFrozenSkillOutputCollision|TestRenderValidationRequirementsMapsHumanReviewAuthorityToRepositoryNames|TestAgentValidationRequirementUsesPhysicalMappedCandidate|TestDeriveAgentCheckpointProvenanceRejectsCompiledSkillOutputCollision' -count=1`

## Round 2 follow-up regression

- `TestRequireValidationRequirementUsesPhysicalMappedRepositoryNames` registers
  only physical candidate/validation repository keys, asserts logical keys are
  absent, and proves both physical snapshot identities reach the authoritative
  validation content gate. The existing mapped AgentStep regression verifies
  the normal input remains mounted at its logical container path.
- Passed:
  `go test ./atc/exec -run
  'TestAgentValidationRequirementUsesPhysicalMappedCandidate|TestRequireValidationRequirementUsesPhysicalMappedRepositoryNames' -count=1`
  and `go test ./atc/exec -run TestExec -ginkgo.focus='uses physical
  repository and sealer keys while retaining logical container paths' -count=1`.
- Full verification passed again:
  `go test ./agent/workflow ./atc/builds ./atc/exec -count=1` and
  `git diff --check`.

## Final review blocker and resolution

- Final bounded review found that compile-time type flow still passed a
  mapped review's logical validation and base names into a physical artifact
  environment, so valid node imports failed before rendering.
- `requireValidation` now applies the agent input mapping consistently to the
  candidate, validation, and provenance base names. Publish and await callers
  pass no mapping and retain their prior behavior.
- RED was the new mapped-flow test failing to compile against the old
  validation seam. GREEN evidence:
  `go test ./agent/workflow -run
  'Test(TypeCheckMappedHumanReviewValidation|RequireValidationMapsHumanReviewRepositoryNames|AuthoritativeValidationFlowRejectsOrdinaryAndStaleBindings)' -count=1`.
- Fresh `go test ./agent/workflow/... -count=1` and `git diff --check` passed.
