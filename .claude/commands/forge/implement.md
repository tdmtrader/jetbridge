---
name: Forge Implement
description: Execute the active track plan through the TDD task workflow; use during implementation when progressing tasks and checkpoints.
---

# Forge Implement

Execute tasks from the current track's plan.

**FIRST: Read all context files before doing anything else.**

Read these files NOW:
- forge/product.md
- forge/tech-stack.md
- forge/workflow.md
- forge/tracks.md

Then read the active track's files:
- forge/tracks/<active-track>/spec.md
- forge/tracks/<active-track>/plan.md
- forge/tracks/<active-track>/cgx.md

Also check for project notes that may provide additional context:
- List forge/notes/ directory for any .md files
- Read relevant notes for decisions, architecture, or documentation
- Notes contain user-written context that may inform implementation

---

## IMPORTANT: Use MCP Tools for All Track Operations

**Before editing ANY forge file manually, check if the `forge` MCP server is connected.** If available, you MUST use MCP tools instead of editing files directly:

| Operation | MCP Tool |
|-----------|----------|
| Change track status | `forge_transition_status({ trackId, targetStatus })` |
| Mark task complete | `forge_complete_task({ trackId, taskText, commitSha })` |
| Checkpoint a phase | `forge_checkpoint_phase({ trackId, phaseHeading, commitSha })` |
| Create a new track | `forge_create_track({ name, type })` |
| Get track status | `forge_get_status({ trackId })` |
| List all tracks | `forge_list_tracks()` |
| Add a task to a phase | `forge_add_task({ trackId, phaseText, taskText })` |
| Edit a task description | `forge_edit_task({ trackId, oldTaskText, newTaskText })` |
| Remove a task | `forge_remove_task({ trackId, taskText })` |
| Start a task (mark [~]) | `forge_start_task({ trackId, taskText })` |
| Add a learning | `forge_add_learning({ trackId, category, content })` |

MCP tools ensure atomic writes, canonical formatting, and keep metadata.json + tracks.md in sync.

---

## After Reading Context

### Find Next Task
1. Look for in-progress task `[~]`
2. If none, find first pending task `[ ]`

### Execute TDD Workflow (per workflow.md)

1. **Mark In Progress**
   - **Preferred (task):** Use `forge_start_task({ trackId, taskText })` to mark the task `[~]`
   - **Preferred (track):** Use `forge_transition_status({ trackId, targetStatus: "in_progress" })` for track-level status
   - **Fallback:** Update plan.md: `[ ]` → `[~]`

2. **Red Phase**
   - Write failing tests first
   - Tests must fail for the right reason

3. **Green Phase**
   - Implement minimum code to pass tests
   - Run tests to verify

4. **Refactor Phase**
   - Clean up code
   - Maintain test coverage (>80%)

5. **Commit**
   - Conventional commit message
   - Attach git note with task summary

6. **Mark Complete (via MCP Tool)**
   - **Preferred:** Use `forge_complete_task` MCP tool: `forge_complete_task({ trackId, taskText, commitSha })`
   - **Fallback:** POST to `/api/projects/:projectId/tracks/:trackId/complete-task` with `{ taskText, commitSha }`
   - **Manual fallback:** Update plan.md: `[~]` → `[x] <commit-sha>`
   - Commit plan update

### Phase Completion Protocol

When ALL tasks in a phase are complete:

1. Ensure test coverage for all phase changes
2. Run full test suite
3. Present manual verification steps to user
4. **WAIT for explicit user "yes"**
5. Create checkpoint commit
6. Attach verification report as git note
7. **Preferred:** Use `forge_checkpoint_phase` MCP tool: `forge_checkpoint_phase({ trackId, phaseHeading, commitSha })`
8. **Fallback:** POST to `/api/projects/:projectId/tracks/:trackId/checkpoint-phase` with `{ phaseHeading, commitSha }`
9. **Manual fallback:** Update plan.md with `[checkpoint: <sha>]`

### CGX Logging (Continuous Improvement)

During implementation, **actively log observations to cgx.md**.
Use `forge_add_learning` MCP tool when available: `forge_add_learning({ trackId, category, content })`

Categories:
1. **frustration** - Any friction, confusion, or repeated attempts
2. **good-pattern** - Workflows that worked well (to encode as skills/commands)
3. **anti-pattern** - Mistakes or inefficiencies to prevent
4. **missing-capability** - Tools or features that would have helped
5. **improvement** - Concrete suggestions for new extensions

Format entries with dates: `- [YYYY-MM-DD] Description`

At track completion, run `/forge:improve` to analyze and generate improvements.

---

## Critical Rules

1. Always read forge/ context files FIRST
2. Follow workflow.md EXACTLY as written
3. Get user approval before making changes
4. NEVER skip the Red phase - tests first!
5. ALWAYS wait for user approval at phase checkpoints
6. Log observations to cgx.md during implementation
7. **ALWAYS prefer MCP tools** (`forge_transition_status`, `forge_complete_task`, `forge_checkpoint_phase`, `forge_add_learning`) over manual file editing — MCP ensures atomic writes and keeps all files in sync
