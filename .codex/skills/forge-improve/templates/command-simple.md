# Command Template: Simple Slash Command

Use this template for straightforward workflows that users invoke manually.

## File Location

Single file at:

- Global: `~/.claude/commands/<name>.md`
- Project: `.claude/commands/<name>.md`
- Namespaced: `.claude/commands/<namespace>/<name>.md` → `/namespace:name`

## Template

```markdown
# <Command Name>

<Brief description of what this command does.>

## Arguments

`/command-name [arg1] [--flag]`

- `arg1`: <description>
- `--flag`: <description>

## Steps

1. **<Step Name>**
   - <Action to take>
   - <Action to take>

2. **<Step Name>**
   - <Action to take>

3. **<Step Name>**
   - <Action to take>

## Checklist

- [ ] <Item 1>
- [ ] <Item 2>
- [ ] <Item 3>

## Example Usage

<Describe a typical use case>

## Notes

- <Important note 1>
- <Important note 2>
```

## When to Use Commands vs Skills

**Use a Command when:**

- User wants explicit control over when it runs
- Workflow is a checklist or procedure
- No automation scripts needed
- Low frequency use (occasional)

**Use a Skill instead when:**

- Claude should auto-invoke based on context
- Automation (bash scripts) would help
- Complex multi-step process with tooling
- Frequently needed capability

## Command Examples

### Deployment Checklist

```markdown
# Deploy Checklist

Pre-deployment verification steps.

## Steps

1. **Run Tests**
   - `npm test`
   - All tests must pass

2. **Check Build**
   - `npm run build`
   - No TypeScript errors

3. **Review Changes**
   - `git diff main`
   - Confirm all changes are intentional

4. **Deploy**
   - `npm run deploy`
   - Verify deployment succeeded

## Post-Deploy

- [ ] Check monitoring dashboard
- [ ] Verify key flows work
- [ ] Update status page if needed
```

### Code Review Command

```markdown
# Code Review

Review code changes systematically.

## Arguments

`/review [file-or-pr]`

## Review Checklist

1. **Correctness**
   - Does the code do what it's supposed to?
   - Are edge cases handled?

2. **Security**
   - Any injection vulnerabilities?
   - Secrets properly handled?

3. **Performance**
   - Any obvious inefficiencies?
   - Database queries optimized?

4. **Maintainability**
   - Is the code readable?
   - Are names clear?
   - Is there adequate documentation?

## Output

Provide review in this format:

### Summary

<Overall assessment>

### Issues Found

- [ ] <Issue 1>
- [ ] <Issue 2>

### Suggestions

- <Suggestion 1>
- <Suggestion 2>
```
