---
name: apple-reminders
description: 'Manage Apple Reminders — add, complete, list, and delete reminders. Use when user asks about reminders, todos, or tasks on macOS.'
argument-hint: 'today | add <task> | complete <id> | list'
allowed-tools: Bash(remindctl*)
---

# Apple Reminders

Manage Apple Reminders using the remindctl CLI. Use this skill when the user asks about reminders, todos, tasks, or to-do lists on macOS.

## Setup (2 minutes)

### Step 1: Install remindctl

```bash
brew install steipete/tap/remindctl
```

Verify it installed:
```bash
remindctl --version
```

### Step 2: Grant Reminders Access

The first time you run remindctl, macOS will ask to grant Reminders access. If it doesn't work:

1. Open **System Settings** (click the Apple menu then System Settings)
2. Go to **Privacy & Security** then **Reminders**
3. Find your terminal app (e.g., **Terminal**, **iTerm**, **Warp**, or **Ghostty**)
4. Toggle it **on**
5. **Restart your terminal** for the change to take effect

Or run:
```bash
remindctl authorize
```

### Verification

```bash
remindctl status
# Should show: Reminders access: granted

remindctl list
# Should list your reminder lists (e.g., "Reminders", "Work", "Personal")
```

If this works, you're all set!

## Commands

### View Reminders

```bash
# Today's reminders (default)
remindctl

# Tomorrow
remindctl tomorrow

# This week
remindctl week

# Overdue items
remindctl overdue

# All reminders
remindctl all

# Specific date
remindctl 2026-03-01
```

### Add a Reminder

```bash
# Quick add
remindctl add "Buy groceries"

# Add with details
remindctl add --title "Call dentist" --list Personal --due tomorrow

# Add to a specific list
remindctl add --title "Review PR" --list Work
```

### Complete a Reminder

```bash
# Complete by ID (shown in list output)
remindctl complete 1

# Complete multiple
remindctl complete 1 2 3
```

### Edit a Reminder

```bash
remindctl edit 1 --title "Updated task name" --due 2026-03-15
```

### Delete a Reminder

```bash
remindctl delete 1 --force
```

### Manage Lists

```bash
# Show all lists
remindctl list

# Show a specific list
remindctl list Work

# Create a new list
remindctl list "New Project" --create

# Rename a list
remindctl list Work --rename Office

# Delete a list
remindctl list "Old Project" --delete
```

### Output Formats

```bash
# JSON output (useful for parsing)
remindctl --json

# Plain tab-separated output
remindctl --plain

# Count only
remindctl --quiet
```

## Troubleshooting

- **"Permission denied"?** Grant Reminders access in System Settings then Privacy & Security then Reminders (Step 2)
- **"remindctl: command not found"?** Run `brew install steipete/tap/remindctl` and restart your terminal
- **No reminders showing?** Make sure you've **restarted your terminal** after granting access, or run `remindctl authorize`
- **Wrong list?** Run `remindctl list` to see all available lists
