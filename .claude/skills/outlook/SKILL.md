---
name: outlook
description: 'Read, search, and reply to Outlook/Microsoft 365 email. Use when user asks about Outlook email or work Microsoft email.'
argument-hint: 'inbox | read <id> | reply <id> | search <query>'
allowed-tools: Bash(himalaya*)
---

# Outlook Email

Manage Outlook/Microsoft 365 email using the himalaya CLI. Use this skill when the user asks about Outlook, Microsoft email, or Office 365 email.

## Setup (10 minutes)

> **Important for work/corporate accounts:** Your IT admin may need to enable IMAP access for your mailbox. If Step 4 fails with "Connection refused", ask your IT team to "enable IMAP for my mailbox in Exchange Admin Center".

### Step 1: Install himalaya

```bash
brew install himalaya
```

Verify it installed:
```bash
himalaya --version
```

> **No Homebrew?** Use `cargo install himalaya` instead (requires Rust).

### Step 2: Generate an App Password

The process differs depending on whether you have a **personal** or **work** Microsoft account:

**For personal accounts** (outlook.com, hotmail.com, live.com):
1. Go to [account.live.com/proofs/AppPassword](https://account.live.com/proofs/AppPassword) in your browser
2. Sign in if prompted
3. Click **Create a new app password**
4. Copy the password that appears — **save it now**, you won't see it again

**For work/school accounts** (Microsoft 365):
1. Go to [mysignins.microsoft.com/security-info](https://mysignins.microsoft.com/security-info) in your browser
2. Click **+ Add sign-in method**
3. Select **App password** from the dropdown
4. Give it a name (e.g., "himalaya") and click **Next**
5. Copy the password that appears

> **Don't see "App password" as an option?** Your admin may have disabled app passwords. Ask your IT team to enable them, or check if your organization uses a different authentication method.

### Step 3: Create the Config File

Add an `outlook` account to `~/.config/himalaya/config.toml` (replace the placeholder values):

```toml
[accounts.outlook]
email = "you@company.com"
display-name = "Your Name"

backend.type = "imap"
backend.host = "outlook.office365.com"
backend.port = 993
backend.encryption = "tls"
backend.login = "you@company.com"
backend.auth.type = "password"
backend.auth.command = "echo $HIMALAYA_OUTLOOK_PASSWORD"

message.send.backend.type = "smtp"
message.send.backend.host = "smtp.office365.com"
message.send.backend.port = 587
message.send.backend.encryption = "start-tls"
message.send.backend.login = "you@company.com"
message.send.backend.auth.type = "password"
message.send.backend.auth.command = "echo $HIMALAYA_OUTLOOK_PASSWORD"
```

### Step 4: Save Your App Password

```bash
export HIMALAYA_OUTLOOK_PASSWORD="your-app-password-here"
```

> Add this line to your `~/.zshrc` or `~/.bashrc` so it persists across terminal sessions.

### Verification

```bash
himalaya -a outlook folder list
# Should list: INBOX, Sent Items, Drafts, Archive, etc.
```

If this works, you're all set!

## Commands

All commands use `-a outlook` to target the Outlook account:

```bash
# List inbox
himalaya -a outlook envelope list

# More messages
himalaya -a outlook envelope list --page-size 25

# Unread only
himalaya -a outlook envelope list flag unseen

# Read a message
himalaya -a outlook message read <id>

# Reply to a message
himalaya -a outlook message reply <id> "Thanks, I'll follow up on this."

# Search by sender
himalaya -a outlook envelope list from manager@company.com

# Search by subject and date
himalaya -a outlook envelope list subject meeting and after 2025-01-01

# Archive
himalaya -a outlook message move Archive <id>

# List folders
himalaya -a outlook folder list
```

Always confirm the reply content with the user before sending.

## Troubleshooting

- **"Authentication failed"?** Your App Password is wrong or expired — generate a new one (see Step 2)
- **"Connection refused"?** IMAP may be disabled for your mailbox — ask your IT admin to enable IMAP in Exchange Admin Center
- **Can't find "App password" option?** Your organization may have disabled them — contact your IT admin
- **"himalaya: command not found"?** Run `brew install himalaya` and restart your terminal
- **"Account not found"?** Make sure `[accounts.outlook]` is in your config.toml
- **Wrong folder names?** Outlook uses "Sent Items" instead of "Sent" and "Deleted Items" instead of "Trash" — run `himalaya -a outlook folder list` to see exact names
