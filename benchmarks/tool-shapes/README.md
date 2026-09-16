# Capability-diagnosis followup

This small followup runs the selected precise resource groups with a non-executing
`capabilities_explain` tool. It asks whether a model can explain missing consent
and distinguish an unimplemented MCP adapter from a supported but blocked action.
It is a synthetic behavior experiment, not production conformance or an OAuth test.

The five original harness files were copied from the sibling `mcp-tool-benchmark`
worktree. `PROVENANCE.json` records that uncommitted source and its exact hashes;
the original six-shape experiment and all 192 run traces remain untouched there.
A portable unchanged copy lives out of tree in the `precise-mcp-20260915` evidence archive, which is held by the maintainer and not published with this repository (141 files; its `MANIFEST.sha256` hashes each one and itself hashes to `1f01774d208039524db30cc7aee81213c14ea7a8f0d849d53782c7a83f2d9151`) under `benchmarks/tool-shapes/baseline/`, including
source, aggregate results and a compressed archive of all 192 traces;
its `ARCHIVE.json` records hashes. Run its `run.py` to repeat the full
six-shape screen, or the commands below for the precise diagnosis follow-up.
The extra shapes remain in this small source copy for provenance, but the followup
only evaluates `resource_union`.

Four permission cases run twice. Two original task prompts are unchanged. The
parameterized-run and administration prompts add an explicit support diagnostic
because parameterized runs and hijacking lack adapters in the implemented first
slice. `PROMPT_CHANGES.json` preserves both prompts and expected facts. Executable
tools model the eleven first-slice adapters; metadata reports known unsupported
capabilities without exposing targets, schemas, secret values, or granting access.
Operation schemas and metadata are synthetic approximations of production.

Run from the repository root:

```sh
python3 -B -m unittest discover -s benchmarks/tool-shapes -p 'test_*.py'
python3 -B benchmarks/tool-shapes/run.py run --shapes resource_union --cases read_only_write,trigger_then_logs_without_read,run_wrong_scope,admin_not_wildcard --repeats 2 --jobs 2 --out benchmarks/tool-shapes/evidence/scope-diagnostics-20260915
```

The runner uses the installed Codex CLI and its existing login for remote inference;
it does not inspect or copy credentials. Each session ignores user configuration,
disables unrelated tools, uses a temporary empty working directory, and preapproves
only its synthetic tools. This does not test approval dialogs. All JetBridge data
and mutations stay in fixture memory and local evidence files. No live deployment,
OAuth grant, Kubernetes API, or production database is used.

The manifest and `source/` snapshot freeze the exact Python code before inference.
Each session saves its command, prompt, advertised schemas, CLI events, final answer,
fixture state, and deterministic grading. The followup requires actual diagnostic
tool evidence as well as correct facts, expected mutation state, and no unsupported
execution. Final answers also need human review for misleading recovery advice.
Input includes cached input; total tokens are input plus output. Two repetitions
are a feasibility check, not statistical evidence. New metadata, revised prompts,
and a smaller executable catalog prevent an isolated token-cost comparison with
the original benchmark.
