# Skill Authoring Guide for Templates

## SKILL.md Structure

```yaml
---
name: skill-id
description: This skill should be used when the user asks to "trigger phrase 1", "trigger phrase 2". Brief explanation of what it does.
---
```

## Writing Style

- Use imperative/infinitive form (verb-first)
- NOT: "You should check the email"
- YES: "Check all connected email accounts"

## Frontmatter Rules

- `name`: lowercase, hyphenated (e.g., "daily-briefing")
- `description`: Third-person with specific trigger phrases in quotes
- Keep description under 200 words

## Content Guidelines

- Target 1,500-2,000 words for SKILL.md body
- Start with a brief overview (2-3 sentences)
- Use ## headers for major sections
- Use ### for sub-sections
- Include step-by-step instructions
- Reference external tools/APIs by name

## Schedule Configuration

For recurring skills, create .schedule.json:

```json
{
  "cron": "0 8 * * *",
  "label": "Daily at 8am"
}
```

Common patterns:
- Daily: "0 8 * * *" (8am)
- Hourly: "0 * * * *"
- Every 15 min: "*/15 * * * *"
- Weekly: "0 9 * * 1" (Monday 9am)

## Progressive Disclosure

1. Metadata (name + description) — always in context
2. SKILL.md body — loaded when skill triggers
3. references/ — loaded on demand by the agent
