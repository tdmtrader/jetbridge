# Triage request: route the UX audit №4 findings to executors

**Type:** triage / scoping (no code change is being asked for in this task)
**Opened:** 2026-07-19
**Deliverable:** a scoping document, written as a single new markdown file named
`TRIAGE.md` at the root of this checkout. That file is what gets circulated; do not
split the routing across several files, and do not change any other file.
**Inputs:** the finding list in [`audit-findings.md`](audit-findings.md), plus this
repository checkout

## Background

A fresh-eyes usability audit of the live JetBridge agentic UI was run this morning
against `concourse.home`. The auditor was asked deliberately *not* to read any
previous audit, so the findings are independent — and there are a lot of them.
The finding list is attached. It is raw: findings are grouped by where the auditor
hit them, not by size, not by cost, and not by who could do them.

Nothing has been decided about any of these findings yet. The list is exactly as
handed over.

## What we need from you

We do not need estimates and we do not need the fixes. We need **routing**: for
each finding, which executor should own it, and why. The bottleneck here is not
capacity, it is mis-assignment — work handed to the wrong executor either stalls,
costs money for nothing, or lands broken.

We route work to exactly five executor classes. Assign every finding to one of
them (a finding may need a second, smaller action in another class — say so
explicitly when it does, and mark which one is primary):

| Class | What it means |
|---|---|
| `ops` | An action taken by a human operator (or a supervised session) against the running deployment or the queue. Minutes, not days. No code change, no review cycle. |
| `loop` | Filed as a self-contained ticket and executed autonomously by an agent in a pod. The agent gets the ticket body plus a checkout of this repo, implements, and the automated gates decide whether its branch is pushed for human review. Nobody watches it while it runs. |
| `wave` | A human working session on a branch, batching a set of small related changes, reviewed and merged as one unit. |
| `plan` | Too large to file or start raw. Gets its own written plan document (spec + task breakdown, `superpowers:writing-plans` discipline) before anyone executes it. |
| `decision` | Cheap to build, but the semantics need an owner's call before anyone builds anything. |

## The document you produce (`TRIAGE.md`) should contain

1. **A routing for every finding** in `audit-findings.md`, with a one-line rationale
   for each one whose class is not self-evident. Findings that need an action in a
   second class must say so.
2. **The evidence behind the discriminating calls.** Where a routing turns on a
   property of this repository or this deployment, cite what settles it. We have
   been burned by routings that were argued from plausibility.
3. **Root causes where several findings share one.** If a group of findings is
   really one underlying problem, say so and route the underlying problem, not
   each symptom separately.
4. **Coordination constraints that the routing has to respect.** This repository
   has several tracks in flight; anything you dispatch or schedule has to live
   alongside them. Find the constraints, state them, and show how they shaped the
   routing.
5. **Self-contained ticket bodies for everything you route to `loop`.** An agent
   receives the ticket body and a repo checkout and nothing else — no conversation,
   no follow-up questions, no access to this document. Each body must carry
   everything needed: behavior wanted, files expected to change, what must be
   verified before starting, and what is explicitly out of scope.
6. **An execution order** — what happens first, what unblocks what.
7. **Any improvement to the machinery itself** that these findings expose. Most of
   this work will be executed by the same machinery the audit was looking at; if
   the findings reveal something that makes it unreliable or invisible, that is in
   scope for the routing too.

## Environment, as observed

These are facts the operator pulled off the running deployment (the UI does not
expose them), handed over with the finding list:

- Deployed web version: **v0.2.195-rc**, whose `vcs.revision` matches the commit
  this checkout is at. The audit therefore saw current code — no finding is
  already-fixed-but-not-deployed.
- The web deployment's `CONCOURSE_AGENT_STEP_IMAGE` is
  **`registry.home/agent-runner:v0.2.167`**. That value is set in the cluster's
  GitOps repository (`home-infra`), which ArgoCD reconciles; a `kubectl edit`
  against the deployment is reverted within minutes.
- Nothing is dispatched or in flight right now.

## Constraints

- Work from this repository and the attached finding list. You have no live cluster
  and no way to run anything against the deployment — do not present inferred
  behavior as observed, and if a routing depends on something you could only check
  live, say what you would check.
- A routing that cannot be defended from evidence in the repo is worse than an
  honest "needs an owner call" — that is what the `decision` class is for.
- Every finding gets a class. If one genuinely needs two, name both and mark which
  is primary.
