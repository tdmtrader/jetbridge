---
name: conductor:improve
description: Analyze conversation sessions for friction patterns and generate skill/command/agent improvements. Use when the user asks to improve the development experience, create new skills, or learn from past sessions.
argument-hint: '[session-id] [--aggregate]'
allowed-tools: Bash(*), Read, Grep, Glob, Write, Edit
---

# Conductor Improve

Analyze conversation sessions to generate improvements to skills, commands, or agents.

## Quick Reference

```bash
# Extract and analyze a session
~/.claude/skills/conductor-improve/scripts/extract-session.sh <session-id>

# List all existing extensions
~/.claude/skills/conductor-improve/scripts/list-extensions.sh

# Analyze friction patterns in extracted messages
~/.claude/skills/conductor-improve/scripts/analyze-friction.sh /tmp/session-messages.txt
```

---

## Understanding Skills vs Commands vs Agents

**CRITICAL**: Before proposing any extension, understand these differences:

### Slash Commands (`.claude/commands/name.md`)

- **Invocation**: Manual only via `/name`
- **Structure**: Single markdown file
- **Use case**: Simple, repeatable workflows triggered by user
- **Example**: `/commit`, `/pr`, `/test`

### Skills (`.claude/skills/name/SKILL.md`)

- **Invocation**: Manual via `/name` OR automatic (Claude decides when relevant)
- **Structure**: Directory with supporting files (scripts, templates, references)
- **Capabilities**:
  - Bash scripts for heavy lifting (parsing, API calls, file generation)
  - `context: fork` for isolated subagent execution
  - `allowed-tools` restrictions
  - Lazy-loaded reference files
  - Dynamic content injection with `!command` syntax (runs command and injects output)
- **Use case**: Complex workflows, automation, domain knowledge
- **Example**: Image generation, code analysis, build systems

### Agents (`.claude/commands/name.agent.md`)

- **Invocation**: Via Task tool delegation (Claude spawns them automatically)
- **Structure**: Single file with agent behavior definition + frontmatter
- **Key feature**: The `description` field determines when Claude spawns the agent
- **Use case**: Specialized personas, isolated context, different "mindset"
- **Example**: `tdd-engineer`, `security-reviewer`, `codebase-explorer`

**How Agents Get Spawned:**

```
1. Claude sees agent definitions in available agents list
2. Claude reads the description to understand when to use it
3. When a task matches the description, Claude calls Task tool:
   Task({ subagent_type: "agent-name", prompt: "..." })
4. Agent runs in isolation with its own context
5. Agent returns results to Claude
```

**Critical**: The description must be specific about WHEN to spawn. Vague descriptions = never used.

---

## Step 1: Extract Session Data

Use the helper script to extract and parse conversation data:

```bash
# For Claude sessions
~/.claude/skills/conductor-improve/scripts/extract-session.sh <session-id>

# Output: /tmp/session-analysis/
#   - user-messages.txt (clean user messages)
#   - assistant-excerpts.txt (key assistant responses)
#   - friction-patterns.txt (detected friction)
#   - tool-errors.txt (failed tool calls)
```

The script handles:

- JSONL parsing with jq
- Large file chunking (avoids memory issues)
- Multi-account session locations
- Friction keyword detection

---

## Step 2: Load Extension Inventory

```bash
~/.claude/skills/conductor-improve/scripts/list-extensions.sh
```

This outputs all existing extensions organized by scope and type, helping avoid duplicates.

---

## Step 3: Analyze Friction Patterns

The `analyze-friction.sh` script categorizes friction:

### High-Value Friction (prioritize these)

- **Repeated corrections**: User says "no", "actually", "that's not what I meant" multiple times
- **Tool failures**: Same tool call fails repeatedly
- **Workarounds**: User describes manual steps to work around limitations
- **Context loss**: User has to re-explain things after compaction

### Medium-Value Friction

- **Clarification requests**: User asks "what do you mean?" or "can you explain?"
- **Iteration loops**: Multiple attempts at the same task

### Low-Value Friction (may be situational)

- **Single corrections**: One-off misunderstandings
- **Exploration**: Natural back-and-forth during discovery

---

## Step 4: Generate Improvement Proposals

For each identified improvement, determine the RIGHT extension type:

### When to Create a SKILL (with scripts)

Create a full skill when:

- Task involves parsing files, calling APIs, or generating output
- Automation would save significant time
- Claude currently does the work "manually" in conversation
- Pattern appears across multiple sessions

**Example**: Session showed repeated JSONL parsing struggles → Create skill with jq-based extraction script

### When to Create a COMMAND

Create a simple command when:

- Task is a straightforward workflow checklist
- No complex automation needed
- User wants explicit control over invocation
- One-time or low-frequency use

**Example**: User wants a deployment checklist → Simple `/deploy-checklist` command

### When to Create an AGENT

Create an agent when:

