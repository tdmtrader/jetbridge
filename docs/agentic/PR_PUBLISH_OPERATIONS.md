# Provider-Native Pull Request Publishing Operations

> **Implementation status:** pre-production and intentionally fail-closed.
> Contracts, provider adapters, exact branch transport, durable publication
> operations, the polling resource, and the reusable monitor workflow are
> implemented. Exact initial publication/reobservation and binding, monitor
> target rendering, revision execution, monitor-run evidence classification,
> current-baseline authority checks, and direct terminal reconciliation are
> also implemented but not production-composed. Concrete impact-policy
> evaluation, action-bound initial observer/sealer/handoff wiring, an exact
> final-baseline materializer and atomic baseline advancement, the database
> resolver for later approved baselines, and complete lifecycle composition
> remain. The web process refuses `--agent-publisher-pull-requests-enabled`
> until that authority spine exists.

The provider-native lane is designed to publish an exactly reviewed repository
change to a pull request and maintain that pull request with a separate polling
workflow. Jetbridge never completes or abandons the pull request. The forge and
its branch policies remain the final merge authority.

The target design has two workflow boundaries:

- `publish-pr-v3` creates or updates the source branch, creates the pull
  request idempotently, and establishes its durable monitor binding.
- `pr-monitor-v3` consumes one completed review batch, conflict transition,
  freshness deadline, or terminal provider observation at a time.

Each active binding owns an ordinary standing Concourse source pipeline. Its
read-only `forge-pr` resource polls every five minutes by default. A binding
admits at most one nonterminal monitor workflow run, and its acknowledged
provider cursor advances only after a safe result has been recorded.

Do not treat the configuration surface as a rollout signal while the startup
gate above remains. In particular, do not bypass the gate to exercise an
adapter directly with production credentials.

Migration `1773106154` deliberately refuses to upgrade any pre-authority
`agent_pr_bindings` row. Those legacy rows cannot prove the accepted review,
the exact successful PR creation, or an approved baseline, so treating them as
authorized would be unsafe. Keep the older binary in place until such
pre-production bindings are drained and removed through an audited operator
procedure; do not edit publication JSON or foreign keys to force the migration.

## Provider support

GitHub is the only supported forge. The provider-neutral `Observer`/`Mutator`
seam in `agent/pullrequest` is retained so a second adapter can be added, but
none ships today; a policy rule naming any other provider fails closed at boot.

Day 1 supports same-repository pull requests only. Fork pull requests fail
closed because they require separate source-repository authority.

## Credential boundary

Configure separate read and write credential references for every provider
destination:

- The read credential is interpolated only into the server-owned `forge-pr`
  resource source. It needs repository read access plus pull-request,
  completed-review, thread, iteration, status, and terminal-state reads.
- The write credential is mounted only into the web node. It needs source-ref
  update, pull-request creation/readback, validation-status publication, and
  review-comment publication. It does not need pull-request completion or
  repository administration.

The two references and their mounted paths must differ. Tokens must not appear
in policy documents, pipeline configuration, repository URLs, command
arguments, logs, publication details, or API responses.

GitHub Git transport uses the configured destination credential through the
private credential helper used for one invocation. Jetbridge does not infer the
authentication mode from token text; the askpass helper must be selected
explicitly. Credential-bearing redirects are disabled.

Grant the write credential only the scopes needed for Git repository reads and
writes, pull-request reads and creation, status writes, and review comment
writes. Do not grant pull-request completion.

## Exact-head and object transport

Every source-branch mutation binds:

- the exact observed source head, or exact expected absence;
- the exact observed target head;
- the exact sealed `repository-change/v1` candidate; and
- its authoritative `validation/v1` and `publish-impact/v1` evidence.

Immediately before a new write, Jetbridge reopens and verifies the sealed
candidate and rechecks both provider heads. It then uploads that exact Git
object and updates the source ref with a force-with-lease-equivalent
compare-and-swap. A stale source or target produces a safe
`rebase_required` reconciliation result; it is not retried as authority to
overwrite a newer human or bot commit.

Branch writes never use a provider REST API: a forge ref-update endpoint can
generally only move a ref to an object the provider already has, and cannot
upload the locally produced commit bytes. The branch object-upload-plus-CAS
operation therefore always uses verified Git smart HTTP. The provider REST API
is used only for objects Git has no concept of: pull-request creation,
observation, commit statuses, and review responses.

## Review batching, validation, and reapproval

