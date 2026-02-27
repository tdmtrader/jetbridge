---
name: Conductor Complete
description: Mark the active track complete and archive it after verification and checkpoints are finished.
---

# Conductor Complete

Mark the current track as complete and archive it.

**FIRST: Read all context files before doing anything else.**

Read these files NOW:
- conductor/tracks.md
- Active track's plan.md and spec.md

---

## After Reading Context

### Verify Completion

1. Check all tasks in plan.md are marked `[x]`
2. If any tasks are pending `[ ]` or in-progress `[~]`, warn user

### Archive Process (via MCP Tool)

**Use the `conductor_transition_status` MCP tool** to transition the track through completion:

1. **Mark completed:**
   ```
   conductor_transition_status({
     trackId: "<track-id>",
     targetStatus: "completed"
   })
   ```

2. **Archive (optional):**
   ```
   conductor_transition_status({
     trackId: "<track-id>",
     targetStatus: "archived"
   })
   ```

3. **Create completion commit:**
   - Message: "complete(<track-id>): <description>"

**Fallback:** PATCH `/api/projects/:projectId/tracks/:trackId/transition` with `{ targetStatus: "completed" }`

---

## Critical Rules

1. Always read conductor/ context files FIRST
2. Follow workflow.md EXACTLY as written
3. Get user approval before archiving
4. All tasks must be complete before marking track complete
5. **Prefer MCP tools** (`conductor_transition_status`) for status changes — ensures atomic writes and canonical format
