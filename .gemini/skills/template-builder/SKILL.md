---
name: template-builder
description: This skill should be used when the user asks to "create a template", "build a template from this project", "export this as a template", "make this project reusable", or "turn this into a starter template". Guides through analyzing the project, interviewing the user, and generating a polished, marketplace-ready template.
---

# Template Builder

Create professional, reusable templates from existing projects. Analyze what makes the project special, interview the user about their workflow, and generate a polished template with setup wizard, skills, tracks, and MCP recommendations.

## When to Use

- User wants to create a template from their current project
- User wants to package their workflow for others to reuse
- User wants to improve or enhance an existing template

## Phase 1: Project Analysis

Analyze the current project to understand what's available for templating.

### Steps

1. Identify the current project ID from the Forge context
2. Call the analyze API to get structured project data:

```
POST /api/projects/{projectId}/analyze-for-template
```

3. Present the analysis summary to the user:
   - **Skills found:** List each skill with schedule/files indicators
   - **Persistent tracks:** List with spec/plan status
   - **MCP servers:** List with transport type
   - **Shortcuts:** List configured control panel buttons
   - **Root instructions:** Which engine files exist
   - **Setup wizard:** Whether a setup skill is configured

4. Ask: "This is what I found in your project. Which components should be included in the template?"

## Phase 2: User Interview

Gather context about the template's purpose and audience through conversation.

### Questions to Ask

1. **Purpose:** "What problem does this template solve? Who is it for?"
2. **Name & Description:** "What should this template be called? Write a one-line description."
3. **Category:** "Which category fits best: software, business, creative, personal, health, or finance?"
4. **Setup Flow:** "When someone uses this template, what should the setup wizard walk them through? What questions should it ask?"
5. **Skill Curation:** "Which of the discovered skills are essential vs optional? Any skills that should be created new?"
6. **Daily Workflow:** "What does a typical day look like using this project? What tasks are recurring?"
7. **MCP Requirements:** "Which external tools or services does this template need? Any API keys or credentials?"

### Capture Decisions

Record the user's answers as structured data for generation:
- Template name, description, category
- Skills to include (with reasons)
- Setup wizard phases (ordered list with descriptions)
- Persistent tracks to create
- MCP server recommendations
- Shortcut configuration

## Phase 3: Template Design

Based on analysis + interview, design the template structure.

### Design Decisions

1. **Setup Wizard Skill:** Design multi-phase conversational setup:
   - Phase 1: Discovery questions (pain points, goals)
   - Phase 2: Tool inventory (what services to connect)
   - Phase 3: Tool checks (verify CLI tools installed)
   - Phase 4: Authentication (API keys, OAuth, credentials)
   - Phase 5: Preferences (working hours, priorities, rules)
   - Phase 6+: Generate project files based on answers

2. **Skill Selection:** From the project analysis:
   - Include skills the user confirmed as essential
   - Adjust schedule frequencies based on user's daily workflow
   - Add descriptions if missing

3. **Persistent Tracks:** Design tracks that never complete:
   - Include spec.md content describing the track's purpose
   - Include plan.md with recurring task structure
   - Associate relevant skills with each track

4. **Shortcuts:** Design control panel layout (max 8):
   - Map most-used skills/commands to buttons
   - Assign intuitive icons and labels

5. **MCP Servers:** Recommend external tool integrations:
   - Include transport type (stdio/http)
   - Document required environment variables or credentials

### Present Design to User

Show the complete template design in a structured format:

```
Template: {name}
Category: {category}
Description: {description}

Setup Wizard: {phase_count} phases
Skills: {skill_count} ({essential_count} essential, {optional_count} optional)
Tracks: {track_count} persistent tracks
MCP Servers: {mcp_count} recommended
Shortcuts: {shortcut_count} control panel buttons
Root Instructions: {instruction_count} engine files
```

Ask: "Does this look right? Any changes before I generate the content?"

## Phase 4: Content Generation

Generate all template content using the design decisions.

### Generate Setup Wizard Skill

