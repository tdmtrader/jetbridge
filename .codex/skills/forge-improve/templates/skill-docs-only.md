# Skill Template: Documentation/Knowledge Base

Use this template when creating a skill that provides reference information or procedural knowledge without automation scripts.

## Directory Structure

```
.claude/skills/<skill-name>/
├── SKILL.md          # Main instructions
├── reference.md      # Optional: detailed reference (lazy-loaded)
└── examples.md       # Optional: extended examples
```

## SKILL.md Template

```yaml
---
name: <skill-name>
description: <What this knowledge helps with. Include keywords for auto-invocation.>
---

# <Skill Name>

<Brief description of what this skill provides.>

## When This Applies

Claude should use this skill when:
- <Condition 1>
- <Condition 2>
- User asks about <topic>

## Quick Reference

### <Category 1>

| Item | Description |
|------|-------------|
| `term1` | Explanation |
| `term2` | Explanation |

### <Category 2>

- **Concept A**: Explanation
- **Concept B**: Explanation

## Guidelines

### <Guideline Category 1>

1. <Step or rule 1>
2. <Step or rule 2>
3. <Step or rule 3>

### <Guideline Category 2>

- <Rule 1>
- <Rule 2>

## Common Patterns

### Pattern: <Name>

**When to use:** <Condition>

**How:**
```

<Example code or structure>
```

### Pattern: <Name>

**When to use:** <Condition>

**How:**

```
<Example code or structure>
```

## Anti-Patterns (Avoid)

- **<Anti-pattern 1>**: <Why it's bad>
- **<Anti-pattern 2>**: <Why it's bad>

## Examples

### Example 1: <Scenario>

<Description>

```
<Code or content>
```

### Example 2: <Scenario>

<Description>

```
<Code or content>
```

## Additional Resources

For detailed reference, see [reference.md](reference.md)
For more examples, see [examples.md](examples.md)

```

## When to Use This Template

Choose this template when:
- The skill is primarily about knowledge transfer
- Claude needs to know HOW to do something, not automate it
- The content is reference material or checklists
- No external API calls or file parsing needed

Examples:
- Architecture guidelines
- Code review checklist
- Domain-specific terminology
- Best practices documentation
```