The observer emits only the earliest submitted/completed review batch after
the acknowledged cursor. Individual comments do not independently start
workflow runs. Later completed reviews stay queued until the current run has
finished and its exact action is acknowledged.

After any content change or target-head change, `pr-monitor-v3`:

1. adopts the exact observed source and target repositories;
2. applies the authorized review batch when one is present;
3. rebases the result onto the observed target;
4. reruns the complete authoritative validation profile;
5. computes deterministic and agent impact evidence;
6. obtains human reapproval only when the deployment-owned policy requires
   it;
7. updates the source branch, status, and authorized review responses through
   separate idempotent publication operations.

Impact assessment can only escalate. In `agent-decides` mode, a valid explicit
non-escalating assessment may avoid optional deterministic escalation, but it
cannot waive validation, platform invariants, malformed evidence, or assessor
failure.

## Polling and capacity

The default polling interval is five minutes and the default freshness
interval is six hours. Freshness is due from the last successful
reconciliation deadline, so an unacknowledged long-running action keeps one
stable semantic identity rather than being redispatched in a later time
bucket.

Size provider API capacity for:

```text
active bindings * checks per hour * provider requests per check
```

Then include headroom for pagination and one mutation workflow. Provider
adapters bound every collection and fail closed on truncation or ambiguous
pagination. If throttling becomes sustained, increase the poll interval rather
than allowing a retry storm. Keep the freshness interval an exact multiple of
the poll interval.

## Operator recovery

### Pause and resume

Pause prevents new monitor admissions without modifying provider state or
acknowledging queued work. Resume requests a fresh observation and restores
ordinary polling. Both operations use the binding revision as compare-and-swap
authority; a stale operator page must be refreshed before retrying.

### Attention required

`attention_required` means Jetbridge cannot safely advance the binding. Common
causes include an invalid provider response, ambiguous recovery marker,
unsupported exact-object fetch, materialization failure, failed impact
authority, or a terminal workflow outcome that is unsafe to acknowledge.

Before resuming:

1. inspect the binding's latest workflow run and immutable snapshots;
2. compare its source and target heads with the provider;
3. correct credentials, network policy, provider configuration, or repository
   state without editing Jetbridge evidence;
4. resume the binding to force a fresh observation.

Never alter a publication operation key, provider operation marker, cursor,
snapshot, or stored head to bypass attention state.

### Conflicts and stale heads

A conflict observation starts a serialized reconciliation run. A source or
target change detected at the final mutation boundary records
`rebase_required`; the next poll adopts the new exact state. This is expected
when humans, bots, or trunk advance concurrently.

### Provider terminal state

The implemented direct terminal coordinator reopens the exact sealed provider
observation and updates the binding without launching a mutation workflow or
creating a synthetic publication run. Completed and abandoned states never
call a merge, completion, abandonment, or source-branch deletion endpoint.

Production composition must still connect that terminal binding transition to
the owned monitor-pipeline pause/archive lifecycle. Until the startup gate is
removed, treat pipeline disposal as an uncomposed lifecycle requirement, not
as deployed behavior. Run and publication history must remain available after
that lifecycle transition.

### Operator termination

Operator termination stops future Jetbridge monitor work but does not
manufacture a provider terminal state. The pull request remains forge-owned
and must be completed or abandoned there.

## Network policy

The web node requires egress to:

- the configured provider API host;
- the credential-free repository URL's HTTPS host for exact Git transport;
- existing Jetbridge dependencies already required by the web node.

The `forge-pr` resource needs the corresponding read-only API and Git egress
from resource-check/get containers. Agent and deterministic transformation
pods remain hermetic and receive no provider credential. When provider-host
egress restrictions are enabled, allow only the exact policy-configured HTTPS
hosts and required DNS path; do not add a wildcard Internet rule.

## Audit and incident evidence

Use the pull-request binding and its linked workflow runs as the audit spine.
Exact observations, repository candidates, validation results, impact
decisions, questions, answers, review responses, and publication occurrences
remain immutable records. The mutable binding row is coordination state, not
the source of evidence.

For an incident, retain:

- binding ID, revision, provider locator, and safe external URL;
- originating and monitor workflow-run IDs;
- observation, candidate, validation, impact, response, and approval snapshot
  IDs and digests;
- publication operation kind, operation key, occurrence, status, exact heads,
  and safe provider result identifiers;
- provider-side audit entries bearing Jetbridge operation markers.

Never copy provider tokens, credential references, raw cursors, launch tokens,
or unrestricted publication detail into an incident ticket.
