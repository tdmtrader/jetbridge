---
name: gmail-work
description: 'Read, search, and reply to work email via Gmail. Use when user asks about email, inbox, or messages.'
argument-hint: 'inbox | read <id> | reply <id> | search <query>'
allowed-tools: Bash(himalaya*)
---

# Gmail Work

Manage work Gmail inbox using the himalaya CLI. Use this skill when the user asks about email, inbox, unread messages, or wants to send/reply to emails.

## Setup (10 minutes)

### Step 1: Install himalaya

```bash
brew install himalaya
```

Verify it installed:
```bash
himalaya --version
```

> **No Homebrew?** Use `cargo install himalaya` instead (requires Rust).

### Step 2: Enable 2-Step Verification on Your Google Account

Google requires 2-Step Verification to generate App Passwords. If you already have it enabled, skip to Step 3.

1. Go to [myaccount.google.com/security](https://myaccount.google.com/security) in your browser
2. Under "How you sign in to Google", click **2-Step Verification**
3. Follow the prompts to set it up (you'll need your phone)

### Step 3: Generate an App Password

1. Go to [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords) in your browser
2. You may need to sign in again
3. In the "App name" field, type **himalaya** and click **Create**
4. Google shows you a **16-character password** (like `abcd efgh ijkl mnop`) — **copy it now**, you won't see it again

### Step 4: Enable IMAP in Gmail

1. Open [Gmail](https://mail.google.com) in your browser
2. Click the **gear icon** (top right) then **See all settings**
3. Click the **Forwarding and POP/IMAP** tab
4. Under "IMAP access", select **Enable IMAP**
5. Click **Save Changes** at the bottom

### Step 5: Create the Config File

Create the file `~/.config/himalaya/config.toml` with this content (replace the placeholder values):

```toml
[accounts.work]
default = true
email = "you@company.com"
display-name = "Your Name"

backend.type = "imap"
backend.host = "imap.gmail.com"
backend.port = 993
backend.encryption = "tls"
backend.login = "you@company.com"
backend.auth.type = "password"
backend.auth.command = "echo $HIMALAYA_GMAIL_WORK_PASSWORD"

message.send.backend.type = "smtp"
message.send.backend.host = "smtp.gmail.com"
message.send.backend.port = 465
message.send.backend.encryption = "tls"
message.send.backend.login = "you@company.com"
message.send.backend.auth.type = "password"
message.send.backend.auth.command = "echo $HIMALAYA_GMAIL_WORK_PASSWORD"
```

### Step 6: Save Your App Password

```bash
export HIMALAYA_GMAIL_WORK_PASSWORD="abcdefghijklmnop"
```

> Replace with the 16-character password from Step 3 (remove the spaces).
> Add this line to your `~/.zshrc` or `~/.bashrc` so it persists across terminal sessions.

### Verification

```bash
himalaya folder list
# Should list: INBOX, Sent, Drafts, Archive, etc.
```

If this works, you're all set!

## Commands

### List Inbox

```bash
# Recent messages
himalaya envelope list

# More messages
himalaya envelope list --page-size 25

# Unread only
himalaya envelope list flag unseen

# From a specific sender
himalaya envelope list from user@example.com
```

### Read a Message

```bash
himalaya message read <id>
```

### Reply to a Message

```bash
himalaya message reply <id> "Thanks, I'll review this today."
```

Always confirm the reply content with the user before sending.

### Search Messages

```bash
# By sender
himalaya envelope list from colleague@company.com

# By subject
himalaya envelope list subject "quarterly report"

# By date range
himalaya envelope list after 2025-01-01 and before 2025-02-01

# Combined
himalaya envelope list from boss@company.com and subject review and after 2025-01-01
```

### Archive Messages

```bash
himalaya message move Archive <id>
himalaya message move Archive <id1> <id2> <id3>
```

### List Folders

```bash
himalaya folder list
```

## Troubleshooting

- **"Authentication failed"?** Your App Password is wrong or expired — generate a new one at [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords)
- **"App Passwords" page not available?** You need to enable 2-Step Verification first (Step 2)
- **"Connection refused"?** Check that IMAP is enabled in Gmail settings (Step 4)
- **"himalaya: command not found"?** Run `brew install himalaya` and restart your terminal
- **Empty inbox?** Try `himalaya envelope list --page-size 50` — the default only shows 10 messages
- **Folder not found?** Run `himalaya folder list` to see the exact folder names
