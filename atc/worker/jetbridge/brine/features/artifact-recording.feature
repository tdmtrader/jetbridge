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
