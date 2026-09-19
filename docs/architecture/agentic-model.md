# Agentic: relationships and invariants

Vocabulary: [`atc/agent/CONTEXT.md`](../../atc/agent/CONTEXT.md).

## Position

```
MCP client ──▶ MCP surface ──▶ wrapped API ──▶ Core
Build step ──▶ Composition ──▶ Run admission port ──▶ Core
```

Core never imports this context. Its packages may import only a pinned
list of core packages, and only the web composition root may import them.
Removing the context leaves core compiling.

## MCP authorization

```
User ──consents──▶ MCP grant ──restricts──▶ Principal ──▶ Operation
```

- An **MCP grant** binds a client, an identity, a resource and a set of
  scopes, with idle and absolute expiry and revocation. It restricts the
  identity's team permissions and never adds a role.
- A **scope** is required by an operation and carried by a grant; an
  operation with a scope the grant lacks is refused as insufficient scope,
  after which ordinary team and role authorization still applies.
- **Account eligibility** proves only that an account could never be
  authorized. It authorizes nothing.
- A **disabled operation** is refused for every caller regardless of grant.

## Composition

```
Build ─ step ──▶ Composition call ──1:n──▶ Composition iteration ──▶ Child run
                 (build id, plan id)        (ordinal)                (pipeline run)
```

- A **composition call** is identified by build id and plan id alone. The
  identity is derived, never minted, and is deliberately not a digest of
  the inputs: keying on inputs would move the key on pod eviction and admit
  one invocation twice.
- Admission is claim-first inside one transaction: the call row is
  inserted before the run is admitted, so a concurrent loser blocks on the
  unique key and then re-reads the winner. That is a **replay**.
- The **input digest** is recorded on first admission and verified on every
  replay. A mismatch is refused; it is never part of the key.
- A **composition iteration** records which child run an admission
  produced. Today every call has exactly one, ordinal 1.

## Invariants the store enforces

- One call per (build, plan). No status column: the unique key is the only
  dedupe.
- An iteration names an existing run; ordinals are positive; one row per
  (call, ordinal).
- Every foreign key cascades on delete and never restricts, so this context
  can never veto a core delete. "Exactly one iteration names this run" is
  therefore an invariant over admission, not over the run's whole life.
