---
name: ship
description: Commit all changes with git notes, push, and optionally bump versions. Use when user asks to "ship", "commit and push", "push everything", or "release a new version".
argument-hint: '[message] | --version [patch|minor|major]'
---

# Ship

Stage, commit, and push changes with proper git notes. Optionally bump versions across all packages.

## Quick Reference

```bash
# Check what will be shipped
./.claude/skills/ship/scripts/check-status.sh

# Bump versions across all packages
./.claude/skills/ship/scripts/bump-version.sh [patch|minor|major]
```

---

## Usage

### Basic Ship (commit + push)

```
/ship "feat: add new feature"
/ship "fix: resolve authentication issue"
```

### Version Bump Ship

```
/ship --version patch "chore: bump version for release"
/ship --version minor "feat: new feature release"
/ship --version major "feat!: breaking change release"
```

---

## Ship Workflow

When user invokes `/ship`:

### Step 1: Check Git Status

Run the status check script:

```bash
./.claude/skills/ship/scripts/check-status.sh
```

Show the user:

- Untracked files
- Modified files
- Current branch

### Step 2: Stage All Changes

```bash
git add -A
```

### Step 3: Confirm with User

Present summary of staged changes and ask for confirmation:

- Number of files to be committed
- List key files (max 10)
- Proposed commit message

### Step 4: Commit with Git Note

Use conventional commit format and attach a git note:

```bash
# Commit
git commit -m "<message>

Co-Authored-By: Claude <noreply@anthropic.com>"

# Get commit hash
COMMIT_SHA=$(git log -1 --format="%H")

# Attach git note with summary
git notes add -m "## Ship Summary

**Message:** <message>
**Files Changed:** <count>
**Timestamp:** $(date -u +"%Y-%m-%dT%H:%M:%SZ")

### Key Changes
- File 1
- File 2
..." $COMMIT_SHA
```

### Step 5: Push

```bash
git push origin $(git branch --show-current)
```

---

## Version Bump Workflow

When user invokes `/ship --version [type]`:

### Step 1: Run Version Bump Script

```bash
./.claude/skills/ship/scripts/bump-version.sh [patch|minor|major]
```

This updates version in:

- Root `package.json`
- `packages/client/package.json`
- `packages/cloud/package.json`
- `packages/desktop/package.json`
- `packages/server/package.json`
- `packages/shared/package.json`
- `packages/terminal-service-rs/package.json`
- `packages/conductor-cli-wrapper/package.json`

### Step 2: Commit Version Bump

```bash
VERSION=$(node -p "require('./package.json').version")
git add -A
git commit -m "chore: bump version to $VERSION

Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Step 3: Create Git Tag

```bash
git tag -a "v$VERSION" -m "Release v$VERSION"
```

### Step 4: Attach Git Note

```bash
git notes add -m "## Version Release

**Version:** $VERSION
**Type:** [patch|minor|major]
**Timestamp:** $(date -u +"%Y-%m-%dT%H:%M:%SZ")
**Packages Updated:** 8

### Affected Packages
- conductor-studio (root)
- @forge/client
- @forge/cloud
- @forge/desktop
- @forge/server
- @forge/shared
- @forge/terminal-service-rs
- @forge/cli-wrapper"
```

### Step 5: Push with Tags

```bash
git push origin $(git branch --show-current) --follow-tags
```

---

## Packages NOT Updated on Version Bump

These packages have independent versions:

- `packages/chatops` (0.0.1) - Standalone ChatOps bot

---

## Semantic Versioning Guide

| Type    | When to Use                       | Example         |
| ------- | --------------------------------- | --------------- |
| `patch` | Bug fixes, minor improvements     | 0.3.32 → 0.3.33 |
| `minor` | New features, backward compatible | 0.3.32 → 0.4.0  |
| `major` | Breaking changes, major rewrites  | 0.3.32 → 1.0.0  |

---

## Critical Rules

1. **Never use --no-verify** - Pre-push hooks exist to catch issues
2. **Always include Co-Authored-By** - For attribution
3. **Always attach git notes** - For audit trail
4. **Wait for user confirmation** - Before committing
5. **Check status first** - Ensure clean working directory is understood
