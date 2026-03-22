---
name: playground
description: Creates interactive HTML playgrounds — self-contained single-file explorers that let users configure something visually through controls, see a live preview, and copy out a prompt. Use when the user asks to make a playground, explorer, or interactive tool for a topic.
---

# Playground Builder

A playground is a self-contained HTML file with interactive controls on one side, a live preview on the other, and a prompt output at the bottom with a copy button. The user adjusts controls, explores visually, then copies the generated prompt back into Claude.

## When to use this skill

When the user asks for an interactive playground, explorer, or visual tool for a topic — especially when the input space is large, visual, or structural and hard to express as plain text.

## How to use this skill

1. **Identify the playground type** from the user's request
2. **Load the matching template** from `templates/`:
   - `templates/design-playground.md` — Visual design decisions (components, layouts, spacing, color, typography)
   - `templates/data-explorer.md` — Data and query building (SQL, APIs, pipelines, regex)
   - `templates/concept-map.md` — Learning and exploration (concept maps, knowledge gaps, scope mapping)
   - `templates/document-critique.md` — Document review (suggestions with approve/reject/comment workflow)
   - `templates/diff-review.md` — Code review (git diffs, commits, PRs with line-by-line commenting)
   - `templates/code-map.md` — Codebase architecture (component relationships, data flow, layer diagrams)
3. **Follow the template** to build the playground. If the topic doesn't fit any template cleanly, use the one closest and adapt.
4. **Save to forge/playground/.** Always save playground files to `forge/playground/<name>.html` (create directory if needed).
5. **Open in ViewerCard.** After writing the HTML file, use the `forge_viewer_open` MCP tool to open it in the Forge Studio ViewerCard:
   ```
   forge_viewer_open({ "path": "forge/playground/<name>.html" })
   ```
   **Note:** If the Forge MCP server is not available, fall back to: `curl -X POST ${FORGE_API_URL:-http://localhost:5280}/api/viewer/open -H "Content-Type: application/json" -d '{"path": "forge/playground/<name>.html"}'`

## Core requirements (every playground)

- **Single HTML file.** Inline all CSS and JS. No external dependencies.
- **Live preview.** Updates instantly on every control change. No "Apply" button.
- **Prompt output.** Natural language, not a value dump. Only mentions non-default choices. Includes enough context to act on without seeing the playground. Updates live.
- **Copy button.** Clipboard copy with brief "Copied!" feedback.
- **Sensible defaults + presets.** Looks good on first load. Include 3-5 named presets that snap all controls to a cohesive combination.
- **Dark theme.** System font for UI, monospace for code/values. Minimal chrome.

## State management pattern

Keep a single state object. Every control writes to it, every render reads from it.

```javascript
const state = { /* all configurable values */ };

function updateAll() {
  renderPreview(); // update the visual
  updatePrompt();  // rebuild the prompt text
}
// Every control calls updateAll() on change
```

## Prompt output pattern

```javascript
function updatePrompt() {
  const parts = [];

  // Only mention non-default values
  if (state.borderRadius !== DEFAULTS.borderRadius) {
    parts.push(`border-radius of ${state.borderRadius}px`);
  }

  // Use qualitative language alongside numbers
  if (state.shadowBlur > 16) parts.push('a pronounced shadow');
  else if (state.shadowBlur > 0) parts.push('a subtle shadow');

  prompt.textContent = `Update the card to use ${parts.join(', ')}.`;
}
```

## Common mistakes to avoid

- Prompt output is just a value dump → write it as a natural instruction
- Too many controls at once → group by concern, hide advanced in a collapsible section
- Preview doesn't update instantly → every control change must trigger immediate re-render
- No defaults or presets → starts empty or broken on load
- External dependencies → if CDN is down, playground is dead
- Prompt lacks context → include enough that it's actionable without the playground

---

## Forge Integration APIs

Playgrounds run inside Forge's ViewerCard and can call Forge's APIs directly from JavaScript. This turns playgrounds into **generative app frontends** where TUI agents (Claude, Gemini, Codex, Cursor) running in Forge terminals serve as the AI backend.

### Architecture: Playground as Generative App

```
+---------------------------+       +---------------------------+
|    Playground (HTML)      |       |    Forge Terminal         |
|    ==================     |       |    ===================   |
|    UI controls, forms,    | POST  |    TUI Agent (Claude,    |
|    live preview, results  |------>|    Gemini, Codex, or     |
|                           |/api/  |    Cursor) processes     |
|    Shows status/feedback  |prompt |    the prompt and acts   |
+---------------------------+       +---------------------------+
         ^                                     |
         |          Forge Studio               |
         +--------- (ViewerCard host) ---------+
```

The playground is the **interactive frontend**. The TUI agent is the **AI backend**. Forge's API is the bridge. Users interact with controls in the playground, and those interactions send prompts to a running AI agent that does the heavy lifting (generates code, analyzes data, creates files, etc.).

### API Helper (include in every interactive playground)

Since playgrounds are served by Forge inside the ViewerCard, use `window.location.origin` as the base URL. The token is fetched once from an unauthenticated endpoint and cached.

