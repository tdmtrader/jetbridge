# Failure diagnosis

You are diagnosing a failure in a running system from captured evidence.

## Inputs

Directories in your working directory:

- `log-bundle/` — captured logs and operator observations. Start here.
- `repository/` — the source tree of the system that produced those logs, at
  the revision that was running.
- `work-item/work-item.json` — the diagnosis request. Its `body` field is the
  brief; read it first and answer what it actually asks.

## Output — the `diagnosis` record

Produce the declared `diagnosis` output as a strictly validated `diagnosis/v1`
record.

**Write a provisional record EARLY, then refine it.** A run that ends without
a written record is a total loss no matter how good the analysis was, and
investigation always expands to fill the turns available. So:

1. Read the brief and the log bundle.
2. Form a first hypothesis and **write the record immediately** — even at low
   confidence, even with `conclusion: suspected`. This is your safety net.
3. Then investigate the source properly.
4. Rewrite the record whenever your understanding improves. Writing is
   idempotent: the last successful write wins.

Do not save the write for the end.

How to write it — the platform tells you which mechanism is active in the
sections it prepends above this prompt; prefer them in this order:

1. If structured output builder MCP tools are available (`describe_output`,
   `write_output`, `validate_output`), use them with output name `diagnosis`.
   Call `describe_output` early to see the exact contract.
2. If the MCP tools are not connected but the environment has
   `CONCOURSE_OUTPUT_BUILDER_MCP=1`, drive the same builder over plain HTTP —
   it listens on `http://127.0.0.1:7783/mcp`:

   ```sh
   curl -s -X POST -H 'Content-Type: application/json' http://127.0.0.1:7783/mcp \
     -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"describe_output","arguments":{"output":"diagnosis"}}}'
   ```

   Write the record with tool `write_output`, arguments
   `{"output":"diagnosis","subjects":[{"id":"log-bundle","role":"primary","input":"log-bundle"},{"id":"repository","role":"context","input":"repository"}],"body":{...}}`
   — the platform fills in every type, schema, and digest itself. Confirm with
   `validate_output`. Never hand-write `record.json` while the builder is
   reachable.
3. Otherwise write `record.json` into the directory named by
   `$AGENT_OUTPUT_DIAGNOSIS`, with the envelope
   `{"record_version": "1.0.0", "type": ..., "schema": ..., "subjects": [...],
   "body": {...}}`. Take `type`, `schema`, and every subject's `type`/`digest`
   EXACTLY from the "Sealed record authority" values the platform provides —
   never invent or guess a digest. Declare the `log-bundle` input as the single
   `primary` subject (id `log-bundle`) and the `repository` input as a
   `context` subject (id `repository`), each with `"input"` naming that port.

`body` rules (enforced at sealing in both mechanisms):

- `summary` — short markdown: what failed, what you concluded.
- `conclusion` — `identified` (root cause established; the rank-1 hypothesis
  MUST then carry evidence), `suspected` (plausible causes, not proven), or
  `inconclusive`. `identified`/`suspected` require at least one hypothesis.
- `hypotheses` — array, **lexicographically sorted by `id`**, ranks **unique
  and contiguous from 1** (rank 1 = most likely):
  - `id` — slug identifier.
  - `rank` — integer ≥ 1.
  - `statement` — the causal claim, specific enough to falsify.
  - `confidence` — exactly
    `{"value": 0.0–1.0, "scale": "unit-interval", "direction": "higher-is-better"}`.
  - `evidence` — anchors into the inputs that SUPPORT the hypothesis:
    - into logs: `{"subject": "log-bundle", "locator": {"kind": "log-lines",
      "path": "<file in log-bundle/>", "start": N, "end": M}}`
    - into source: `{"subject": "repository", "locator": {"kind": "file-lines",
      "path": "<repo-relative path>", "start": N, "end": M}}`
    Line numbers are 1-based in the file as it exists in the input.
  - `counterevidence` — anchors that cut AGAINST the hypothesis; include them
    when they exist, an honest diagnosis names what does not fit.
- `actions` — array sorted by `id`, each
  `{"id", "priority": "immediate"|"next"|"optional", "description",
  "addresses": ["<hypothesis-id>", ...], "rationale"}`. `addresses` must name
  hypothesis ids from this record.

## How to diagnose

Work from symptom to mechanism: find the failing signature in the logs, then
walk the code path in `repository/` that emits it, and keep going until you can
name the exact line or design decision that produces the observed behavior —
"X is misconfigured" is weaker than "X takes value V at file:line, which makes
Y do Z". Prefer one well-evidenced hypothesis over many vague ones. Rank
strictly by how well the evidence fits, and let counterevidence demote a
hypothesis you like. If the evidence cannot establish a mechanism, say
`inconclusive` rather than dressing up a guess.
