---
name: Forge Status
description: Display current project and track progress from Forge artifacts; use before starting, resuming, or handing off work.
---

# Forge Status

Show current project and track status.

## Data Sources (in priority order)

### 1. MCP Tools (Preferred)
Use these MCP tools to get canonical track data:
- `forge_list_tracks` — returns all tracks with status, type, and metadata
- `forge_get_status` — returns detailed status of a specific track (pass `{ trackId }`)

### 2. REST API (Fallback)
- GET `/api/projects/:projectId/tracks/list` — list all tracks
- GET `/api/projects/:projectId/tracks/:trackId` — get track detail

### 3. Direct File Read (Last Resort)
Read these files:
- forge/product.md
- forge/tracks.md
- All forge/tracks/*/metadata.json
- All forge/tracks/*/plan.md

Also check for recent manual changes:
- Read forge/notes/.changelog.json if it exists
- Shows recent file edits made through the Notepad UI
- Display as "Recent Manual Changes" section if present

---

## After Reading Context

### Calculate Metrics

For each track, count:
- Pending tasks: `[ ]`
- In-progress tasks: `[~]`
- Completed tasks: `[x]`

### Display Format

```
📊 Forge Status

Project: <project-name>
Tracks: <total> | Tasks: <completed>/<total>

━━━ In Progress ━━━
🔵 <track-id> (<type>)
   ████████░░░░░░░░ 50% (4/8 tasks)
   Current: <current-task-name>

━━━ Planned ━━━
⚪ <track-id> (<type>)
   0% (0/6 tasks)

━━━ Completed ━━━
✅ <track-id> (<type>)
   Completed: <date>

━━━ Next Actions ━━━
→ Continue: /project:forge:implement
→ New track: /project:forge:newTrack "description"
```

### Edge Cases

- No tracks: Suggest creating one
- Not initialized: Suggest running setup

---

## Critical Rules

1. **Prefer MCP tools** (`forge_list_tracks`, `forge_get_status`) for reading track data — returns canonical format
2. Follow workflow.md EXACTLY as written
3. Get user approval before making changes
