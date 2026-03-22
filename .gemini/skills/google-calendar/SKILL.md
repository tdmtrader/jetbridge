---
name: google-calendar
description: 'View, create, and manage Google Calendar events. Use when user asks about calendar, meetings, schedule, or events.'
argument-hint: 'today | week | add <title> | search <query>'
---

# Google Calendar

View, create, and manage Google Calendar events using the Google Workspace MCP server. Use this skill when the user asks about their calendar, meetings, schedule, or events.

> This skill uses the **workspace-mcp** server — a unified Google Workspace integration. The same server powers Google Docs, Sheets, Calendar, Drive, and more. Set up once, use everywhere.

## Setup (10 minutes)

> **Already set up Google Docs or Sheets skill?** Skip to Step 4 — you can reuse the same credentials. Just make sure the Google Calendar API is enabled.

### Step 1: Create a Google Cloud Project

1. Go to [console.cloud.google.com](https://console.cloud.google.com) in your browser
2. Click the project dropdown at the top, then **New Project**
3. Name it (e.g., "Forge Workspace") and click **Create**

### Step 2: Enable APIs

1. Go to **APIs & Services** then **Library**
2. Search for and enable these APIs:
   - **Google Calendar API**
   - **Google Drive API** (needed for shared files)
3. If using other Google skills, also enable: Google Docs API, Google Sheets API

### Step 3: Create OAuth Credentials

1. Go to **APIs & Services** then **Credentials**
2. Click **+ CREATE CREDENTIALS** then **OAuth client ID**
3. If asked, configure the OAuth consent screen:
   - Choose **External**, fill in app name and your email
   - Add your email as a test user
   - Click through remaining steps
4. For Application type, choose **Desktop app**
5. Name it (e.g., "Forge") and click **Create**
6. Copy the **Client ID** and **Client Secret**

### Step 4: Save Your Credentials

```bash
export GOOGLE_OAUTH_CLIENT_ID="your-client-id.apps.googleusercontent.com"
export GOOGLE_OAUTH_CLIENT_SECRET="your-client-secret"
```

> Add these to your `~/.zshrc` or `~/.bashrc` so they persist across terminal sessions. These credentials are shared with the google-docs and google-sheets skills.

### Step 5: Add the MCP Server

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "google-workspace": {
      "command": "uvx",
      "args": ["workspace-mcp", "--tools", "calendar", "drive"],
      "env": {
        "GOOGLE_OAUTH_CLIENT_ID": "${GOOGLE_OAUTH_CLIENT_ID}",
        "GOOGLE_OAUTH_CLIENT_SECRET": "${GOOGLE_OAUTH_CLIENT_SECRET}",
        "OAUTHLIB_INSECURE_TRANSPORT": "1"
      }
    }
  }
}
```

> **Tip:** If you also use Google Docs or Sheets, add them to the `--tools` list: `["workspace-mcp", "--tools", "calendar", "drive", "docs", "sheets"]`. One server handles all Google services.

### Step 6: First-Time Authentication

On the first tool invocation, the MCP server will provide a Google OAuth URL. Click it, sign in with your Google account, and grant access. Tokens are stored at `~/.google_workspace_mcp/credentials/` and auto-refresh.

### Verification

Restart your AI CLI session, then ask: "What's on my calendar today?" It should return your events.

## Available MCP Tools

### Core Tools
- **list_calendars** — List all accessible calendars
- **get_events** — Retrieve events with time filtering (today, this week, date range)
- **create_event** — Create events with title, time, duration, location, attendees, reminders
- **modify_event** — Update existing events (reschedule, change location, etc.)

### Extended Tools
- **delete_event** — Remove events from calendar

## Common Workflows

### View today's events
Ask: "What's on my calendar today?"

### View this week
Ask: "Show me my calendar for this week"

### Create an event
Ask: "Add a meeting with Sarah tomorrow at 2pm for 30 minutes"

### Find an event
Ask: "When is my next dentist appointment?"

### Reschedule an event
Ask: "Move my 3pm meeting to 4pm"

## Shared Credentials with Google Docs & Sheets

This skill shares Google OAuth credentials with the **google-docs** and **google-sheets** skills. All three use the same `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` from the same GCP project. Enable all the APIs you need in one project.

## Troubleshooting

- **"No calendar tools available"?** Make sure `.mcp.json` is configured (Step 5) and restart your AI CLI session
- **"Authentication failed"?** Delete `~/.google_workspace_mcp/credentials/` and restart to re-authenticate
- **"Calendar API not enabled"?** Go to GCP Console then APIs & Services then Library and enable Google Calendar API
- **"Access blocked"?** Your OAuth consent screen is in "Testing" mode — add your email as a test user in GCP Console
- **MCP server not starting?** Make sure `uvx` is installed (`brew install uv`) and Python 3.10+ is available
