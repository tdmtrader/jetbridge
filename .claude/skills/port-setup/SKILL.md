---
name: port-setup
description: Analyzes a project's codebase to detect services and their ports, then generates a .forge/ports.json config file so Forge can allocate non-conflicting ports across worktrees.
---

# Port Setup

Analyze this project to identify all services that bind to network ports, detect their port configuration, and generate a `.forge/ports.json` file.

## When to use this skill

When a project needs port configuration for Forge worktree isolation. This is required before tracks can be isolated into separate worktrees with their own dev servers.

## Steps

### Step 1: Discover Services and Ports

Read the following files (if they exist) to identify services and their ports:

1. **`.env` and `.env.example`** — Look for env vars containing "PORT" (e.g., `PORT=3000`, `API_PORT=8080`, `VITE_PORT=5173`)
2. **`docker-compose.yml` / `docker-compose.yaml`** — Look for `ports:` mappings (e.g., `"3000:3000"`, `"8080:8080"`)
3. **`package.json` scripts** — Look for `--port` flags, `PORT=` assignments in scripts (e.g., `"dev": "vite --port 5173"`)
4. **`Procfile`** — Look for services with port bindings
5. **`Makefile`** — Look for port references in dev/start targets
6. **Framework config files** — `vite.config.ts`, `next.config.js`, `webpack.config.js`, `angular.json`, etc.

### Step 2: Identify Port Environment Variables

For each discovered service, determine:
- **Service name** (e.g., "api", "web", "db", "redis")
- **Default port** (the port used when no env var override is set)
- **Environment variable(s)** that control the port (e.g., `PORT`, `VITE_PORT`)
- **Computed env vars** (optional) — derived values like URLs that include the port (e.g., `DATABASE_URL=localhost:${port}`)

### Step 3: Scan for Hardcoded Ports

Search the codebase for hardcoded port values that should be parameterized:

```bash
# Search for common hardcoded port patterns
grep -rn "localhost:[0-9]\{4,5\}" --include="*.ts" --include="*.js" --include="*.tsx" --include="*.jsx" --include="*.json" --include="*.yaml" --include="*.yml" .
grep -rn "127\.0\.0\.1:[0-9]\{4,5\}" --include="*.ts" --include="*.js" .
grep -rn "0\.0\.0\.0:[0-9]\{4,5\}" --include="*.ts" --include="*.js" .
```

For each hardcoded port found, note:
- The file and line number
- The current hardcoded value
- Which service it belongs to
- The env var that should replace it

### Step 4: Generate `.forge/ports.json`

Create the file at `.forge/ports.json` with this schema:

```json
{
  "services": [
    {
      "name": "api",
      "defaultPort": 3000,
      "envVars": ["PORT", "API_PORT"],
      "computed": {
        "API_URL": "http://localhost:${port}"
      }
    },
    {
      "name": "web",
      "defaultPort": 5173,
      "envVars": ["VITE_PORT"]
    }
  ],
  "portSpacing": 10
}
```

**Schema reference:**
- `services` (required): Array of service definitions, each with:
  - `name` (string): Short identifier for the service (e.g., "api", "web", "db")
  - `defaultPort` (number): The port used in development by default
  - `envVars` (string[]): Environment variable name(s) that set this port. At least one required.
  - `computed` (object, optional): Key-value pairs where the key is an env var name and the value is a template string using \`${port}\` as a placeholder for the allocated port value
- `portSpacing` (number, optional): Minimum gap between worktree port allocations (default: 10). Each worktree gets ports offset by multiples of this value.

### Step 5: Recommend Codebase Changes

If you found hardcoded ports in Step 3, list specific changes the developer should make to parameterize them. For example:

- "In `src/server.ts:42`, replace `listen(3000)` with `listen(parseInt(process.env.PORT || '3000'))`"
- "In `vite.config.ts:8`, add `server: { port: parseInt(process.env.VITE_PORT || '5173') }`"

### Step 6: Verify Dev Command Compatibility

Verify that the project's dev/start command will pick up the environment variables:

1. Check if the main dev command reads the env vars you identified
2. If not, recommend how to modify the dev command
3. Test by running: `cat .forge/ports.json` to confirm the file was created correctly

## Output Format

Present your findings as:

1. **Discovered Services** — table of service name, default port, env vars
2. **Hardcoded Ports Found** — list of file:line with hardcoded values and recommended fixes
3. **Generated `.forge/ports.json`** — the file contents
4. **Recommended Changes** — specific code changes to parameterize hardcoded ports
5. **Verification** — confirmation that the config file is valid

## Important Notes

- Do NOT modify any source code automatically — only recommend changes
- The `.forge/ports.json` file is the only file you should create/modify
- If a service doesn't have an env var for its port, create a reasonable name (e.g., `MY_SERVICE_PORT`)
- Use `portSpacing: 10` as the default unless the project has more than 10 services
- Each service must have unique `name` and `defaultPort` values
