---
status: accepted
date: 2026-09-04
---

# The agentic layer reaches core only through the run admission port

The agentic context (the MCP surface and composition) must be removable
without touching core, and core must never depend on it. So core publishes
one seam, the run admission port, expressed without database types, and the
agentic packages may import only a pinned list of core packages; the web
composition root is the single place that wires the two together. The
alternative, letting composition call the run factory and read run tables
directly, was tried in the removed v1 and v2 agentic layers and made core's
schema an agentic dependency.

## Consequences

- Composition calls run inside a caller-owned transaction through a hook
  the port exposes; core never reads the composition tables.
- Three root tests enforce the seam: the importer allowlist and reverse
  ratchet, the single caller of the transactional run creation, and the
  ban on core's run package naming composition tables. Adding an agentic
  package means pinning it.
- The word *agent* is reserved for this context.