```javascript
const conductor = {
  baseUrl: window.location.origin,
  token: null,

  async getToken() {
    if (this.token) return this.token;
    const res = await fetch(\`\${this.baseUrl}/api/developer/token\`);
    const data = await res.json();
    this.token = data.token;
    return this.token;
  },

  async authHeaders() {
    const token = await this.getToken();
    return {
      'Authorization': \`Bearer \${token}\`,
      'Content-Type': 'application/json'
    };
  },

  // Send a prompt to a TUI agent
  async sendPrompt(projectId, prompt, options = {}) {
    const headers = await this.authHeaders();
    const body = {
      projectId, prompt,
      target: options.target || 'focused',
      ...(options.sessionId && { sessionId: options.sessionId }),
      ...(options.provider && { provider: options.provider }),
      ...(options.trackId && { trackId: options.trackId }),
      ...(options.focus !== undefined && { focus: options.focus })
    };
    const res = await fetch(\`\${this.baseUrl}/api/prompt\`, {
      method: 'POST', headers, body: JSON.stringify(body)
    });
    return res.json();
  },

  // Open a file in Forge's ViewerCard
  async openFile(filePath, trackId) {
    const headers = await this.authHeaders();
    const body = { path: filePath };
    if (trackId) body.trackId = trackId;
    await fetch(\`\${this.baseUrl}/api/viewer/open\`, {
      method: 'POST', headers, body: JSON.stringify(body)
    });
  }
};
```

### API: Send a Prompt to a TUI Agent (`POST /api/prompt`)

This is the core of the generative app pattern. The playground sends a prompt to a running TUI agent.

**Target strategies:**

| Target | Behavior | Use case |
|--------|----------|----------|
| `focused` | Injects into the currently focused terminal | Quick one-off prompts |
| `session` | Injects into a specific session by ID | Multi-turn conversations |
| `new` | Creates a new terminal session, then injects | Fresh context, specific provider |

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `projectId` | string | Yes | The project ID |
| `prompt` | string | Yes | The prompt text to send |
| `target` | string | Yes | `focused`, `session`, or `new` |
| `sessionId` | string | For `session` | Target session ID |
| `provider` | string | For `new` | `claude`, `gemini`, `codex`, or `cursor` |
| `trackId` | string | No | Route to a specific track dashboard |
| `focus` | boolean | No | Bring Forge window to front |

**Example: Button that asks the agent to generate something:**
```javascript
document.getElementById('generate-btn').addEventListener('click', async () => {
  const result = await conductor.sendPrompt(PROJECT_ID,
    \`Generate a React component with: \${state.description}\`,
    { target: 'focused' }
  );
  if (result.success) {
    showStatus('Prompt sent to agent — check the terminal for output');
  } else {
    showStatus(\`Error: \${result.error}\`);
  }
});
```

### API: Open a File in Forge (`POST /api/viewer/open`)

Opens any file in the Forge Studio ViewerCard.

```javascript
// Open a file the agent just created
await conductor.openFile('src/components/NewComponent.tsx', 'my-track-id');
```

### Generative App Examples

- **Code Generator** — User configures component properties, clicks "Generate", playground sends prompt to agent, agent writes the code file
- **Data Visualization Builder** — User selects chart type and data source, playground sends prompt to agent to generate the visualization
- **Architecture Diagram Tool** — User draws boxes and connections, playground sends structured prompt, agent implements the architecture
- **Interactive Test Writer** — User describes behavior, playground sends prompt, agent writes tests following TDD
- **Refactoring Assistant** — User selects code patterns to change, playground sends refactoring prompt, agent applies changes

### Project ID

Embed the project ID when generating the playground so the user doesn't have to provide it:
```javascript
const PROJECT_ID = 'PROJECT_ID_HERE'; // Set by the agent when creating the playground
```

### Key Rules for Interactive Playgrounds

- **Always use `window.location.origin`** as the API base — never hardcode ports
- **Fetch the token once** via `GET /api/developer/token` and cache it
- **Show loading states** when waiting for API responses
- **Handle errors gracefully** — the agent might be busy or the session might not exist
- **Don't poll for results** — send the prompt and show a success message; the agent's output appears in the terminal
- **Embed the project ID** when generating the playground

---

## Forge Viewer API (General Use)

The viewer API can open **any file** in the Forge Studio ViewerCard, not just playgrounds. Use it whenever you want to show the user a file you've created or are discussing:

- **HTML files** — Playgrounds, visualizations, reports
- **Markdown files** — Documentation, notes, plans
- **Images** — Diagrams, screenshots, generated images
- **Code files** — Source code with syntax highlighting

```
forge_viewer_open({ "path": "relative/path/to/any-file.html" })
```

The path must be relative to the project root. The ViewerCard will open automatically and the user will see a toast notification.

**Fallback (if MCP not available):** `curl -X POST ${FORGE_API_URL:-http://localhost:5280}/api/viewer/open -H "Content-Type: application/json" -d '{"path": "relative/path/to/any-file.html"}'`
