---
name: task-tracker
description: 'Personal task tracker — add, list, complete, and archive tasks. Zero dependencies. Use when user asks about tasks, todos, or tracking work.'
argument-hint: 'add <title> | list | complete <id> | archive'
allowed-tools: Bash(cat*), Bash(echo*), Read, Write, Edit
---

# Task Tracker

A zero-dependency personal task tracker that stores tasks in `.forge/tasks.json`. No external tools needed — just read and write JSON.

## Quick Reference

```
/task-tracker add "Fix the login bug"
/task-tracker list
/task-tracker complete 3
/task-tracker archive
```

## When to Use

- User asks "what are my tasks" or "what do I need to do"
- User asks to add a task, todo, or reminder
- User asks to mark something as done
- User asks to see completed or archived tasks
- User mentions "task", "todo", "to-do list", or "backlog"

## Data Format

Tasks are stored in `.forge/tasks.json`:

```json
{
  "tasks": [
    {
      "id": 1,
      "title": "Fix login bug",
      "status": "todo",
      "priority": "high",
      "created": "2025-03-01T10:00:00Z",
      "completed": null,
      "tags": ["bug"]
    }
  ],
  "archived": [],
  "nextId": 2
}
```

### Task Schema

| Field       | Type                                     | Description                    |
| ----------- | ---------------------------------------- | ------------------------------ |
| `id`        | number                                   | Auto-incrementing ID           |
| `title`     | string                                   | Task description               |
| `status`    | `"todo"` \| `"in_progress"` \| `"done"` | Current status                 |
| `priority`  | `"low"` \| `"medium"` \| `"high"`       | Priority level                 |
| `created`   | ISO 8601 string                          | When the task was created      |
| `completed` | ISO 8601 string \| null                  | When the task was completed    |
| `tags`      | string[]                                 | Optional categorization tags   |

## Operations

### Initialize (if .forge/tasks.json doesn't exist)

Read the file. If it doesn't exist, create it:

```json
{
  "tasks": [],
  "archived": [],
  "nextId": 1
}
```

### Add a Task

1. Read `.forge/tasks.json`
2. Create a new task object with `id: nextId`, `status: "todo"`, `created: now`
3. Add to `tasks` array
4. Increment `nextId`
5. Write back to file

Default priority is `"medium"` unless specified.

### List Tasks

1. Read `.forge/tasks.json`
2. Display active tasks (not archived) as a table:

```
ID  Priority  Status       Title                    Tags
1   high      todo         Fix login bug            bug
2   medium    in_progress  Add dark mode            feature
3   low       todo         Update README            docs
```

Sort by: priority (high > medium > low), then by ID.

### Complete a Task

1. Read `.forge/tasks.json`
2. Find task by ID
3. Set `status: "done"` and `completed: now`
4. Write back to file

### Start a Task

1. Read `.forge/tasks.json`
2. Find task by ID
3. Set `status: "in_progress"`
4. Write back to file

### Archive Completed Tasks

1. Read `.forge/tasks.json`
2. Move all tasks with `status: "done"` from `tasks` to `archived`
3. Write back to file

This keeps the active list clean while preserving history.

### View Archived Tasks

1. Read `.forge/tasks.json`
2. Display `archived` array as a table

### Set Priority

1. Read `.forge/tasks.json`
2. Find task by ID
3. Set `priority` to the new value
4. Write back to file

### Add Tags

1. Read `.forge/tasks.json`
2. Find task by ID
3. Append tag(s) to `tags` array
4. Write back to file

## HTML Viewer

To view tasks in the Visual Explorer, generate an HTML file from the template:

1. Read `.forge/tasks.json`
2. Read the template at `templates/task-viewer.html` (relative to this skill directory)
3. Replace `__TASKS_JSON__` in the template with the JSON content
4. Write to `forge/playground/my-tasks.html`
5. Open via the Forge MCP tool:
   ```
   forge_viewer_open({ "path": "forge/playground/my-tasks.html" })
   ```
   **Fallback (if MCP not available):** `curl -X POST "${FORGE_API_URL:-http://localhost:5280}/api/viewer/open" -H "Content-Type: application/json" -d '{"path": "forge/playground/my-tasks.html"}'`

## Tips

- Tasks persist across sessions in `.forge/tasks.json`
- Archive regularly to keep the active list manageable
- Use tags to categorize: `bug`, `feature`, `docs`, `chore`
- The HTML viewer auto-refreshes when you update and re-open
