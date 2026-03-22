---
name: google-docs
description: 'Read, create, and edit Google Docs. Use when user asks about Google Docs, documents, or writing in Google Drive.'
argument-hint: 'read <doc> | create <title> | edit <doc> | search'
---

# Google Docs

Read, create, and edit Google Docs using the Google Workspace MCP server. Use this skill when the user asks about Google Docs, documents, or Google Drive files.

> This skill uses the **workspace-mcp** server — a unified Google Workspace integration. The same server powers Google Docs, Sheets, Calendar, Drive, and more. Set up once, use everywhere.

## Setup (10 minutes)

> **Already set up Google Calendar or Sheets skill?** Skip to Step 4 — you can reuse the same credentials. Just make sure the Google Docs API is enabled.

### Step 1: Create a Google Cloud Project

1. Go to [console.cloud.google.com](https://console.cloud.google.com) in your browser
2. Click the project dropdown at the top, then **New Project**
3. Name it (e.g., "Forge Workspace") and click **Create**

### Step 2: Enable APIs

1. Go to **APIs & Services** then **Library**
2. Search for and enable these APIs:
   - **Google Docs API**
   - **Google Drive API** (needed for file search and listing)
3. If using other Google skills, also enable: Google Calendar API, Google Sheets API

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

> Add these to your `~/.zshrc` or `~/.bashrc` so they persist across terminal sessions. These credentials are shared with the google-calendar and google-sheets skills.

### Step 5: Add the MCP Server

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "google-workspace": {
      "command": "uvx",
      "args": ["workspace-mcp", "--tools", "docs", "drive"],
      "env": {
        "GOOGLE_OAUTH_CLIENT_ID": "${GOOGLE_OAUTH_CLIENT_ID}",
        "GOOGLE_OAUTH_CLIENT_SECRET": "${GOOGLE_OAUTH_CLIENT_SECRET}",
        "OAUTHLIB_INSECURE_TRANSPORT": "1"
      }
    }
  }
}
```

> **Tip:** If you also use Google Calendar or Sheets, add them to the `--tools` list: `["workspace-mcp", "--tools", "docs", "drive", "calendar", "sheets"]`. One server handles all Google services.

### Step 6: First-Time Authentication

On the first tool invocation, the MCP server will provide a Google OAuth URL. Click it, sign in with your Google account, and grant access. Tokens are stored at `~/.google_workspace_mcp/credentials/` and auto-refresh.

### Verification

Restart your AI CLI session, then ask: "List my recent Google Docs." It should return document titles.

## Available MCP Tools

### Core Tools
- **get_doc_content** — Read a document's full text content
- **create_doc** — Create a new Google Doc with a title
- **modify_doc_text** — Edit text in a document (with formatting and links)

### Extended Tools
- **search_docs** — Find documents by name in Google Drive
- **find_and_replace_doc** — Find and replace text across a document
- **list_docs_in_folder** — List documents in a specific Drive folder
- **insert_doc_elements** — Add tables, lists, and page breaks
- **update_paragraph_style** — Apply styles, headings, and formatting
- **get_doc_as_markdown** — Export document content as Markdown
- **export_doc_to_pdf** — Export document as PDF

### Complete Tools
- **insert_doc_image** — Insert images from Drive or URLs
- **update_doc_headers_footers** — Modify headers and footers
- **batch_update_doc** — Execute multiple document operations at once
- **inspect_doc_structure** — Analyze document structure
- **read_document_comments** — Read comments on a document
- **create_document_comment** — Add a comment
- **reply_to_document_comment** — Reply to a comment
- **resolve_document_comment** — Resolve a comment

### Google Drive Tools (included)
- **search_drive_files** — Search files across Google Drive
- **get_drive_file_content** — Read file content
- **create_drive_file** — Create files in Drive

## Common Workflows

### Read a document
Ask: "Read my Google Doc titled 'Project Proposal'"

### Create a document
Ask: "Create a new Google Doc called 'Meeting Notes - Feb 24' with a header and bullet points"

### Edit a document
Ask: "Add a summary section to the bottom of my 'Q1 Report' doc"

### Search for documents
Ask: "Find my Google Docs about marketing"

### Export a document
Ask: "Export my 'Design Spec' doc as Markdown"

## Shared Credentials with Google Calendar & Sheets

This skill shares Google OAuth credentials with the **google-calendar** and **google-sheets** skills. All three use the same `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` from the same GCP project.

## Troubleshooting

- **"No Google Docs tools available"?** Make sure `.mcp.json` is configured (Step 5) and restart your AI CLI session
- **"Authentication failed"?** Delete `~/.google_workspace_mcp/credentials/` and restart to re-authenticate
- **"Docs API not enabled"?** Go to GCP Console then APIs & Services then Library and enable Google Docs API
- **"Access blocked"?** Your OAuth consent screen is in "Testing" mode — add your email as a test user in GCP Console
- **MCP server not starting?** Make sure `uvx` is installed (`brew install uv`) and Python 3.10+ is available
