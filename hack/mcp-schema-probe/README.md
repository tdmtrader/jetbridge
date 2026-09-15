# Authenticated HTTP schema feasibility probe

This fixture imports the production `atc/mcp` operation definitions and union
generators, and serves them through the production
`atc/api/mcpserver.NewHTTPHandler` wrapper with official Go SDK v1.6.1. The actual
`internal/mcpclient.Client.SaveToken/Connect` path and installed Codex CLI exercise
the same schemas. No fixture handler is registered in production.

Three synthetic grants select two read branches (`pipelines_list`,
`pipeline_get`), one remaining read branch, or those reads plus `pipeline_pause`.
Input and output schemas come directly from the selected production definitions.
All results are fixed fixture data. The probe never requests the synthetic write.

The SDK/reference checks compare the schema after HTTP transport, call valid
branches, and verify rejected branches/arguments never reach handlers. Four Codex
sessions check normal reads, pruning, conservative mixed-group approval failure,
and the same mixed schema with explicit fixture-only group approval. The runner
checks dispatched operations against expected calls.

Run from the worktree root; the output directory must not already exist:

```sh
GOCACHE=/private/tmp/jb-mcp-schema-probe-cache go build -o /private/tmp/jb-mcp-schema-probe ./hack/mcp-schema-probe
python3 hack/mcp-schema-probe/run.py --binary /private/tmp/jb-mcp-schema-probe --out /private/tmp/jb-mcp-schema-evidence
```

The process must be permitted to bind/connect to numeric loopback and let Codex
reach its configured model service. The original sandbox could not bind the
listener; the bounded probe was run with approved escalation. No database,
Kubernetes or live JetBridge connection is used.

Codex uses its existing login itself. The runner never reads or copies credentials,
ignores user MCP configuration, disables other tools/plugins, uses temporary
working directories and ephemeral sessions, and supplies only synthetic bearer
labels to the fixture. Reference-client grant files are newly created temporary
fixture files and removed when checks finish.

This is schema/transport feasibility, not production authorization evidence or a
full OAuth test. No browser enrollment, exact callback registration, token renewal,
revocation, interactive approval dialog or hosted-client certificate trust is
verified. Inspect the evidence's negotiated protocol: installed Codex 0.153.1 uses
2025-06-18; the reference SDK client uses 2025-11-25. The existing SDK-backed
production wrapper accepts both without a protocol implementation change.

Mixed groups retain a single native tool approval identity. The conservative
annotations therefore gate even read branches in that group. Explicitly approving
the fixture's whole `pipeline` tool proves schema usability but does not establish
per-branch approval controls. Operation authorization remains a server obligation.
