# Workflow Source Format, Deploy Model, and Skills — Design

**Date:** 2026-07-17
**Status:** Approved direction (amends §2 and §6 of `2026-07-07-agentic-platform-end-state-design.md`)
**Depends on:** workflow-store (plan 05, landed), manual dispatch (plan 11 slice, landed)

## Motivation

Workflow definitions are already versioned, content-hashed data with an import
path (`fly agent workflows import`) and human promotion (`set-live`). What is
missing:

1. **A composable source format.** Prompts live as multi-paragraph strings
   inside YAML (see `agent/workflow/seeds/standard-dev.yaml`) — no
   highlighting, poor diffs, no reuse. There is no way to ship skills, extra
   context, or a system-prompt layer with a workflow at all.
2. **A deploy story.** Workflow edits via fly/API produce no review artifact,
   no diff, no history narrative — inconsistent with a platform whose thesis
   is "agent pushes a branch, human merges, evidence attached." The
   process-intelligence loop (retrospective tickets proposing workflow edits)
   has nowhere reviewable to land its edits.

This design adds a directory-based source format (prompt files, skills,
context, system prompt), a transparent import transport, and a
Concourse-native deploy model — without changing the store, the render path,
or promotion semantics. It is a new front door on the same store.

## Principles

- **Standard pipelines-that-deploy model.** Workflow source lives anywhere —
  its own repo, several workflows in one repo, or in-tree next to a project.
  The platform's server surface stays exactly two verbs: **import** (mint a
  content-hashed version) and **set-live** (promotion). Continuous deployment
  is an ordinary user-authored pipeline that watches a repo and runs
  `fly agent workflows import` — the same pattern as a pipeline that runs
  `set-pipeline` on itself. The platform ships an *example* deploy pipeline,
  not a sync component.
- **Set-live remains a human decision**, imperative and scriptable (like
  `fly set-pipeline` today). No merge-implies-live, no live-pin file, no
  reconciler. An `--set-live` flag on import gives auto-promote pipelines
  their one-liner; manual promotion stays the default.
- **Versions change iff source changes.** The content hash is computed over a
  canonical serialization of the source tree, so it is stable across fly and
  server upgrades. Scorecard lineage stays clean.
- **Skills are provider-neutral.** The grammar assumes any agent runner
  supports skills; the runner maps them into its provider's discovery
  location. Nothing in the grammar names Claude.
- **Hooks are deferred** — and *rejected*, not ignored: a `hooks:` key is an
  import-time error (the same refuse-don't-drop rule the dispatch renderer
  applies to unenforced config, Fix A / `ErrRenderRefused`).
- **Trust boundary unchanged.** Workflow-authored content (skills, context,
  prompts) materializes into **agent pods only**, never the harvest pod.
  Harvest's independent re-verification stays outside anything a workflow
  can author. Agent pods hold no credentials, so a malicious skill can waste
  budget but cannot push.

## 1. Source format

A workflow is a directory:

```
develop/
  workflow.yml              # §6 grammar + the additions below
  prompts/
    implement.md
  system/
    base.md
    implement.md
  context/
    concourse-conventions.md
    tdd-checklist.md
  skills/
    tdd/
      SKILL.md              # a skill = a directory: SKILL.md + supporting files
      references/red-green.md
    concourse-idioms/
      SKILL.md
```

`workflow.yml` grammar additions (all optional; existing documents parse
unchanged):

```yaml
schema_version: 2            # documents using any new field declare 2

# -- prompts: file references as an alternative to inline strings
prompts:
  review: |
    Inline prompts keep working.
prompt_files:
  implement: prompts/implement.md    # inlined into prompts at import; a key
                                     # may not appear in both maps

# -- skills: workflow-global set; names resolve to skills/<name>/ in this dir
skills: [tdd, concourse-idioms]

# -- system prompt: appended to the runner's baseline system prompt
system_prompt_file: system/base.md    # or inline: system_prompt: |

# -- context: injected at session start for every agent step
context:
  - context/concourse-conventions.md

steps:
- agent: implement
  prompt: implement
  skills: [extra-skill]                    # ADDITIVE to the workflow-global set
  system_prompt_file: system/implement.md  # REPLACES the workflow-level layer
  context: [context/tdd-checklist.md]      # ADDITIVE to the workflow-global list
```

Semantics:

- **Skills:** effective set for a step = workflow-global ∪ step-additional.
  Step lists never remove or replace.
- **System prompt:** the workflow-level value is *appended* to the runner's
  baseline system prompt (preserving provider defaults, in the spirit of
  `--append-system-prompt`). A step-level value replaces the workflow-level
  layer only — never the baseline.
- **Context:** same additive pattern as skills. The runner injects the
  concatenated content at session start however its provider supports it
  (for the claude runner: a session-start context block — what superpowers
  does via its SessionStart hook, done platform-side so it needs no hook
  support).
