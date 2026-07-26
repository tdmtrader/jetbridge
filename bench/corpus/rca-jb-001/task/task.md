# RCA request: every `concourse-web` rolling restart errors all in-flight builds

**Type:** root-cause analysis (diagnosis only — no code change is being asked for)
**Opened:** 2026-07-04
**Priority:** high — the release pipeline cannot upgrade itself reliably
**Evidence:** see [`evidence.md`](evidence.md) alongside this work item

## What is happening

Whenever the `concourse-web` deployment is rolled — a `kubectl rollout restart`,
a Helm upgrade, or the release pipeline's own `self-upgrade` job — every build
that is in flight at that moment ends up with build status **`errored`** within
seconds of the old web pod receiving SIGTERM.

It is not a step failure. The step never reports failing, so `attempts:` does
not save the job either (attempts retry failed steps, not errored builds). The
only recovery is a manual re-trigger once the rollout has settled.

A related, possibly-same-shape symptom: on this single-node cluster, builds
intermittently go straight to `errored` under load instead of retrying. That has
been attributed to node contention and is currently bandaged with `attempts: 2`
on the release-chain jobs. Whether it is the same mechanism is an open question
this analysis should answer if it can.

## What we expected instead

The runtime was built so that a web restart would be survivable. Task pods are
independent Kubernetes objects — they carry no `OwnerReferences` back to the web
deployment, so nothing in Kubernetes garbage-collects them when a web pod dies —
and they carry exit-status / resource-result annotations specifically so that
step state can be recovered after a restart. A web upgrade was supposed to be
close to invisible to a running build. Today it is fatal to every one of them.

## Standing hypothesis — confirm or refute it

The working theory on the team is that this is **the Kubernetes runtime deleting
task pods when the web shuts down**: SIGTERM cancels the build context, the
context cancellation propagates into the runtime's process-wait path, and that
path deletes the pod it was waiting on. The pod disappears out from under the
build, so the build errors.

That theory fits the observed `failed-to-get-pod` / `pod … not found` noise in
the web logs (see evidence), and the code it points at is real. Before
anyone builds a fix around it, we want it verified against what the production
process actually does, not against what a plausible-looking code path could do.

## Deliverable

A written root-cause analysis, filed as `RCA.md` at the root of the repository
you are given. Specifically:

1. **The mechanism.** What actually marks these builds `errored`? Name the
   code path(s) with `file:line`, and describe the sequence of events from
   SIGTERM to the build's final status.
2. **Provenance.** When and why did this behavior enter the codebase? If it was
   introduced deliberately, say what problem it was solving — we need to know
   what we would break by removing it.
3. **The standing hypothesis.** Confirmed or refuted, with the evidence that
   settles it. If refuted, explain what the `failed-to-get-pod` / `pod … not
   found` log lines actually are, and where they sit in the causal ordering.
4. **Blast radius.** Is the rolling-restart case the only situation this
   mechanism affects, or does it catch other situations too? Include the
   intermittent single-node `errored` builds in your assessment.
5. **Recommended direction.** A short statement of what a correct fix would have
   to preserve and what it would have to change. A patch is optional and is not
   what is being graded; the analysis is.

## Constraints

- Diagnose from the repository and the attached evidence. There is no live
  cluster to inspect and no additional log capture available — do not assume
  access to cluster state, and do not present inferred behavior as observed.
- Cite concrete `file:line` references and commit SHAs for every claim about the
  code or its history. Distinguish clearly between what you verified and what
  you inferred, and state your confidence in the primary conclusion.
- If two mechanisms are both contributing, say so — do not stop at the first
  plausible one.
