---
name: asana
description: 'Manage Asana tasks and projects. Use when user asks about Asana tasks, projects, or work items.'
argument-hint: 'tasks | projects | create <task> | search <query>'
---

# Asana

Manage Asana tasks and projects using the Asana MCP server. Use this skill when the user asks about Asana tasks, projects, sprints, or work tracking.

> This skill works through an MCP server — once set up, you'll have access to 47 Asana tools directly.

## Setup (5 minutes)

### Step 1: Get Your Personal Access Token

1. Go to [app.asana.com/0/my-apps](https://app.asana.com/0/my-apps) in your browser
2. Click **Create new token**
3. Give it a name (e.g., "Forge") and click **Create token**
4. Copy the token — **save it now**, you won't see it again

### Step 2: Save Your Token

```bash
export ASANA_ACCESS_TOKEN="your-token-here"
```

> Add this line to your `~/.zshrc` or `~/.bashrc` so it persists across terminal sessions.

### Step 3: Add the MCP Server

Add the Asana MCP server to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "asana": {
      "command": "npx",
      "args": ["-y", "@roychri/mcp-server-asana"],
      "env": {
        "ASANA_ACCESS_TOKEN": "${ASANA_ACCESS_TOKEN}"
      }
    }
  }
}
```

### Verification

Restart your AI CLI session, then ask it to list your Asana workspaces. It should return your workspace names.

## Available Tools

Once connected, you have access to these MCP tools:

### Tasks
- **asana_search_tasks** — Search tasks with filters (assignee, project, due date, etc.)
- **asana_create_task** — Create a new task in a project
- **asana_get_task** — Get full task details
- **asana_update_task** — Update task name, description, due date, assignee, etc.
- **asana_create_subtask** — Add subtasks to a parent task
- **asana_set_parent_for_task** — Move a task under a parent

### Projects
- **asana_search_projects** — Find projects by name
- **asana_get_project** — Get project details
- **asana_create_project** — Create a new project
- **asana_get_project_sections** — List sections in a project
- **asana_create_section** — Add a section to a project

### Comments & Activity
- **asana_get_task_stories** — Get comments and activity on a task
- **asana_create_task_story** — Add a comment to a task

### Tags
- **asana_create_tag** — Create a tag
- **asana_update_tag** — Modify a tag
- **asana_get_tags_for_task** — List tags on a task
- **asana_add_tag_for_task** — Tag a task
- **asana_remove_tag_for_task** — Remove a tag from a task

### Workspaces
- **asana_list_workspaces** — List all workspaces

## Common Workflows

### List my tasks for today
Ask: "Show me my Asana tasks due today"

### Create a task
Ask: "Create an Asana task 'Review Q1 report' in the Marketing project, due Friday"

### Update task status
Ask: "Mark the 'Update docs' task as complete in Asana"

### Search for tasks
Ask: "Search Asana for tasks assigned to me in the Engineering project"

## Troubleshooting

- **"No Asana tools available"?** Make sure `.mcp.json` is configured (Step 3) and restart your AI CLI session
- **"Authentication failed"?** Your token is wrong or expired — generate a new one at [app.asana.com/0/my-apps](https://app.asana.com/0/my-apps)
- **"Workspace not found"?** Run `asana_list_workspaces` to see available workspaces
- **MCP server not starting?** Make sure Node.js is installed and `npx` is available