Write a SKILL.md for the setup wizard following this pattern:

```yaml
---
name: {template-slug}-setup
description: Set up a new {template-name} project by configuring tools, preferences, and generating project files.
---
```

The body should contain conversational phases with clear instructions for the agent. Each phase header (`## Phase Name`) defines a stage of the setup conversation.

### Generate Skill Content

For each included skill, ensure it has:
- Complete SKILL.md with proper frontmatter (name, description with trigger phrases)
- Schedule configuration (.schedule.json) if recurring
- Reference files if the skill needs detailed documentation

### Generate Track Content

For each persistent track, generate:
- spec.md describing the track's purpose and recurring workflow
- plan.md with a repeatable task structure

### Build ConductorTemplate Object

Assemble all generated content into a ConductorTemplate:
- skills array with full content
- shortcuts array with labels and icons
- persistentTracks array with spec/plan content
- mcpServers array with transport config
- rootInstructions map with engine file content
- setupSkillId pointing to the generated setup skill

## Phase 5: Validation & Preview

Before saving, validate the template.

### Validation Checks

1. Every skill has valid YAML frontmatter (name + description)
2. Setup wizard skill has at least 2 phases
3. Skill descriptions contain specific trigger phrases
4. No duplicate skill IDs
5. Shortcuts reference valid command IDs (max 8)
6. MCP server names are unique

### Preview

Show the user a section-by-section preview:
- Setup wizard phases with names
- Skill list with schedules
- Track list with associated skills
- MCP server list
- Shortcut layout

Ask: "Everything looks good. Ready to save this template?"

## Phase 6: Save & Publish

Create the template via the user-templates API.

### Save Steps

1. Create the template:

```
POST /api/user-templates
Body: { name, description, category }
```

2. Update with full template data:

```
PUT /api/user-templates/{id}
Body: { template: { ...conductorTemplate } }
```

3. Confirm success: "Template '{name}' created successfully! You can find it in your Template Library."

### Next Steps

Suggest to the user:
- "Use this template to create a new project from the Template Library"
- "Share the template by exporting it (coming in a future update)"
- "Edit the template anytime from the Template Library page"

## Enhancement Mode

When improving an existing template rather than creating a new one:

1. **Load the existing template:** `GET /api/user-templates/{id}`
2. **Run gap analysis** using the checklist below
3. **Present improvement suggestions** to the user with priority ranking
4. **Generate missing content** for approved improvements
5. **Apply updates** via `PUT /api/user-templates/{id}`

### Gap Analysis Checklist

Evaluate the template against these criteria and report findings:

1. **Setup Wizard** — Does the template have a `setupSkillId`? If not, suggest creating a setup skill with at least 3 phases (discovery, tool inventory, preferences).
2. **Skill Coverage** — Are there at least 3 domain skills? Do all skills have trigger phrases in their descriptions? Do recurring skills have `.schedule.json`?
3. **Persistent Tracks** — Is there at least one persistent track with spec.md and plan.md content? Are skills associated with tracks?
4. **Control Panel** — Are there 4-8 shortcuts? Do they map to the most-used skills?
5. **MCP Servers** — Are required external tools documented with transport type and credentials?
6. **Root Instructions** — Do CLAUDE.md / GEMINI.md files exist with project-specific guidance?
7. **Descriptions** — Is the template description clear and specific? Do all skills have meaningful descriptions?
8. **Category** — Is the category accurate for the template's purpose?

### Enhancement Generation

For each approved gap, generate the missing content:

- **Missing setup wizard:** Create a new skill with conversational phases following the patterns in `references/setup-wizard-patterns.md`
- **Few skills:** Propose new skills based on the template's category and purpose, following `references/skill-authoring-guide.md`
- **No persistent tracks:** Suggest recurring workflows based on the skills present
- **Sparse descriptions:** Rewrite descriptions with specific trigger phrases and clear explanations
- **Missing shortcuts:** Propose shortcuts for the most-used skills with Lucide icon names
