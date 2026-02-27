---
name: conductor-sync
description: Sync this worktree branch with main: pull latest changes, resolve merge conflicts, and ensure no work is lost before completing.. Use when the user asks about conductor sync or mentions $conductor-sync.
---

# Conductor Sync

Sync this worktree's branch with the main branch to stay up to date and prevent merge conflicts at completion time.

**This is a git operation** — it pulls changes from main into the current worktree branch, resolves any conflicts, and ensures no work on either side is lost.

---

## Pre-flight Checks

1. **Verify clean working tree**
   - Run `git status` — must have no uncommitted changes
   - If dirty, ask user to commit or stash first

2. **Identify branches**
   - Current branch: `git branch --show-current`
   - Main branch: usually `main` or `master`
   - Confirm the merge direction with user: "I'll merge main into your current branch"

---

## Sync Process

### Step 1: Fetch Latest
```bash
git fetch origin main
```

### Step 2: Merge Main into Worktree Branch
```bash
git merge origin/main --no-edit
```

### Step 3: Handle Merge Conflicts (if any)

If conflicts occur:
1. List all conflicting files
2. For each conflict:
   - Show both sides of the conflict
   - Explain what changed on main vs. the worktree
   - Ask the user how to resolve (keep ours, keep theirs, or manual merge)
3. After all conflicts are resolved:
   - Stage resolved files
   - Complete the merge commit

### Step 4: Verify

1. Run the test suite to make sure nothing is broken
2. Report summary:
   - How many commits were merged from main
   - Any files that had conflicts and how they were resolved
   - Test results

---

## When to Sync

Use this command:
- **Before completing a track** — ensures clean merge when the worktree is merged back
- **After main has significant changes** — avoid drift that causes painful conflicts later
- **Periodically during long-running tracks** — sync weekly or after major main branch merges

---

## Critical Rules

1. **Never force-push** after syncing — this rewrites history and can lose work
2. **Always verify with tests** after the merge
3. Working directory MUST be clean before starting
4. If the merge is complex, walk through each conflict with the user
5. Get user approval before completing the merge commit
