# Learnings

### 2026-04-25 [missing-capability]

`forge_start_task` / `forge_complete_task` MCP tools fail to match multi-line task text — even when the literal text from plan.md is supplied. Tasks in this plan are intentionally multi-line (Task: leading line + 4-6 line description + File: footer) to keep diffs readable; the tool reports "Task not found in plan.md". Falling back to manual `Edit` on plan.md works but loses the atomic write / metadata.json sync that MCP would provide. Suggested fix: tool should normalize whitespace (collapse newlines+indent to single space) before matching, OR accept just the first non-empty line of the task as the lookup key.
