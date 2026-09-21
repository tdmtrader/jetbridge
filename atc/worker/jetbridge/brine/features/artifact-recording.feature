Feature: Recording where a step's outputs went

  The worker records artifact locations, builds the following pod and asks
  real daemon processes to copy outputs. Local pod-layout cases inspect real
  API objects; they do not claim kubelet execution. Named-node reads and
  producer-mount writes run in live/artifact-recording.feature.

  Scenario: The next step fetches its input from the directory the producing step wrote it to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    And a later step "consume-42" takes the artifact "build-42-output-result" at "/tmp/build/workdir/from-earlier"
    When the worker records where the step's outputs went
    And that step's pod is built
    Then that fetch asks the daemon for "build-42/result"
    And that fetch does not ask the daemon for "build-42-output-result"
    And the pod prefers the node "node-1"

  Scenario: A step that reads nothing is not steered to any node
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    And a later step "unrelated" takes no inputs
    When the worker records where the step's outputs went
    And that step's pod is built
    Then the pod expresses no preference about where it runs

  Scenario: Every input is fetched by one init container in one request
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "wide-fan-in" takes the artifact "vol-a" at "/tmp/build/workdir/a"
    And it also takes the artifact "vol-b" at "/tmp/build/workdir/b"
    And it also takes the artifact "vol-c" at "/tmp/build/workdir/c"
    When that step's pod is built
    Then the step's inputs are fetched by one init container in one request
    And that fetch asks the daemon for "vol-a"
    And that fetch asks the daemon for "vol-b"
    And that fetch asks the daemon for "vol-c"

  Scenario: An output whose node the worker could not identify is still fetched by its directory
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on a node the worker could not identify
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    And a later step "consume-42" takes the artifact "build-42-output-result" at "/tmp/build/workdir/from-earlier"
    When the worker records where the step's outputs went
    And that step's pod is built
    And the node's daemon answers its fetch
    Then the fetch succeeded, so the step starts
    And what the step finds at "/tmp/build/workdir/from-earlier" is "compiled binary"

  # An output the pipeline named with an absolute path, which is how a step
  # captures a directory its own image owns. A task may declare an output named
  # "/data" and give it no path: of its own — the ATC resolves an absolute NAME
  # to itself — and the mount lands at /data, so a Postgres container's data
  # directory becomes an artifact the next step can read.
  #
  # The two halves of the index are built differently for such a name. The
  # directory comes from joining <store>/steps/<handle> with the name, and a
  # join CLEANS the leading slash away: the bytes land in steps/<handle>/data.
  # The key was built by concatenation — handle + "/" + name — which does not:
  # the index says "<handle>//data". One name, two directories.
  #
  # The daemon will not paper over it. It refuses a non-canonical key outright
  # rather than cleaning it, because callers derive guard keys by splitting on
  # "/": "<handle>//data" yields an empty segment where the sweeper derives a
  # handle, so the lock excludes nobody and the sweeper is free to delete the
  # tree mid-read. The refusal is correct; the key it refuses is the defect.
  #
  # Nothing before the consuming step can see it. The producing step writes its
  # files through the mount and exits 0. The alias registered alongside the
  # index carries the JOINED path, so it is right, and a web-side read by
  # volume handle — "the output ... reads back as ..." above — resolves through
  # that alias and returns the bytes. Only the next step's init container asks
  # for the recorded key, and it asks the one daemon that holds the data and
  # will not answer to that name. The build fails on the fetch, in the step
  # after the one that was wrong, and the mirror that would have protected the
  # output was refused the same way and swallowed.
  #
  # Every other output in this file is named "result", "binary", "report" — a
  # name with no separator in it, for which joining and concatenating agree by
  # accident. That is why the two derivations could disagree for a year with
  # every scenario green.
  Scenario: An output the pipeline named by an absolute path is fetched from the directory it was written to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "/data" is the volume "build-42-output-/data" holding "the postgres data directory"
    And a later step "consume-42" takes the artifact "build-42-output-/data" at "/tmp/build/workdir/pgdata"
    When the worker records where the step's outputs went
    And that step's pod is built
    And the node's daemon answers its fetch
    Then the fetch succeeded, so the step starts
    And what the step finds at "/tmp/build/workdir/pgdata" is "the postgres data directory"

  @core-review
  Scenario Outline: Output names survive the fetch shell unchanged
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "<name>" is the volume "build-42-output-<name>" holding "typed review findings"
    And a later step "consume-42" takes the artifact "build-42-output-<name>" at "/tmp/build/workdir/review"
    When the worker records where the step's outputs went
    And that step's pod is built
    And the node's daemon answers its fetch
    Then the fetch succeeded, so the step starts
    And what the step finds at "/tmp/build/workdir/review" is "typed review findings"

    Examples:
      | name                    |
      | owner's-report          |
      | report'$(printf wrong)' |

  # And the failure this file exists to make loud. The daemon refuses a batch
  # it could only partly deliver — 404 when an artifact is simply not on this
  # node, 422 when one that IS here is refused (an absolute symlink target),
  # 500 when resolving it broke, with an overall status of "error" in every
  # case — and the init container must turn that into a failed build.
  #
  # A fetch that exits 0 instead is the worst shape a build can take. The
  # kubelet reads success, the step's own command starts, and it runs against a
  # workspace missing the inputs the pipeline promised it. The failure surfaces
  # later, as a task erroring on a file it was handed, with nothing in the log
  # to connect it to the fetch that never happened — and on a green step it may
  # not surface at all: a put that uploads an empty directory, a test suite that
  # finds no tests and passes.
  #
  # The scenario above is the contrast that keeps this one honest: same script,
  # same shell, a daemon that CAN deliver, and the step starts.
  Scenario: A fetch the daemon could not fully deliver fails the step instead of starting it
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    And a later step "consume-42" takes the artifact "build-42-output-result" at "/tmp/build/workdir/from-earlier"
    And it also takes the artifact "vol-lost" at "/tmp/build/workdir/lost"
    When the worker records where the step's outputs went
    And that step's pod is built
    And the node's daemon answers its fetch
    Then the fetch failed, so the step never starts

  Scenario: The fetch dials the daemon on the node its own pod landed on
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "needs-inputs" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    When that step's pod is built
    Then the fetch dials the daemon on the node the pod lands on

  Scenario: A check's pod is not handed the node's artifact store
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later check "check-vulnerable" takes no inputs
    When that step's pod is built
    Then the pod is not given the node's artifact store

  Scenario: A task's pod on the same worker is handed it
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "task-with-store" takes no inputs
    When that step's pod is built
    Then the pod is given the node's artifact store

  Scenario: A step may only land on a node whose daemon has declared itself ready
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "needs-a-daemon" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    When that step's pod is built
    Then the node running the artifact daemon can accept the pod

  @CO-10
  Scenario: A step is steered to the node holding most of its inputs
    Given a jetbridge worker placing step "link" from recorded artifact locations
    And an input artifact is recorded on node "node-1"
    And an input artifact is recorded on node "node-2"
    And an input artifact is recorded on node "node-2"
    When that step's pod is built
    Then the pod prefers the node "node-2"

  Scenario: A step's node-local directories are made on a node that has never held them
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "first-here" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    When that step's pod is built
    Then every directory the pod expects on the node is created if it is missing

  Scenario: A task cache is not filed among the step data the daemon sweeps
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "cached-build" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    And it keeps a task cache at "/tmp/build/workdir/.gradle"
    When that step's pod is built
    Then the task cache is filed apart from the step data on that node

  Scenario: A retried step clears its workspace through a mount it can write to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "retried-build" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    And that step has run here before
    When that step's pod is built
    Then the step's cleanup can really delete what the last attempt left
    And the fetch of its inputs still cannot write there

  Scenario Outline: Volume lookup preserves location, database identity and delivery
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And an artifact volume "<key>" the worker can look up
    And the node's daemon holds the artifact "<key>" containing "the cached resource"
    When the worker looks up "<key>" with "<location>" location and "<discovery>" discovery
    Then the lookup has database identity "<identity>" and delivers "<content>"

    Examples:
      | key               | location   | discovery   | identity | content             |
      | rc-7              | unrecorded | published   | no       | the cached resource |
      | artifact-handle-1 | unrecorded | published   | yes      | the cached resource |
      | build-42-result   | unrecorded | published   | yes      | the cached resource |
      | input-vol         | unrecorded | published   | yes      | the cached resource |
      | rc-42             | unrecorded | unpublished | yes      | unavailable         |
      | rc-42             | recorded   | published   | yes      | the cached resource |
