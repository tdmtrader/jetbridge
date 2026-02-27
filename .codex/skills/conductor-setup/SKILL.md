---
name: conductor-setup
description: Initialize Conductor context for this project by creating required workflow files; use before creating tracks.. Use when the user asks about conductor setup or mentions $conductor-setup.
---

# Conductor Setup

Initialize the Conductor workflow for this project.

**FIRST: Check if conductor/ directory already exists.**

If conductor/ exists:
- Read conductor/setup_state.json to check initialization status
- If already initialized, inform user and ask if they want to reconfigure

If conductor/ does not exist, guide the user through setup:

## Phase 1: Project Scaffolding

### 1. Analyze Project Type
- Check for existing code (Brownfield vs Greenfield)
- Detect tech stack from package.json, go.mod, etc.
- If Brownfield: analyze existing codebase (README, manifests, directory structure)
- If Greenfield: ask "What do you want to build?"

### 2. Create Directory Structure
```
conductor/
├── product.md
├── product-guidelines.md
├── tech-stack.md
├── workflow.md
├── tracks.md
├── code_styleguides/
│   └── README.md
└── setup_state.json
```

### 3. Guide User Through Each File

Generate each file interactively:

1. **product.md** - Ask about target users, goals, features. Generate and get approval.
2. **product-guidelines.md** - Ask about prose style, brand messaging. Generate and get approval.
3. **tech-stack.md** - For brownfield: document detected stack. For greenfield: ask about languages, frameworks, databases. Generate and get approval.
4. **code_styleguides/** - Select appropriate style guides based on tech stack.
5. **workflow.md** - Set up development workflow (test coverage, commit strategy, etc.).

After each file is written, update `conductor/setup_state.json` with `last_successful_step`.

### 4. Save State
- Write setup_state.json with completion status after each step

---

## Phase 2: Initial Track Generation

**CRITICAL: After completing Phase 1, you MUST proceed to create the first track. Do NOT stop after scaffolding.**

### 5. Generate Product Requirements (Greenfield only)
- Read conductor/product.md to understand the project concept
- Ask up to 5 questions about user stories, functional/non-functional requirements
- Gather enough information to define the initial track

### 6. Propose a Single Initial Track
- Analyze project context (product.md, tech-stack.md)
- Propose a single track title summarizing the initial work
  - For Greenfield: usually an MVP track
  - For Brownfield: usually a maintenance or enhancement track
- Get user approval on the track

### 7. Create Track via MCP Tool (Preferred)

Once the track is approved, **use the `conductor_create_track` MCP tool** to create all track artifacts atomically:

```
conductor_create_track({
  name: "<Track description>",
  type: "feature" | "bugfix" | "refactor" | "docs"
})
```

This creates the track directory, metadata.json, spec.md, plan.md, cgx.md, and updates tracks.md in one atomic operation.

**Fallback (if MCP is unavailable):** POST to `/api/projects/:projectId/tracks/create` with `{ name, type }`.

**Manual fallback (last resort — only if both MCP and REST are down):**
Create `conductor/tracks/<track_id>/` with spec.md, plan.md, cgx.md, metadata.json, then update tracks.md.

### 8. Finalize Setup
1. Update setup_state.json: `{"last_successful_step": "3.3_initial_track_generated"}`
2. Git commit all conductor files: `conductor(setup): Add conductor setup files`
3. Announce completion and inform user they can start with `/conductor:implement`

---

## Critical Rules

1. Always read conductor/ context files FIRST
2. Follow workflow.md EXACTLY as written
3. Get user approval before making changes
4. **NEVER skip Phase 2** — the first track MUST be created as part of setup
5. **Prefer MCP tools** (`conductor_create_track`) for track creation — ensures atomic writes and canonical format
