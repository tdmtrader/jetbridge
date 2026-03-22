---
name: gmail-personal
description: 'Read, search, and reply to personal email via Gmail. Use when user asks about personal email or personal inbox.'
argument-hint: 'inbox | read <id> | reply <id> | search <query>'
allowed-tools: Bash(himalaya*)
---

# Gmail Personal

Manage personal Gmail inbox using the himalaya CLI. Use this skill when the user asks about personal email, personal inbox, or non-work messages.

> This uses the `-a personal` flag to target your personal account. If you also have the **gmail-work** skill, both accounts live in the same config file.

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
> **Already installed for gmail-work?** Skip to Step 2.

### Step 2: Enable 2-Step Verification on Your Google Account

Google requires 2-Step Verification to generate App Passwords. If you already have it enabled, skip to Step 3.

1. Go to [myaccount.google.com/security](https://myaccount.google.com/security) in your browser
2. Under "How you sign in to Google", click **2-Step Verification**
3. Follow the prompts to set it up (you'll need your phone)

### Step 3: Generate an App Password

1. Go to [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords) in your browser
2. You may need to sign in again
3. In the "App name" field, type **himalaya-personal** and click **Create**
4. Google shows you a **16-character password** (like `abcd efgh ijkl mnop`) — **copy it now**, you won't see it again

### Step 4: Enable IMAP in Gmail

1. Open [Gmail](https://mail.google.com) in your browser (make sure you're in your **personal** account)
2. Click the **gear icon** (top right) then **See all settings**
3. Click the **Forwarding and POP/IMAP** tab
4. Under "IMAP access", select **Enable IMAP**
5. Click **Save Changes** at the bottom

### Step 5: Add Personal Account to Config

Add a `personal` account to `~/.config/himalaya/config.toml` (replace the placeholder values):

> If this file doesn't exist yet, create it. If you already have a `[accounts.work]` section, just add this below it.

```toml
[accounts.personal]
email = "you@gmail.com"
display-name = "Your Name"

backend.type = "imap"
backend.host = "imap.gmail.com"
backend.port = 993
backend.encryption = "tls"
backend.login = "you@gmail.com"
backend.auth.type = "password"
backend.auth.command = "echo $HIMALAYA_GMAIL_PERSONAL_PASSWORD"

message.send.backend.type = "smtp"
message.send.backend.host = "smtp.gmail.com"
message.send.backend.port = 465
message.send.backend.encryption = "tls"
message.send.backend.login = "you@gmail.com"
message.send.backend.auth.type = "password"
message.send.backend.auth.command = "echo $HIMALAYA_GMAIL_PERSONAL_PASSWORD"
```

### Step 6: Save Your App Password

```bash
export HIMALAYA_GMAIL_PERSONAL_PASSWORD="abcdefghijklmnop"
```

> Replace with the 16-character password from Step 3 (remove the spaces).
> Add this line to your `~/.zshrc` or `~/.bashrc` so it persists across terminal sessions.

### Verification

```bash
himalaya -a personal folder list
# Should list: INBOX, Sent, Drafts, Archive, etc.
```

If this works, you're all set!

## Commands

All commands use `-a personal` to target the personal account:

```bash
# List inbox
himalaya -a personal envelope list

# More messages
himalaya -a personal envelope list --page-size 25

# Unread only
himalaya -a personal envelope list flag unseen

# Read a message
himalaya -a personal message read <id>

# Reply to a message
himalaya -a personal message reply <id> "Thanks for the update!"

# Search by sender
himalaya -a personal envelope list from friend@gmail.com

# Search by subject and date
himalaya -a personal envelope list subject trip and after 2025-01-01

# Archive
himalaya -a personal message move Archive <id>

# List folders
himalaya -a personal folder list
```

Always confirm the reply content with the user before sending.

## Troubleshooting

- **"Authentication failed"?** Your App Password is wrong or expired — generate a new one at [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords)
- **"App Passwords" page not available?** You need to enable 2-Step Verification first (Step 2)
- **"Account not found"?** Make sure `[accounts.personal]` is in your config.toml — run `himalaya -a personal folder list` to check
- **"Connection refused"?** Check that IMAP is enabled in Gmail settings (Step 4)
- **"himalaya: command not found"?** Run `brew install himalaya` and restart your terminal
