# Product Guidelines

## Prose & Documentation Style

### Tone
- **Technical and precise** — Write for experienced Go developers and Kubernetes operators.
- **Detailed with rationale** — Explain the "why" behind design decisions, not just the "what."
- **Practical** — Include examples, configuration snippets, and concrete behavior descriptions.

### Formatting
- Use Markdown for all documentation.
- Prefer tables for feature comparisons and status tracking.
- Use code blocks with language hints for all code examples.
- Keep paragraphs focused — one idea per paragraph.

### Naming Conventions
- **JetBridge** — The name for the Kubernetes runtime layer within this fork.
- **K8s runtime** — Acceptable shorthand in technical docs.
- **Legacy runtime** — Refers to Garden + Baggageclaim + TSA (now removed).
- **Agent step** — The pipeline primitive for AI agent execution. Always "agent step", not "AI step" or "LLM step".
- **Agent-first workflow** — A pipeline that uses agent steps as primary actors. The DAG still governs structure; agents have autonomy within their step boundaries.
- **Tool** — A capability an agent can invoke (shell, API, MCP server). Distinct from Concourse "resources".
- **MCP (Model Context Protocol)** — The protocol agents use to connect to external tool servers.
- Use Kubernetes API resource names precisely (Pod, PersistentVolumeClaim, ServiceAccount, etc.).

### Commit Messages
- Follow conventional commits: `type(scope): description`
- Types: `feat`, `fix`, `refactor`, `test`, `chore`, `docs`
- Scopes: `k8s-worker`, `k8s-volume`, `k8s-registrar`, `k8s-exec`, `agent`, `agent-step`, `agent-tools`, `mcp`, `tracing`, `metrics`, `e2e`, `integration`
- Example: `feat(k8s-worker): add pod eviction retry with exponential backoff`

### Code Comments
- Document exported functions and types with GoDoc-style comments.
- Add inline comments for non-obvious Kubernetes API interactions.
- Explain retry/backoff strategies and error handling rationale.
- No comments for self-evident code.

## Brand & Messaging

- This is a **private fork** — not an upstream Concourse contribution (yet).
- Position JetBridge as a **modernization** of Concourse's runtime, not a replacement of Concourse itself.
- Emphasize **compatibility** — existing pipelines should work without modification.
- Highlight **operational benefits** — fewer moving parts, Kubernetes-native scaling, better observability.
