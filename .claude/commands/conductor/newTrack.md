---
name: Conductor New Track
description: Create a scoped development track with spec.md, plan.md, and cgx.md; use when starting new feature, bugfix, refactor, or docs work.
---

# Conductor New Track

Create a new development track (feature, bugfix, refactor, or docs).

**FIRST: Read all context files before doing anything else.**

Read these files NOW:
- conductor/product.md
- conductor/tech-stack.md
- conductor/workflow.md
- conductor/tracks.md
- conductor/code_styleguides/ (all files)

---

## Phase 0: Understand the WHY and WHAT (MANDATORY — Do NOT skip)

**Before creating any files, you MUST deeply understand what the user wants and why.** Do not generate a spec, plan, or track directory until this phase is complete.

### Step 1: Ask about the WHY
Focus on motivation and goals. Ask the user:
- "What problem are you trying to solve?" or "What's driving this change?"
- "Who benefits from this? What's the user impact?"
- "What happens if we don't do this?"

### Step 2: Ask about the WHAT
Focus on desired behavior and outcomes. Ask the user:
- "What should the user experience look like when this is done?"
- "What are the key behaviors or capabilities this needs to have?"
- "What's explicitly NOT in scope for this work?"
- "How will we know it's done? What does 'working' look like?"

### Step 3: Ask about constraints and priorities
- "Are there any deadlines or dependencies?"
- "Should this be a minimal first pass or a complete solution?"
- "Any existing patterns or decisions this must follow?"

**CRITICAL: Do NOT rush this phase.** A 5-minute conversation here prevents hours of rework later. If the user gives a vague one-liner like "add notifications" or "fix the dashboard", you MUST ask follow-up questions — do not guess and start generating files.

---

## Phase 1: Research the HOW (MANDATORY — Do NOT skip)

**After understanding what the user wants, YOU must research the codebase to figure out HOW to implement it.** The user should not have to explain the technical approach — that's your job.

### Step 4: Explore the codebase
Based on the user's requirements, systematically research:
- **Existing patterns:** Search for similar features already implemented. How do they work?
- **Entry points:** Where does this change need to hook in? Which files, routes, components?
- **Data flow:** How does data move through the system for related features?
- **Test patterns:** How are similar features tested?
- **Dependencies:** What packages, services, or APIs are involved?

Use Glob, Grep, and Read tools aggressively. Look at:
- Related route handlers, components, or services
- Existing test files for similar functionality
- Type definitions and interfaces involved
- Configuration files that may need changes

### Step 5: Present your proposed approach
After researching, present to the user:
1. **Summary of findings:** "Here's what I found in the codebase..."
2. **Proposed approach:** "Based on the existing patterns, here's how I'd implement this..."
3. **Key files involved:** List the main files that will be created or modified
4. **Estimated scope:** How many phases, rough task count
5. **Risks or trade-offs:** Anything the user should know

Ask: "Does this approach make sense? Anything you'd change?"

### Step 6: Get explicit approval to proceed
Only after the user confirms both the WHAT and the HOW, move to track creation below.

---

## Phase 2: Create the Track

### 1. Determine Track Type
- feature: New functionality
- bugfix: Fix existing behavior
- refactor: Improve code without changing behavior
- docs: Documentation updates

### 2. Create spec.md
Generate specification based on the discovery conversation:
- Overview (the WHY)
- Requirements (the WHAT — numbered list)
- Technical approach (the HOW — key decisions from research)
- Acceptance criteria (how we know it's done)
- Out of scope (explicitly excluded)

**Show spec to user and get approval before proceeding.**

### 3. Create plan.md
Generate implementation plan informed by codebase research:
- Phases (logical groupings based on actual code structure)
- Tasks within each phase (referencing specific files and patterns found)
- Each task marked `[ ]` (pending)
- Tasks should be concrete and actionable (not vague)

**Show plan to user and get approval before proceeding.**

### 4. Create Track via MCP Tool (Preferred)

**Use the `conductor_create_track` MCP tool** to create the track. This atomically creates all files (spec.md, plan.md, cgx.md, metadata.json) and updates tracks.md:

```
conductor_create_track({
  name: "<track-description>",
  type: "feature" | "bugfix" | "refactor" | "docs"
})
```

Then overwrite the generated spec.md and plan.md with the approved versions.

**Fallback (if MCP tool is unavailable):** POST to `/api/projects/:projectId/tracks/create` with `{ name, type }`.

**Manual fallback (last resort):** Create the track directory and files manually:
```
conductor/tracks/<track-id>/
├── spec.md
├── plan.md
├── cgx.md      # Conductor Growth Experience tracking
└── metadata.json
```

Then update tracks.md with this exact format:
```markdown
---

## [ ] Track: <Track Description>
*Link: [./conductor/tracks/<track-id>/](./conductor/tracks/<track-id>/)*

---
```

---

## Critical Rules

1. Always read conductor/ context files FIRST
2. **Phase 0 (WHY/WHAT) is mandatory** — never skip the user conversation
3. **Phase 1 (HOW) is mandatory** — always research the codebase before proposing a plan
4. Get user approval on both the approach AND the spec/plan before creating files
5. **Prefer MCP tools** (`conductor_create_track`) for track creation — ensures atomic writes and canonical format
6. Plans should reference specific files and patterns discovered during research
7. If the user's request is unclear, keep asking questions until you understand
