# What Makes a Great Template

## Essential Components

### 1. Setup Wizard (Most Important)
A conversational skill that guides new users through project setup. Should:
- Start with discovery (pain points, goals)
- Inventory connected tools and services
- Verify tool installation and authentication
- Collect preferences (schedule, priorities)
- Generate project configuration files

### 2. Domain Skills (3-8 recommended)
Skills that define the daily workflow:
- Each skill should have a clear trigger phrase in the description
- Recurring skills need .schedule.json (daily, hourly, etc.)
- Skills should update shared state (e.g., dashboard data)

### 3. Persistent Tracks (1-3 recommended)
Tracks that never complete — recurring workflows:
- Include spec.md explaining the track's purpose
- Include plan.md with repeatable tasks
- Associate relevant skills

### 4. Control Panel Shortcuts (4-8 recommended)
Quick-access buttons for the most common actions:
- Map to the most-used skills and commands
- Use intuitive Lucide icon names
- Order by frequency of use

### 5. MCP Server Recommendations
External tools the template needs:
- Include transport type (stdio for local, http for remote)
- Document required credentials
- Note which skills depend on which servers

### 6. Conductor Files
Project configuration files stored in the `conductorFiles` map:
- product.md — project vision and context
- tech-stack.md — technology decisions
- workflow.md — development process
- Custom files specific to the template's domain

### 7. Setup Skill ID
The `setupSkillId` field links the template to its setup wizard skill. When a user creates a project from this template, the setup skill is invoked automatically to guide initial configuration.

## Quality Checklist

- [ ] Template has a clear, descriptive name
- [ ] Description explains the problem it solves in one sentence
- [ ] setupSkillId points to a valid setup wizard skill
- [ ] Setup wizard has 3+ phases
- [ ] Each skill has trigger phrases in description
- [ ] Recurring skills have schedules
- [ ] At least one persistent track exists
- [ ] Shortcuts provide quick access to daily workflow
- [ ] conductorFiles include at minimum product.md
- [ ] Category is accurate
