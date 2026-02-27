---
name: conductor:tasks
description: Sync Conductor plan.md tasks with Claude Code's native task system. Use when starting a Conductor track to load tasks, or when completing tasks to update plan.md.
argument-hint: 'load | complete <task-id> <sha> | status'
allowed-tools: Bash(*), Read, Write, Edit, TaskCreate, TaskUpdate, TaskList
---

# Conductor Tasks

Synchronize Conductor's plan.md with Claude Code's native task management system.

## Quick Reference

```bash
# Parse plan.md and load tasks into Claude's task list
/conductor:tasks load

# Mark a task complete (updates both Claude tasks and plan.md)
/conductor:tasks complete <task-id> <commit-sha>

# Show mapping between Claude tasks and plan.md lines
/conductor:tasks status
```

---

## How It Works

Conductor's plan.md is the **source of truth** for task tracking. This skill creates a "view" of those tasks in Claude's native task system, enabling:

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

### `/conductor:tasks load`

Parse the current track's plan.md and create Claude tasks.

**Steps:**

1. Find the current track's plan.md (from conductor/tracks/\*/plan.md)
2. Parse tasks using the helper script
3. Create Claude tasks via TaskCreate
4. Set up phase-based dependencies (Phase 2 tasks blocked by Phase 1 incomplete tasks)

**Example Output:**

```
Loaded 8 tasks from plan.md:
- Phase 1: 3 tasks (2 completed, 1 in progress)
- Phase 2: 5 tasks (5 pending, blocked by Phase 1)
```

### `/conductor:tasks complete <task-id> <sha>`

Mark a task complete in both systems.

**Steps:**

1. Find the task in Claude's task list by ID
2. Get the task's plan.md line number from metadata
3. Update plan.md: `[~]` → `[x] <sha>`
4. Update Claude task status to `completed`

### `/conductor:tasks status`

Show the mapping between Claude tasks and plan.md.

**Steps:**

1. Call `TaskList` to get all Claude tasks
2. Filter to tasks with `conductorTrackId` in metadata
3. Display a table showing the mapping:

**Example Output:**

```
Current Track: sync_claude_planstasks_with_conductor_tracks_20260123

ID | Status      | Line | Phase | Description
---|-------------|------|-------|---------------------------
1  | completed   | 7    | 1     | Research Claude Code's task system
2  | completed   | 8    | 1     | Understand plan mode vs task management
3  | in_progress | 32   | 3     | Implement /conductor:tasks load command
4  | pending     | 52   | 4     | Implement /conductor:tasks complete command
```

---

## Implementation

### Step 1: Identify Current Track

Find the active track by looking for in-progress tracks:

```bash
# Find tracks with [~] status
grep -l "\[~\]" conductor/tracks/*/plan.md 2>/dev/null | head -1
```

Or use the track ID if known:

```bash
TRACK_ID="sync_claude_planstasks_with_conductor_tracks_20260123"
PLAN_PATH="conductor/tracks/$TRACK_ID/plan.md"
```

### Step 2: Parse plan.md

Use the helper script to extract tasks:

```bash
~/.claude/skills/conductor-tasks/scripts/parse-plan.sh "$PLAN_PATH"
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
    "description": "Create /conductor:tasks skill file structure",
    "commitSha": null
  }
]
```

### Step 3: Create Claude Tasks

For each parsed task, call TaskCreate:

```
TaskCreate({
  subject: "Phase 2: Create skill file structure",
  description: "From plan.md line 15: Create /conductor:tasks skill file structure",
  activeForm: "Creating skill file structure",
  metadata: {
    conductorPhase: 2,
    conductorLineNumber: 15,
    conductorTrackId: "sync_claude_planstasks_with_conductor_tracks_20260123"
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
conductor_complete_task({
  trackId: "my_track_20260214",
  taskText: "Implement login endpoint",
  commitSha: "abc1234"
})
```

### Checkpoint a phase:
```
conductor_checkpoint_phase({
  trackId: "my_track_20260214",
  phaseText: "Phase 1: Authentication Setup",
  commitSha: "def5678"
})
```

### Get track status:
```
conductor_get_status({ trackId: "my_track_20260214" })
```

### Transition track lifecycle:
```
conductor_transition_status({ trackId: "my_track_20260214", targetStatus: "completed" })
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
sed -i '' "${LINE_NUMBER}s/\[~\]/[x] $SHA/" conductor/tracks/*/plan.md
```

Or use Claude's Edit tool with the exact line content.

---

## Integration with /conductor:implement

The `/conductor:implement` skill can auto-load tasks at session start:

```markdown
# At the start of implementation:

1. Run /conductor:tasks load
2. Tasks are now visible via Ctrl+T
3. Mark task in-progress when starting work
4. Mark complete when done (auto-updates plan.md)
```

---

## Best Practices

1. **Load once per session**: Run `/conductor:tasks load` at the start of a track
2. **Don't duplicate**: Check TaskList before loading to avoid duplicates
3. **Keep plan.md canonical**: Always update plan.md - Claude tasks are just a view
4. **Use metadata**: Store lineNumber for accurate plan.md updates
