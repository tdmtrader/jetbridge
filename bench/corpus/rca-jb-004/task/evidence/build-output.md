# Build output — `dogfood/build-and-test`, builds #417 and #418

Captured with `fly -t theborg watch -j dogfood/build-and-test -b <n>`.
ANSI colour codes stripped. Timestamps are the web pod's.

---

## Build #417 — PASSED (cache miss)

This is the first build after the `main-repo` resource picked up a new commit,
so the resource cache for that version did not exist yet.

```
initializing
selected worker: k8s-concourse

get main-repo
  fetching git://forge.home/theborg/cicd @ 9c2c5dacc1
  ...
  succeeded

task run-unit-tests
  initializing
  running /bin/sh -c ./ci/scripts/unit
  ...
  succeeded

succeeded
```

Duration 6m12s. Nothing unusual.

---

## Build #418 — ERRORED (cache hit)

Re-run of #417. Same pipeline, same resource version, nothing changed in
between except that #417 left the cache populated.

```
initializing
selected worker: k8s-concourse

get main-repo
  INFO: found existing resource cache

  succeeded

task run-unit-tests
  resolve node IP for 100.68.228.107: get node 100.68.228.107: nodes "100.68.228.107" not found

errored
```

Duration 4s. The `get` step is green — the UI shows it as a normal successful
get. The `task` step never starts a pod; it errors while it is still working out
what to run (this pipeline uses `file: main-repo/ci/tasks/unit.yml`, so the very
first thing the task step does is read a file out of the `main-repo` artifact).

We have since reproduced the identical error on a task that takes `main-repo` as
a plain input with an inline `config:` instead of `file:` — so it is not
specific to task-config reading. Anything that reads the artifact trips it.

---

## Build #419 — ERRORED (cache hit)

Identical to #418, same IP in the message. Included only to show the address is
stable across builds:

```
task run-unit-tests
  resolve node IP for 100.68.228.107: get node 100.68.228.107: nodes "100.68.228.107" not found

errored
```

---

## Build #420 — PASSED (cache miss, forced)

After `rm -rf /var/concourse/artifacts/steps/rc-*` on the node, build #420
went green again with a full `fetching git://…` get — and #421, run immediately
after with no other change, errored exactly like #418.
