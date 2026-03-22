---
name: google-sheets
description: 'Read, write, and create Google Sheets. Use when user asks about spreadsheets, Google Sheets, or tabular data in Google Drive.'
argument-hint: 'read <sheet> | write <range> | create <title> | list'
---

# Google Sheets

Read, write, and create Google Sheets using the Google Workspace MCP server. Use this skill when the user asks about spreadsheets, Google Sheets, or tabular data.

> This skill uses the **workspace-mcp** server — a unified Google Workspace integration. The same server powers Google Docs, Sheets, Calendar, Drive, and more. Set up once, use everywhere.

## Setup (10 minutes)

> **Already set up Google Calendar or Docs skill?** Skip to Step 4 — you can reuse the same credentials. Just make sure the Google Sheets API is enabled.

### Step 1: Create a Google Cloud Project

1. Go to [console.cloud.google.com](https://console.cloud.google.com) in your browser
2. Click the project dropdown at the top, then **New Project**
3. Name it (e.g., "Forge Workspace") and click **Create**

### Step 2: Enable APIs

1. Go to **APIs & Services** then **Library**
2. Search for and enable these APIs:
   - **Google Sheets API**
   - **Google Drive API** (needed for listing and searching)
3. If using other Google skills, also enable: Google Calendar API, Google Docs API

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

> Add these to your `~/.zshrc` or `~/.bashrc` so they persist across terminal sessions. These credentials are shared with the google-calendar and google-docs skills.

### Step 5: Add the MCP Server

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "google-workspace": {
      "command": "uvx",
      "args": ["workspace-mcp", "--tools", "sheets", "drive"],
      "env": {
        "GOOGLE_OAUTH_CLIENT_ID": "${GOOGLE_OAUTH_CLIENT_ID}",
        "GOOGLE_OAUTH_CLIENT_SECRET": "${GOOGLE_OAUTH_CLIENT_SECRET}",
        "OAUTHLIB_INSECURE_TRANSPORT": "1"
      }
    }
  }
}
```

> **Tip:** If you also use Google Calendar or Docs, add them to the `--tools` list: `["workspace-mcp", "--tools", "sheets", "drive", "calendar", "docs"]`. One server handles all Google services.

### Step 6: First-Time Authentication

On the first tool invocation, the MCP server will provide a Google OAuth URL. Click it, sign in with your Google account, and grant access. Tokens are stored at `~/.google_workspace_mcp/credentials/` and auto-refresh.

### Verification

Restart your AI CLI session, then ask: "List my Google Sheets." It should return spreadsheet titles.

## Available MCP Tools

### Core Tools
- **read_sheet_values** — Read cell values from a range (e.g., "Sheet1!A1:D10")
- **modify_sheet_values** — Write values, update cells, or clear ranges
- **create_spreadsheet** — Create a new spreadsheet with a title

### Extended Tools
- **list_spreadsheets** — List your accessible spreadsheets
- **get_spreadsheet_info** — Get spreadsheet metadata (sheets, ranges, properties)
- **format_sheet_range** — Apply colors, fonts, alignment, and number formats

### Complete Tools
- **create_sheet** — Add a new sheet (tab) to a spreadsheet
- **read_sheet_comment** — Read comments on a sheet
- **create_sheet_comment** — Add a comment to a cell
- **reply_to_sheet_comment** — Reply to a comment
- **resolve_sheet_comment** — Resolve a comment

### Google Drive Tools (included)
- **search_drive_files** — Search spreadsheets across Google Drive
- **get_drive_file_content** — Read file content

## Common Workflows

### Read spreadsheet data
Ask: "Read the data from my 'Budget 2026' spreadsheet, Sheet1 A1 to F20"

### Update cells
Ask: "Set cell B5 in my 'Sales Tracker' sheet to 150"

### Create a new spreadsheet
Ask: "Create a Google Sheet called 'Project Timeline' with columns: Task, Owner, Due Date, Status"

### Search for a spreadsheet
Ask: "Find my Google Sheets about revenue"

### Format cells
Ask: "Make the header row bold and add a blue background in my 'Budget' sheet"

## Shared Credentials with Google Calendar & Docs

This skill shares Google OAuth credentials with the **google-calendar** and **google-docs** skills. All three use the same `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` from the same GCP project.

## Troubleshooting

- **"No Google Sheets tools available"?** Make sure `.mcp.json` is configured (Step 5) and restart your AI CLI session
- **"Authentication failed"?** Delete `~/.google_workspace_mcp/credentials/` and restart to re-authenticate
- **"Sheets API not enabled"?** Go to GCP Console then APIs & Services then Library and enable Google Sheets API
- **"Spreadsheet not found"?** The spreadsheet must be in your Google Drive or shared with you
- **"Access blocked"?** Your OAuth consent screen is in "Testing" mode — add your email as a test user in GCP Console
- **MCP server not starting?** Make sure `uvx` is installed (`brew install uv`) and Python 3.10+ is available
