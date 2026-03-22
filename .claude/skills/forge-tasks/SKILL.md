---
name: forge:tasks
description: Sync Forge plan.md tasks with Claude Code's native task system. Use when starting a Forge track to load tasks, or when completing tasks to update plan.md.
argument-hint: 'load | complete <task-id> <sha> | status'
allowed-tools: Bash(*), Read, Write, Edit, TaskCreate, TaskUpdate, TaskList
---

# Forge Tasks

Synchronize Forge's plan.md with Claude Code's native task management system.

## Quick Reference

```bash
# Parse plan.md and load tasks into Claude's task list
/forge:tasks load

# Mark a task complete (updates both Claude tasks and plan.md)
/forge:tasks complete <task-id> <commit-sha>

# Show mapping between Claude tasks and plan.md lines
/forge:tasks status
```

---

## How It Works

Forge's plan.md is the **source of truth** for task tracking. This skill creates a "view" of those tasks in Claude's native task system, enabling:

1. **Task visibility**: See plan.md tasks via `Ctrl+T` or `/tasks`
2. **Native task tools**: Use TaskUpdate to track progress
3. **Plan.md sync**: Changes reflect back to plan.md

### Task Status Mapping

| plan.md | Claude Task Status |
| ------- | ------------------ |
| `[ ]`   | `pending`          |
| `[~]`   | `in_progress`      |
| `[x]`   | `completed`        |

---

## Commands

### `/forge:tasks load`

Parse the current track's plan.md and create Claude tasks.

**Steps:**

1. Find the current track's plan.md (from forge/tracks/\*/plan.md)
2. Parse tasks using the helper script
3. Create Claude tasks via TaskCreate
4. Set up phase-based dependencies (Phase 2 tasks blocked by Phase 1 incomplete tasks)

**Example Output:**

```
Loaded 8 tasks from plan.md:
- Phase 1: 3 tasks (2 completed, 1 in progress)
- Phase 2: 5 tasks (5 pending, blocked by Phase 1)
```

### `/forge:tasks complete <task-id> <sha>`

Mark a task complete in both systems.

**Steps:**

1. Find the task in Claude's task list by ID
2. Get the task's plan.md line number from metadata
3. Update plan.md: `[~]` → `[x] <sha>`
4. Update Claude task status to `completed`

### `/forge:tasks status`

Show the mapping between Claude tasks and plan.md.

**Steps:**

1. Call `TaskList` to get all Claude tasks
2. Filter to tasks with `forgeTrackId` in metadata
3. Display a table showing the mapping:

**Example Output:**

```
Current Track: sync_claude_planstasks_with_forge_tracks_20260123

ID | Status      | Line | Phase | Description
---|-------------|------|-------|---------------------------
1  | completed   | 7    | 1     | Research Claude Code's task system
2  | completed   | 8    | 1     | Understand plan mode vs task management
3  | in_progress | 32   | 3     | Implement /forge:tasks load command
4  | pending     | 52   | 4     | Implement /forge:tasks complete command
```

---

## Implementation

### Step 1: Identify Current Track

Find the active track by looking for in-progress tracks:

```bash
# Find tracks with [~] status
grep -l "\[~\]" forge/tracks/*/plan.md 2>/dev/null | head -1
```

Or use the track ID if known:

```bash
TRACK_ID="sync_claude_planstasks_with_forge_tracks_20260123"
PLAN_PATH="forge/tracks/$TRACK_ID/plan.md"
```

### Step 2: Parse plan.md

Use the helper script to extract tasks:

```bash
~/.claude/skills/forge-tasks/scripts/parse-plan.sh "$PLAN_PATH"
```

Output is JSON array of tasks with metadata:

```json
[
  {
    "phase": 1,
    "phaseTitle": "Research & Design",
    "lineNumber": 7,
    "status": "completed",
    "description": "Research Claude Code's task system",
    "commitSha": null
  },
  {
    "phase": 2,
    "phaseTitle": "Skill Infrastructure",
    "lineNumber": 15,
    "status": "pending",
    "description": "Create /forge:tasks skill file structure",
    "commitSha": null
  }
]
```

### Step 3: Create Claude Tasks

For each parsed task, call TaskCreate:

```
TaskCreate({
  subject: "Phase 2: Create skill file structure",
  description: "From plan.md line 15: Create /forge:tasks skill file structure",
  activeForm: "Creating skill file structure",
  metadata: {
    forgePhase: 2,
    forgeLineNumber: 15,
    forgeTrackId: "sync_claude_planstasks_with_forge_tracks_20260123"
  }
})
```

For in-progress tasks, immediately call TaskUpdate to set status:

```
TaskUpdate({
  taskId: "1",
  status: "in_progress"
})
```

### Step 4: Set Up Dependencies

Phase 2+ tasks should be blocked by incomplete Phase 1 tasks:

```
TaskUpdate({
  taskId: "5",  // Phase 2 task
  addBlockedBy: ["1", "2", "3"]  // Incomplete Phase 1 task IDs
})
```

---

## Updating plan.md via MCP Tools (Preferred)

**Always prefer MCP tools** over direct file editing for plan.md updates. The Conductor MCP server ensures atomic writes and canonical format.

### Mark a task complete:
```
forge_complete_task({
  trackId: "my_track_20260214",
  taskText: "Implement login endpoint",
  commitSha: "abc1234"
})
```

### Checkpoint a phase:
```
forge_checkpoint_phase({
  trackId: "my_track_20260214",
  phaseText: "Phase 1: Authentication Setup",
  commitSha: "def5678"
})
```

### Get track status:
```
forge_get_status({ trackId: "my_track_20260214" })
```

### Transition track lifecycle:
```
forge_transition_status({ trackId: "my_track_20260214", targetStatus: "completed" })
```

### Fallback: REST API
If MCP tools are unavailable, use the REST API:
```bash
# Complete a task
curl -X POST http://localhost:${CONDUCTOR_PORT:-5280}/api/projects/$PROJECT_ID/tracks/$TRACK_ID/complete-task \
  -H "Content-Type: application/json" \
  -d '{"taskText": "Implement login endpoint", "commitSha": "abc1234"}'

# Checkpoint a phase
curl -X POST http://localhost:${CONDUCTOR_PORT:-5280}/api/projects/$PROJECT_ID/tracks/$TRACK_ID/checkpoint-phase \
  -H "Content-Type: application/json" \
  -d '{"phaseText": "Phase 1: Authentication Setup", "commitSha": "def5678"}'
```

### Last Resort: Direct File Editing
Only if both MCP and REST are unavailable:

```bash
# Get line number from task metadata
LINE_NUMBER=15
SHA="abc1234"

# Update the line (sed on macOS)
sed -i '' "${LINE_NUMBER}s/\[~\]/[x] $SHA/" forge/tracks/*/plan.md
```

Or use Claude's Edit tool with the exact line content.

---

## Integration with /forge:implement

The `/forge:implement` skill can auto-load tasks at session start:

```markdown
# At the start of implementation:

1. Run /forge:tasks load
2. Tasks are now visible via Ctrl+T
3. Mark task in-progress when starting work
4. Mark complete when done (auto-updates plan.md)
```

---

## Best Practices

1. **Load once per session**: Run `/forge:tasks load` at the start of a track
2. **Don't duplicate**: Check TaskList before loading to avoid duplicates
3. **Keep plan.md canonical**: Always update plan.md - Claude tasks are just a view
4. **Use metadata**: Store lineNumber for accurate plan.md updates
