# bench/harness — out-of-band bench tooling

Tooling that runs bench corpus cases against the platform and grades the
results outside it. A separate Go module so the root module never compiles it;
run its tests with `make test-bench-harness`.

## Why out-of-band

`agent/experiment` cannot target a node: `TargetKind` is `workflow` or
`function` only (`agent/experiment/types.go`). Rather than change the experiment
subsystem before we know what a node should look like, we execute nodes
directly (`fly agent nodes run`) and grade with `cmd/reviewgrade`.

Node runs are already first class — `POST
/api/v1/agent/nodes/:name/versions/:version/runs`, `DefinitionKindNode` — so no
wrapper workflow is needed to start. A one-node workflow is also supported and
tested (`agent/workflow/node_reference_test.go`) if the wrapper is ever wanted.

## Packages

| Path | Responsibility |
|---|---|
| `reviewgrade/` | Score a produced `review/v1` record against a corpus `expected-findings` oracle, by anchored file+region. |
| `cmd/reviewgrade/` | CLI over the above. |
| `casespec/` | Read a case.yaml's `pre_state` ports and signature input types. |

## STATUS 2026-07-30: blocked on a deployed fix

The loop below cannot run yet. Enabling `agentSnapshots` on the cluster
requires `artifactDaemon.hangar.bucket`, and every Hangar-enabled artifact
daemon exits at boot:

```
hangar: invalid zstd encoder configuration: unknown encoder level
```

`cmd/artifact-daemon/main.go` builds its `hangar.GCSConfig` without a
`ZstdLevel`, and `zstd.EncoderLevel`'s zero value is not a valid level. Fixed in
`agent/hangar/gcs.go` (commit `cb34f4b792`), but the fix needs a
`registry.home/jetbridge` image build to reach the cluster. Local Docker was
unavailable when this was written.

To resume:

1. Build and push a `registry.home/jetbridge` image containing `cb34f4b792`.
2. Re-apply the cluster enablement, which is preserved as a single reverted
   commit in home-infra:
   ```bash
   cd ~/home-infra && git revert --no-edit 4f08672 && git push origin main
   ```
   That restores `agentSnapshots.enabled`, daemon mTLS pinned to
   `concourse-artifact-daemon-tls-pinned`, the Hangar emulator Application, and
   the hangar bucket/endpoint. The pinned TLS Secret still exists in `cicd`; it
   was left in place because it is inert while `tls.enabled` is false.
3. `kubectl --context theborg -n argocd annotate app root argocd.argoproj.io/refresh=hard --overwrite`
4. Confirm `fly -t home agent snapshots list` returns a table rather than
   `snapshot service is not enabled`, and that the artifact daemon stays
   `1/1 Running`.

`fly` must be built from this tree — the released 0.2.208-rc CLI predates
`fly agent nodes`:

```bash
go build -o /tmp/fly ./fly
```

## The loop

```bash
# 1. Import and release the node (once per node version).
/tmp/fly -t home agent nodes import bench/nodes/code-review
/tmp/fly -t home agent nodes release code-review 1 --compatibility compatible

# 2. Materialize a case into typed snapshots, then run the node with them.
/tmp/fly -t home agent nodes run code-review 1 \
  --input repository=<id> --input change=<id> --input work-item=<id>

# 3. Download the produced review/v1 snapshot and extract record.json.
/tmp/fly -t home agent snapshots download <output-id> --to /tmp/review.tar
mkdir -p /tmp/review && tar -xf /tmp/review.tar -C /tmp/review

# 4. Grade it.
cd bench/harness && go run ./cmd/reviewgrade \
  -expected ../corpus/review-jb-004/ground_truth/expected_findings.yaml \
  -review /tmp/review/record.json
```

## Rules that are not optional

- **Run on the cluster, not this machine.** 24 of 34 cases carry
  `known_leak_channels: [project-auto-memory]`; this machine's auto-memory
  states their answers verbatim. Cluster agent pods are naturally clean.
- **Never co-run review-jb-001 and feedback-jb-001.** The latter's exposure is
  the former's ground truth.
- **Never headline a result on a `cc` case.** They are `memorization_risk: high`.
- **Cite the corpus commit and the deployed web image** in every result record.
- **A `reviewgrade` match is a location candidate, not a scored finding.**
  Confirm against the case's `ground_truth/rubric.md`.
- **Respect `materialize:` directives.** Cases whose answer key is reachable
  from branch refs (review-jb-001, neg-cc-001) mandate a refs-suppressed
  materialization; `casespec` preserves the directive so it cannot be lost.

## The oracles are not uniform

The corpus's `expected_findings.yaml` files were hand-authored per case and
never normalized. Across the eight that exist there are three schema strings
(`expected-findings/v1`, `expected-findings/v0`, `review-findings/v1`, plus one
file with no `schema` key), two list keys (`findings:`, `required:`), and six
location spellings (`file`+`region`, `region` as `{start,end}`, `region.also[]`,
`anchors[]` with `region`/`line`, `primary_anchor` with `lines: [a,b]`,
`supporting_regions[]`).

`reviewgrade` normalizes all of them rather than rewriting withheld human
ground truth, which is the artifact each case's validation run was measured
against. `reviewgrade/corpus_test.go` pins every real oracle, so a ninth dialect
fails loudly instead of silently scoring zero recall.
