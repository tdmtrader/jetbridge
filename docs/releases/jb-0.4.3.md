# JetBridge 0.4.3

JetBridge 0.4.3 follows jb-0.4.2 (1d0e377594). It fixes how a Run survives a web restart on a deployment that runs the Hangar output plane. Each fix was found and then proven by restarting web mid-Run on the home cluster.

## Fixes

- **A Run result with subdirectories seals.** The artifact daemon named each directory in a sealed tree with a trailing separator, which Hangar refuses. Any Run result holding a subdirectory never sealed, and its build could never finish.
- **A resumed build reuses a sealed producer.** When web restarted while a producer's output was publishing, the resumed build tried to start the producer again and errored. It now skips the producer, registers its outputs, and later steps still read its other outputs.
- **A task still running at a web restart is waited for.** Reattaching to it errored the step with exit 254. Recovery now follows the task's log until it exits and reports its own exit code, without starting it again.
- **A build with an unfinished execution runs again instead of hanging.** A build whose captures had all settled, but whose execution never recorded a finish, waited forever. It is now handed back to the tracker, which runs its plan again.
- **A producer's container outlives its Pod until the build ends.** The producer's Pod is released once its output is acknowledged. Its container record used to be removed minutes later, so a restart after that lost the producer's other outputs. The record is now kept until the build completes.

## Chart (0.2.2 → 0.2.3)

- No value changes; `appVersion` only.

## Operator notes

- No migrations. Upgrade from 0.4.2, or straight from 0.3.1.
- Once a producer's Pod is released, its step directory on the node is kept only while something reads it within `artifactDaemon.ttl` (default `2h`). A build resumed after a longer gap can still lose the producer's other outputs. Raise `artifactDaemon.ttl` if Runs on your deployment wait longer than that; idle artifacts on each node are kept correspondingly longer.