- Task benefits from a specialized persona or "mindset"
- Isolated context would prevent confusion (agent doesn't see full conversation)
- Delegation via Task tool is natural ("go do this and report back")
- Different approach is needed (TDD, security-focused, exploration-mode)

**When NOT to create an agent:**

- Task is simple (just use a command)
- Would duplicate Claude's default behavior
- No specialized approach needed

**Example agents:**

- `tdd-engineer` - Strict test-first development (different workflow than default)
- `security-reviewer` - Thinks like an attacker (different mindset)
- `codebase-explorer` - Fast exploration with haiku model
- `docs-writer` - Clear, concise documentation (different output style)

**Writing Agent Descriptions (CRITICAL):**

The `description` field is how Claude decides when to spawn the agent. Be SPECIFIC:

```yaml
# GOOD - Specific triggers
description: Use for test-driven development tasks. Writes failing tests FIRST,
then implements minimum code to pass. Spawn when user asks to "add a feature
with tests", "implement using TDD", or wants test-first development.

# BAD - Too vague, Claude won't know when to use it
description: Helps with coding tasks
```

See `templates/agent-subagent.md` for full examples.

---

## Step 5: Proposal Format

For each proposal, provide:

```markdown
### Proposal: [Name]

**Type:** skill (with scripts) | skill (docs only) | command | agent
**Scope:** project | global
**Confidence:** [0-100]%
**Why this type:** [Explain why skill vs command vs agent]

**Friction Addressed:**

- [Specific excerpt from session]
- [Impact: how often this occurs, how much time wasted]

**Proposed Structure:**
```

.claude/skills/<name>/
├── SKILL.md
├── scripts/
│ └── helper.sh (if applicable)
└── templates/ (if applicable)

```

**SKILL.md Content:**
[Full content including frontmatter]

**Script Content (if applicable):**
[Full bash script]

**Similar Existing Extensions:**
- [List any that overlap]
```

---

## Step 6: User Approval

Present proposals and ask:

1. **Accept** - Create as proposed
2. **Accept with changes** - User specifies modifications
3. **Change type** - Convert skill↔command↔agent
4. **Change scope** - Project↔global
5. **Reject** - Skip
6. **Defer** - Save to `conductor/growth/deferred.md`

---

## Step 7: Create Extension

When creating a SKILL with scripts:

1. Create directory structure
2. Write SKILL.md with proper frontmatter
3. Write supporting scripts (make executable)
4. Test the scripts work
5. Log to `conductor/growth/improvements.md`

When creating a COMMAND:

1. Write single .md file
2. Log to improvements.md

---

## Frontmatter Reference

```yaml
---
name: skill-name # Becomes /skill-name
description: When to use... # Claude uses this for auto-invocation
argument-hint: '[args]' # Shown in autocomplete

# Advanced options
disable-model-invocation: true # Manual only (for /commit, /deploy)
user-invocable: false # Hide from menu (background knowledge)
context: fork # Run in isolated subagent
agent: Explore | Plan # Which subagent type
allowed-tools: Bash(*), Read # Tool restrictions
model: claude-opus-4 # Model override
---
```

---

## Templates

See the `templates/` directory for:

- `skill-with-script.md` - Full skill with bash automation
- `skill-docs-only.md` - Skill without scripts (knowledge base)
- `command-simple.md` - Simple slash command
- `agent-subagent.md` - **Subagent definition with description guidance**

### Agent Template Highlights

The `agent-subagent.md` template includes:

1. **How subagents work** - Task tool delegation flow
2. **Writing effective descriptions** - Good vs bad examples
3. **Real-world examples**:
   - `tdd-engineer` - Test-driven development specialist
   - `codebase-explorer` - Fast exploration with haiku model
   - `security-reviewer` - OWASP checklist, thinks like attacker
   - `docs-writer` - Clear, example-driven documentation
4. **When to use agents vs skills vs commands** - Decision guide

---

---

## Quick Decision Guide

```
Is automation needed (scripts, API calls)?
├── Yes → SKILL with scripts
└── No
    └── Does it need a different "mindset" or approach?
        ├── Yes → AGENT
        └── No
            └── Is it a simple checklist/procedure?
                ├── Yes → COMMAND
                └── No → SKILL (docs only)
```

| Pattern                             | Extension Type             |
| ----------------------------------- | -------------------------- |
| "Claude struggles with large files" | Skill with bash script     |
| "Need security-focused review"      | Agent (different mindset)  |
| "Want a deployment checklist"       | Command                    |
| "Need domain knowledge reference"   | Skill (docs only)          |
| "TDD workflow every time"           | Agent (different workflow) |
| "Parse API responses"               | Skill with script          |

---

## Recording Improvements via MCP Tools (Preferred)

**Always prefer MCP tools** over direct file editing for recording learnings, notes, and improvement proposals.

### Record a learning from session analysis:
```
conductor_add_learning({
  trackId: "my_track_20260214",
  category: "friction",
  content: "Agent struggled with large JSONL parsing — created skill with jq-based extraction"
})
```

### Check existing learnings before adding duplicates:
```
conductor_get_learnings({
  trackId: "my_track_20260214",
  category: "friction"
})
```

### Save an improvement proposal as a track note:
```
conductor_add_note({
  trackId: "my_track_20260214",
  name: "improvement-proposal-session-parser",
  content: "## Proposal: Session Parser Skill\n\n**Type:** skill (with scripts)\n..."
})
```

### List existing notes to avoid duplicates:
```
conductor_list_notes({ trackId: "my_track_20260214" })
```

### Save project-level improvement notes:
```
conductor_project_note({
  action: "write",
  name: "growth-deferred",
  content: "## Deferred Improvements\n\n- Session parser skill (low priority)\n..."
})
```

### Fallback: Direct File Editing
Only if MCP tools are unavailable, write directly to:
- `conductor/tracks/<trackId>/learnings.md` — for learnings
- `conductor/tracks/<trackId>/note-<name>.md` — for track notes
- `conductor/notes/<name>.md` — for project-level notes

---

## Critical Rules

1. **Skills > Commands** when automation is involved
2. **Agents** for specialized mindsets, not just tasks
3. **Agent descriptions must be specific** about spawn triggers
4. **Include bash scripts** when Claude currently does manual work
5. **Use `allowed-tools`** to restrict dangerous operations
6. **Use `context: fork`** for analysis/exploration tasks
7. **Test scripts** before marking proposal complete
8. **Log all improvements** for audit trail
9. **Check existing extensions** before creating duplicates