- **Degenerate case:** a directory containing only `workflow.yml` with inline
  prompts is valid, and today's single-file format continues to import
  unchanged. The three seeds and the live `smoke`/`analyze`/`develop`
  workflows need no edits.
- **Sharing across workflows in one repo** is done with symlinks
  (`develop/skills/tdd -> ../../lib/skills/tdd`), which fly dereferences
  during packaging. No cross-directory reference grammar; the manifest always
  contains real files.
- **Identity:** the `name:` field, not the directory path, remains workflow
  identity (import 400s on name mismatch, as today). Renames and moves are
  safe.

### schema_version

Additive fields land as `schema_version: 2`, per the plan-05 freeze rule
(slot shapes change via new schema_version, never mutation of v1). v1
documents continue to parse; a v2 document on a pre-v2 server is a 400
(unknown schema_version), which is the correct failure.

## 2. Transport: JSON source manifest

The import body becomes a transparent files map:

```json
{ "files": {
    "workflow.yml": "...",
    "prompts/implement.md": "...",
    "skills/tdd/SKILL.md": "..." } }
```

- Fly walks the directory (dereferencing symlinks), builds the map, and POSTs
  it to the existing per-name route
  `POST /api/v1/agent/workflows/:workflow_name/versions`
  (Content-Type `application/json`).
- The raw-YAML body (any other content type) keeps working for scratch and
  API users; the server wraps it into a single-file manifest
  (`{"files": {"workflow.yml": <bytes>}}`) before hashing, so the hash scheme
  is uniform.
- No tar, no binary handling, no extraction guards. Validation on map keys:
  relative paths only, no `..` segments, no absolute paths, no empty
  components; file-count cap (512) and total-size cap (10 MiB; per-file
  1 MiB).
- Text encoding: file contents are UTF-8 strings. Binary assets in skills are
  out of scope for v1 (reject non-UTF-8); revisit only if a real skill needs
  one.
- **Rejected alternative — import-by-git-reference** (server fetches
  `repo@sha:path`, ArgoCD-style): kills uncommitted local iteration
  (`fly agent workflows import ./develop` while hacking on a prompt), adds a
  server-side git-credential surface, and buys nothing for deploy pipelines,
  whose task containers already have the checkout via the git resource. If
  wanted later it slots behind the same route as an alternate body
  (`{"git": {"uri", "ref", "path"}}`) without disturbing anything.

## 3. Server: compile, hash, store

Server-side compile (fly packages, the server compiles):

1. **Validate** the manifest (paths, caps, UTF-8), parse `workflow.yml`,
   eager-validate the grammar (phaseconfig-style, as today). Every
   `prompt_files`, `system_prompt_file`, `context[]`, and `skills[]`
   reference must resolve to files present in the manifest; a skill is the
   whole tree under `skills/<name>/` and must contain
   `skills/<name>/SKILL.md`. Unreferenced files (a README, design notes) are
   *allowed* and hashed — they are source too, and an edit to them correctly
   mints a new version. Fly excludes hidden files/dirs at packaging so
   junk (`.DS_Store`, `.git`) never reaches the manifest. Errors cite source
   paths (`prompts/implement.md`, `workflow.yml: skills[1]`).
2. **Compile** the `Definition`: inline `prompt_files` references, resolve
   the system-prompt and context layers per step, record the skill file
   trees. The compiled Definition is what the renderer consumes; the render
   path does not read the manifest.
3. **Hash** = sha256 over the canonical manifest serialization (paths sorted,
   exact bytes). Import stays idempotent on hash: re-importing identical
   source returns the existing version. One-time consequence: the hash scheme
   changes from raw-YAML bytes to canonical manifest, so each existing live
   workflow mints one new version on its next import — accepted (a handful
   of workflows exist; lineage note in the version row records the scheme).
4. **Store** the canonical manifest on the version row (new column on
   `agent_workflow_definitions`' version storage) alongside the compiled
   Definition. No blob store, no separate bundle table; the files map is the
   stored form. Migration number: allocate above the deployed
   `jetbridgeHeadMigration` at landing time (ticket-core renumber precedent;
   next free is 1773106066+, confirm at landing).

Why server-side compile (decided after weighing fly-side): the server must
store and materialize file trees regardless (skills mount into pods), so the
client pre-chewing buys nothing; a compiler versioned with fly would make
version hashes depend on the fly build — a fly bump in the deploy pipeline's
image could mint spurious versions of every workflow, polluting scorecard
lineage; server-side keeps the API first-class (curl/CI/UI can import) and
error messages source-addressed.

## 4. Render and runner

- The renderer materializes each step's effective skill set (files from the
  stored manifest) read-only into the step's workspace, alongside the
  existing `spec.md`/`plan.md` materialization. Same mechanism family
  (write-task); if the base64/arg-size path gets tight for large skill sets,
  fall back to an authenticated fetch-by-version endpoint — implementation
  detail for the plan, not a design commitment.
