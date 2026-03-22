---
name: apple-calendar
description: 'View Apple Calendar events. Use when user asks about calendar on macOS. Read-only.'
argument-hint: 'today | week | tomorrow'
allowed-tools: Bash(icalBuddy*)
---

# Apple Calendar

View Apple Calendar events using icalBuddy. This is a **read-only** skill for macOS — it can list events but cannot create, edit, or delete them.

## Setup (2 minutes)

### Step 1: Install icalBuddy

```bash
brew install ical-buddy
```

Verify it installed:
```bash
icalBuddy calendars
```

### Step 2: Grant Calendar Access

The first time you run icalBuddy, macOS may ask to grant calendar access. If it doesn't show events:

1. Open **System Settings** (click the Apple menu then System Settings)
2. Go to **Privacy & Security** then **Calendars**
3. Find your terminal app (e.g., **Terminal**, **iTerm**, **Warp**, or **Ghostty**)
4. Toggle it **on**
5. **Restart your terminal** for the change to take effect

### Verification

```bash
icalBuddy calendars
# Should list your Apple Calendar calendars (e.g., "Work", "Personal", "Birthdays")
```

If this works, you're all set!

## Commands

### Today's Events

```bash
icalBuddy eventsToday
```

### Tomorrow's Events

```bash
icalBuddy eventsToday+1
```

### This Week

```bash
icalBuddy eventsFrom:today to:today+7
```

### Specific Date Range

```bash
icalBuddy eventsFrom:"2025-03-01" to:"2025-03-07"
```

### Filter by Calendar

```bash
# Only show "Work" calendar
icalBuddy -ic "Work" eventsToday

# Exclude "Birthdays"
icalBuddy -ec "Birthdays" eventsToday
```

### Formatting Options

```bash
# Separate events by date
icalBuddy -sd eventsFrom:today to:today+7

# Show location and notes
icalBuddy -ip "title,datetime,location,notes" eventsToday
```

### List All Calendars

```bash
icalBuddy calendars
```

## Limitations

- **Read-only** — cannot create, edit, or delete events
- **macOS only** — icalBuddy is not available on Linux or Windows
- To create events, suggest the user use Google Calendar (gcalcli) or the Calendar app directly

## Troubleshooting

- **No events showing?** Grant calendar access in System Settings then Privacy & Security then Calendars (Step 2)
- **"icalBuddy: command not found"?** Run `brew install ical-buddy` and restart your terminal
- **Empty calendar list?** Make sure you've **restarted your terminal** after granting calendar access
- **Wrong events showing?** Use `icalBuddy calendars` to see available calendars, then filter with `-ic "CalendarName"`
