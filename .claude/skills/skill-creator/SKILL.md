---
name: skill-creator
description: This skill should be used when the user asks to "create a skill", "write a new skill", "add a custom skill", "make a skill for my project", or needs guidance on skill structure, SKILL.md format, frontmatter metadata, or progressive disclosure for Conductor project skills.
---

# Skill Creator for Conductor Projects

## Overview

Skills are modular, self-contained packages that extend AI agent capabilities by providing specialized knowledge, workflows, and tools. This skill guides the creation of custom skills for Conductor projects.

Skills are stored in `.conductor/skills/<skill-name>/SKILL.md` and automatically synced to all TUI engine directories (`.claude/skills/`, `.gemini/skills/`, `.codex/skills/`, `.cursor/skills/`).

## Skill Structure

Every skill consists of a required SKILL.md file and optional bundled resources:

```
.conductor/skills/<skill-name>/
├── SKILL.md (required)
│   ├── YAML frontmatter metadata (required)
│   │   ├── name: (required)
│   │   └── description: (required)
│   └── Markdown instructions (required)
└── Bundled Resources (optional)
    ├── scripts/          - Executable code (Python/Bash/etc.)
    ├── references/       - Documentation loaded into context as needed
    └── assets/           - Files used in output (templates, icons, fonts)
```

## SKILL.md Format

### Frontmatter (Required)

```yaml
---
name: my-skill
description: This skill should be used when the user asks to "specific phrase 1", "specific phrase 2". Be specific about triggers.
---
```

**Description quality matters:** The `description` determines when the AI will use the skill. Include specific trigger phrases users would say. Use third-person format.

### Body (Required)

Write using imperative/infinitive form (verb-first instructions), not second person:

```markdown
# Skill Title

## When to use this skill

[Describe scenarios]

## How to use this skill

[Step-by-step instructions]
```

## Progressive Disclosure

Skills use a three-level loading system for context efficiency:

1. **Metadata (name + description)** — Always in context (~100 words)
2. **SKILL.md body** — When skill triggers (<5k words)
3. **Bundled resources** — As needed by the agent (unlimited)

### What Goes Where

| Location | Content | Size Target |
|----------|---------|-------------|
| SKILL.md | Core concepts, essential procedures, quick reference | 1,500-2,000 words |
| references/ | Detailed patterns, API docs, edge cases | 2,000-5,000+ words each |
| scripts/ | Validation tools, automation, utilities | Executable files |
| assets/ | Templates, images, boilerplate | Output files |

## Skill Creation Process

### Step 1: Understand the Skill

Identify concrete examples of how the skill will be used:
- What functionality should it support?
- What would a user say that should trigger this skill?
- What tasks will it help accomplish?

### Step 2: Plan Reusable Resources

For each use case, identify:
- What scripts would be helpful to avoid rewriting code?
- What reference docs would provide domain knowledge?
- What templates or assets would be useful?

### Step 3: Create the Structure

```bash
mkdir -p .conductor/skills/<skill-name>/{references,scripts}
```

### Step 4: Write SKILL.md

1. Write frontmatter with specific trigger phrases
2. Write lean body (1,500-2,000 words) in imperative form
3. Reference supporting files so the agent knows they exist

### Step 5: Add Resources

- `scripts/` — Executable utilities for common operations
- `references/` — Detailed docs, schemas, API references
- `assets/` — Templates, boilerplate, sample files

### Step 6: Validate

- [ ] SKILL.md has valid YAML frontmatter with name and description
- [ ] Description uses third person and includes trigger phrases
- [ ] Body uses imperative/infinitive form
- [ ] Body is lean (1,500-2,000 words), details in references/
- [ ] All referenced files exist
- [ ] Scripts are executable

## Writing Style

### Frontmatter Description

Use third-person with specific triggers:

```yaml
# Good
description: This skill should be used when the user asks to "review finances", "check budget", "track spending".

# Bad
description: Use this skill for financial tracking.
```

### Body Content

Use imperative/infinitive form:

```markdown
# Good
Parse the configuration file. Validate inputs before processing.

# Bad
You should parse the configuration file. You need to validate inputs.
```

## Conductor Integration

After creating a skill:

1. **Sync to engines** — Skills in `.conductor/skills/` are automatically synced to `.claude/skills/`, `.gemini/skills/`, `.codex/skills/`, and `.cursor/skills/`
2. **Add to template shortcuts** — If the skill should appear as a quick-action button, add it to `template-config.json` shortcuts
3. **Schedule reminders** — Skills can be scheduled to run at intervals (daily, weekly, monthly) via the persistent track schedule UI
4. **Reference in other skills** — Skills can suggest running other skills (e.g., "Use `/mcp-integration` to connect your data source")

## Common Patterns

### Data-Aware Skills

Skills that read `conductor/tech-stack.md` to adapt behavior:

```markdown
## Steps
1. Read \`conductor/tech-stack.md\` to identify configured tools
2. If Google Sheets is configured, use the Google Sheets MCP tools
3. If no data source configured, suggest \`/mcp-integration\`
```

### Track-Creating Skills (spawnsTrack)

Skills that create new tracks for structured work:

```markdown
## Steps
1. Gather requirements from user
2. Create a new track: "Post: [Title]"
3. Define phases: Outline → Draft → Edit → Publish
```

### Review/Check-in Skills

Skills for periodic review with data aggregation:

```markdown
## Steps
1. Read from persistent track "Daily Log" for recent entries
2. Aggregate and summarize data
3. Identify trends and suggest adjustments
```

## Quick Reference

| Skill Type | SKILL.md Focus | Resources |
|------------|---------------|-----------|
| Simple knowledge | Core info only | None needed |
| Standard workflow | Procedures + references | references/ for details |
| Data-aware | Tool-specific instructions | references/ for tool guides |
| Complex automation | Workflow + scripts | scripts/ + references/ |

## Best Practices

- Keep SKILL.md under 2,000 words — move details to references/
- Include specific trigger phrases in description
- Reference `conductor/tech-stack.md` for tool-aware behavior
- Suggest `/mcp-integration` when external services are needed
- Use the `/playground` skill for visual configuration tasks