- The agent step schema gains `SystemPrompt`, `Context`, and `Skills`
  (materialized-path list) fields, flowing through the rendered pipeline
  template into the runner (§8.1 env family).
- The runner maps materialized skills into its provider's discovery location
  (claude runner: the workspace's `.claude/skills/`), appends the resolved
  system-prompt layer, and injects context at session start.
- **Coexistence with target-repo skills:** the repo's own `.claude/` remains
  in effect — workflow skills are about the *task type*, repo skills about
  the *repo*. On a name collision the workflow's skill wins, and the runner
  logs the shadowing.
- **Refusal discipline:** until the render/runner slice lands, render REFUSES
  definitions that declare skills/system_prompt/context (422 via
  `ErrRenderRefused`), consistent with how v0 render refuses sidecars —
  never silently dropped.

## 5. fly UX

- `fly agent workflows import <dir|file>` — directory or single file. A
  directory containing several `workflow.yml`-bearing subdirectories imports
  each in turn (fly iterates; the route stays per-name). Each import is
  independent: a failure in one workflow leaves the others imported, and an
  idempotent re-run converges — deploy pipelines can simply retry.
- `import --set-live` — promote in the same call, for auto-promote deploy
  pipelines.
- `show` — renders the compiled definition plus a source manifest summary
  (per-file sizes, per-skill file counts) rather than dumping trees.
- `set-live`, `list` — unchanged.

## 6. Example deploy pipeline (shipped as an example, not a component)

```yaml
resources:
- name: workflows-repo
  type: git
  source: {uri: git@github.com:me/my-workflows.git, branch: main}

jobs:
- name: deploy-workflows
  plan:
  - get: workflows-repo
    trigger: true
  - task: import
    config:
      # image with fly; runs:
      #   fly agent workflows import workflows-repo/workflows/ [--set-live]
```

Auto-promote (`--set-live`) versus import-only is the pipeline author's
policy, not platform machinery. With this, the retrospective loop closes:
a retrospective ticket edits the workflows repo, harvest pushes
`agent/ticket-N`, a human reviews the process change *as a diff*, merges,
and the deploy pipeline imports the new version — whose scorecard is then
comparable against its predecessor, with the git history explaining why it
exists.

## 7. Out of scope

- Hooks (`hooks:` key rejected at import; revisit as its own design).
- A mandated/central workflows repo; any platform-side reconciler or
  live-pin file; merge-implies-live.
- Import-by-git-reference (documented alternate body if wanted later).
- OCI artifacts; multi-env overlays; binary files in skills.
- Any change to promotion semantics, the store's versioning model, or the
  render path's consumption of compiled Definitions.

## 8. Rollout

Two slices:

- **(a) Source format + import.** Manifest transport, server compile +
  validation, schema_version 2 grammar (prompt files, skills, system prompt,
  context), manifest storage, fly `import <dir>` / `--set-live` / `show`
  summary. Render refuses the new surfaces. Everything importable and
  inspectable; existing workflows unaffected.
- **(b) Materialization.** Renderer materializes skills into agent pods;
  runner maps skills/system-prompt/context (system_prompt/context are pure
  text plumbing and land at the front of this slice); example deploy
  pipeline published; proven with a live dispatch of a skill-bearing
  workflow on theborg.

## 9. Testing

- Grammar/compile: table-driven parse+validate tests in `agent/workflow`
  (missing file refs, unreferenced files, path traversal keys, caps, v1/v2
  schema gating, symlink-free manifests, hash canonicalization/idempotency).
- Store: factory tests over the new manifest column (existing
  `agent_workflows_factory` recipe).
- fly: integration specs against the mock ATC (directory walk, symlink
  deref, multi-workflow iteration, `--set-live`).
- Render/runner: render-refusal specs in slice (a); materialization and
  shadowing-log specs in slice (b); one live smoke on theborg (skill-bearing
  `analyze` variant) closes slice (b).

## Decision log

- Pipelines-that-deploy model; no central repo, no sync component
  (user, 2026-07-17).
- Skills in the definition; assume every agent supports skills; hooks
  deferred (user, 2026-07-17).
- Skill attachment: workflow-global + per-step additive (user, 2026-07-17).
- Server-side compile, fly-side packaging (recommended, accepted).
- JSON manifest transport instead of tar; git-ref import rejected for v1
  (user objected to tar; manifest recommended, accepted 2026-07-17).
- System prompt append-at-workflow / replace-at-step; context additive
  (recommended in design, unobjected).
- Grammar realization: `prompt_files` sibling map instead of a
  string-or-object union under `prompts:` (implementation, slice (a) —
  additive, keeps every existing Prompts consumer untouched; a key may
  not appear in both maps).
- Migration landed as 1773106066, vacating the 1773106065 PARK-V2
  reservation — PARK-V2 renumbers above the deployed head at landing
  (ticket-core precedent; noted in migrate-preflight.sh).
