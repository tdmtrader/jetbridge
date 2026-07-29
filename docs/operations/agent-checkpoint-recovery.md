# Agent checkpoint recovery operations

Agent checkpoints recover an agent's durable filesystem and session evidence,
not its live environment. They are an interruption-control mechanism, not a
general process checkpoint.

## What recovery guarantees—and does not

Only a **committed safe boundary** is recoverable. Capture first obtains the
server-controlled safe-boundary/quiescence lease, packages the workspace, and
commits the manifest only after the archive has been durably committed to
Hangar. A staged, aborted, failed, or merely uploaded archive is not a recovery
point.

The archive contains filesystem state and, where the provider supports it,
durable session evidence. It does **not** preserve a live process, process
memory, PID, socket, network connection, mount, credential injection, kernel
state, or other operating-system state. Recovery always creates one exact,
durable, fresh replacement attempt; normal automatic recovery never reruns or
reattaches the interrupted process.

Recovery validates the immutable admitted provenance before it starts: run and
function identity, provider/adapter, runtime image, model, configuration,
inputs, MCP configuration, and skills. It also chooses and freezes one source
checkpoint generation. A later retry/re-entry reads that exact retained source;
it cannot silently select a newer checkpoint.

## Automatic-recovery admission

An interruption is admitted automatically only if all durable checks pass:

- The source is a committed archive at a safe boundary with matching immutable
  provenance.
- The external-effect journal is complete and server-owned. Every effect for
  the interrupted attempt must be committed, read-only, and match the admitted
  provider and adapter version.
- Native provider resume additionally requires a non-empty exported session,
  static server configuration declaring session export and native resume, and a
  valid compatible provider recovery proof. Provider stream output and workflow
  YAML cannot supply this authority.

If native resume lacks that static proof, recovery uses **workspace-only**:
restore the archive and start a fresh provider session. If no checkpoint exists
but the effect journal is clean, **checkpoint-zero** starts a fresh replacement
without a restored archive. Missing, corrupt, incompatible, unsafe, or
effect-ambiguous evidence requires **manual review** instead of automatic
recovery.

External effects are controlled at least once: an interrupted effect may have
reached its remote system even when the local worker died before recording its
completion. Therefore automatic recovery is allowed only for the complete
server-owned journal described above. A begun, non-read-only, mismatched, or
otherwise incomplete entry is ambiguous and must be reviewed manually; do not
retry it automatically.

Current production legacy Claude composition has `Authorities: nil`: it has no
complete effect journal or recovery proof. Consequently it fails closed for
interrupted sources and does **not** currently auto-resume Claude.

## Operator procedure

1. Identify the interrupted attempt, its replacement attempt (if allocated),
   checkpoint generation, recovery mode, and interruption reason in the run
   records and logs.
2. For `manual_review_required`, inspect the durable effect journal and the
   exact attempt transcript/metrics. Reconcile any remote effect before deciding
   how to continue; do not treat a successful filesystem restore as proof that
   a remote write did not occur.
3. For a restore failure, verify the generation-pinned Hangar object and daemon
   access first. A missing daemon cache is recoverable: Hangar is the durable
   source. Do not delete the object to test this path.
4. For workspace-only or checkpoint-zero recovery, give the new agent bounded
   reconstruction guidance. It must recreate required processes, sockets,
   credentials, mounts, and network connections from the admitted configuration
   before continuing.

## Retention and diagnostics

The exact source archive is pinned while its current replacement attempt is
`scheduling`, `materializing`, or `running`. This holds even if the source
checkpoint is superseded, so garbage collection cannot remove the bytes needed
by a recovery that is still being materialized or executed.

After that replacement leaves those states, normal checkpoint expiry may remove
the archive (superseded checkpoints are eligible after one hour; a terminal
committed head after 24 hours). The durable attempt, frozen source identity,
events, effects, per-attempt metrics, and per-attempt transcript remain for the
diagnostic metadata window (currently 30 days after terminalization) before
bounded cleanup. Backwards-compatible build/plan aggregate metrics and
transcript projections are retained independently.

Metrics and transcripts are attributed to the exact server-assigned attempt.
Legacy build/plan rows are aggregate presentation projections: metrics add each
attempt's monotonic delta because provider counters reset in a fresh process;
one final presentation is selected for the legacy transcript projection. Use
the per-attempt records for recovery investigation.

## Monitoring and troubleshooting

All recovery telemetry uses bounded names and labels; it deliberately excludes
run, attempt, pod, node, model, session, digest, and provider-controlled label
values.

| Signal | Labels / meaning |
| --- | --- |
| `concourse_agent_checkpoint_duration` | `phase` is `requested_to_quiesced`, `archive`, `upload`, or `total`; `trigger` is `elapsed`, `completion`, `explicit`, or `preemption`. |
| `concourse_agent_checkpoint_captures_total` | `outcome` is `committed`, `skipped`, or `failed`; the same bounded `trigger` applies. |
| `concourse_agent_checkpoint_lost_work` | Time since the previous committed safe boundary, by bounded trigger. |
| `concourse_agent_checkpoint_retained_bytes` | Uncompressed bytes observed for **each committed archive**, by trigger. It is a histogram observation, not a live retained-total gauge. |
| `concourse_agent_interruptions_total` | Bounded reason: `pod_deleted`, `evicted`, `node_lost`, or `preempted`. |
| `concourse_agent_recovery_attempts_total` | `mode` is `native_resume`, `workspace_only`, `checkpoint_zero`, or `not_admitted`; `outcome` is `succeeded`, `failed`, or `manual_review_required`. `succeeded` means the fresh attempt crossed its pre-launch restore gate, not that the agent ultimately succeeded. |
| `concourse_agent_recovery_restore_duration` | Restore duration with bounded `outcome` (`succeeded` or `failed`). |
| `concourse_agent_recovery_ambiguous_effects_total` | Count of unsafe/incomplete effects that forced manual review. |

The bundled alerts mean:

- `ConcourseAgentCheckpointCaptureFailures` (warning): a capture failed; inspect
  capture logs and durable snapshot storage before retrying the run.
- `ConcourseAgentRecoveryManualReviewRequired` (critical): automatic recovery
  correctly refused unsafe or unusable evidence; reconcile effects and review
  the interrupted run.
- `ConcourseAgentCheckpointRestoreFailures` (warning): inspect the exact
  checkpoint object, Hangar/daemon access, and recovery logs before retrying a
  restore.
- `ConcourseHangarSnapshotFailures` (warning): daemon PUT/GET failures prevent
  durable archive commit or restore; verify Hangar credentials, egress, and
  object integrity.

Useful failure signals are `no static recovery authority`, `complete
server-owned effect journal authority is required`, `interrupted effect journal
is incomplete or unsafe`, `immutable provenance differs`, `frozen recovery
source is unavailable`, and `frozen native recovery is no longer safe`. Each is
a fail-closed admission result, not an instruction to force a process retry.
