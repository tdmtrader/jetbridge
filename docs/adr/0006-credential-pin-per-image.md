---
status: accepted
date: 2026-09-26
---

# The credential pin is per image; operator-installed templates are the trust boundary

A Run credential handoff delivers the owner's credentials only into a result
producer whose snapshotted image is exactly one the operator pinned by digest
(`--run-credential-worker-image`, chart value `web.runCredentialWorkerImages`).
The pin names an image, not a template, a task script or a workload. Every
detached workload (review, implement) runs the same `jb-review-worker` image,
so one pin covers them all: any operator-installed template whose result
producer runs the pinned image may receive a handoff. The boundary that
decides what that image is asked to do is who may install templates, not the
pin. Submitters choose an installed template and an input; they cannot install
one, choose its image or change its script.

The alternative, a separate worker image per workload with the pin scoped to
a template or result, needs a core flag, a chart value and chart tests to buy
a separation this boundary does not need: whoever can install a template can
already change the script the pinned image runs, which is what a narrower pin
would claim to prevent. Renaming the binary to something workload-neutral was
also deferred. The fixed handoff helper path and socket
(`/usr/local/bin/jb-review-worker auth-handoff`, `/dev/shm/jb-review/auth.sock`)
are shared by every workload unchanged.

## Consequences

- A new detached workload is a worker mode, a template and a result schema.
  It needs no core, flag or chart change to receive credentials.
- Restricting who may set templates on a team that runs detached workloads is
  the operator's control over what receives credentials. The worker READMEs
  say so.
- The pinned image carries every workload's mode, so a change to any mode is a
  new image build and a new pin, reviewed as one unit.
- One handoff per Run is unchanged. The submitting client names, by result,
  the one producer that receives the credentials; no other producer in the
  template is handed them.
