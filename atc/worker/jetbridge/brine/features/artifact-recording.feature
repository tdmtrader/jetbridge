Feature: Recording where a step's outputs went

  A step's outputs never move. They are written straight onto the node's disk
  through a hostPath mount, and everything downstream depends on the ATC having
  written down WHERE — which node, and which directory on it. That note is the
  artifact index, and it is what the next step's pod is built from: the
  directory its init container asks the daemon for, and the node the scheduler
  is asked to prefer so the fetch never leaves the machine.

  The same moment does two more things. It tells the producing node's daemon
  that the artifact key names that directory, so a later read by handle
  resolves; and it asks that daemon to push a copy to its peers, so losing the
  node does not lose the build's output.

  Source: storage_daemonset_test.go — the DaemonSetBackend cases that describe
  a step's outputs rather than the shape of a constructor's return value.

  Nothing here asserts that a request was made. The daemons below are real HTTP
  servers, and a mirror really lands on a real peer, so "it was asked to
  mirror" is stated the only way a consumer can observe it: the copy is
  fetched, from the other node, over the wire. See ../steps/artifact_recording.go.

  # =========================================================================
  # The index a finished step leaves behind
  # =========================================================================

  # The presence half of a claim brine only ever made negatively.
  # failure-priority.feature asserts a HALF-WRITTEN artifact stays unlocatable;
  # nothing asserted a finished one becomes locatable, and an absence assertion
  # with no matching presence assertion passes just as happily against a
  # RecordOutputs that does nothing at all.
  #
  # Two separate things have to be true for the read below to succeed, and
  # either one failing alone is a silent loss: the index has to remember which
  # node holds the artifact, and that node's daemon has to have been told that
  # the artifact's key names the directory the step wrote. Neither is visible
  # from outside; the artifact arriving is.
  Scenario: A finished step's output can be read back by name from the node that ran it
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "vol-result" holding "compiled binary"
    When the worker records where the step's outputs went
    Then the output "vol-result" reads back as "compiled binary"

  # The seam between the two halves: what one step recorded is what the next
  # step's pod is told to fetch. The consuming step knows the artifact only by
  # its own volume handle, and no daemon has ever heard of that — the data is
  # filed under the PRODUCING step's handle and output name. Asking for the
  # wrong one is a fetch that 404s in an init container, which surfaces as a
  # step that never starts.
  #
  # The preference is the other half. A step is steered toward the node that
  # already holds its inputs so the fetch reads local disk instead of crossing
  # the network with the whole artifact. Nothing in brine asserted it: no
  # feature file mentioned kubernetes.io/hostname or a preferred scheduling
  # term at all, only the HARD requirement that some artifact cache be present.
  Scenario: The next step fetches its input from the directory the producing step wrote it to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "vol-result" holding "compiled binary"
    And a later step "consume-42" takes the artifact "vol-result" at "/tmp/build/workdir/from-earlier"
    When the worker records where the step's outputs went
    And that step's pod is built
    Then that fetch asks the daemon for "build-42/result"
    And that fetch does not ask the daemon for "vol-result"
    And the pod prefers the node "node-1"

  # The contrast that gives the preference above its teeth. Without it, an
  # assertion that SOME node is preferred cannot tell a preference derived from
  # where the inputs are from one that names a node at random — and a step that
  # reads nothing has no reason to be pinned anywhere. The index is populated
  # here on purpose: the question is whether the preference comes from this
  # step's inputs, not whether the index is empty.
  Scenario: A step that reads nothing is not steered to any node
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "vol-result" holding "compiled binary"
    And a later step "unrelated" takes no inputs
    When the worker records where the step's outputs went
    And that step's pod is built
    Then the pod expresses no preference about where it runs

  # Batching is the behaviour, and it was unasserted. brine's existing check
  # only says SOME init container other than the cleanup one exists, which
  # would pass just as happily with one container per input — that is one image
  # pull and one serial round trip per input, before the step's own command
  # gets to start. A ten-input task pays it ten times.
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

  # =========================================================================
  # What a check container is not given
  # =========================================================================

  # A second, independent isolation channel beside the one container-pod.feature
  # already covers. That one says a check's WORKING DIRECTORY is ephemeral.
  # This says a check's pod is not handed the whole node's artifact store —
  # every other step's outputs, read AND write, mounted into a container that
  # runs the resource author's code on a schedule and reuses one handle
  # forever.
  #
  # It is a genuinely separate guard: the workspace comes from the container's
  # METADATA type and the store mount from the container SPEC's type, and the
  # scenario in container-pod.feature leaves the spec's type unset, so it
  # cannot see this at all.
  Scenario: A check's pod is not handed the node's artifact store
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later check "check-vulnerable" takes no inputs
    When that step's pod is built
    Then the pod is not given the node's artifact store

  # The contrast: the same worker DOES give a task the store, because a task's
  # init containers have to put the inputs they fetch somewhere. Without this,
  # "not given the store" cannot be told apart from a worker that has no
  # artifact store configured.
  Scenario: A task's pod on the same worker is handed it
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "task-with-store" takes no inputs
    When that step's pod is built
    Then the pod is given the node's artifact store

  # =========================================================================
  # Copying outputs off the node that produced them
  # =========================================================================

  # ../features/artifact-daemon.feature records, in prose, that asking a daemon
  # to mirror had NO scenario anywhere in brine: "a
  # func TriggerMirror(...) error { return nil } would satisfy the entire
  # surviving suite". It also names the honest way to close that — a daemon
  # double that really mirrors, to a real peer, so the copy can be fetched
  # afterwards. The two scenarios below are that.
  #
  # What is at stake is a whole build's work. Every artifact a step produces
  # lives on exactly one node's disk; without the copy, losing that node — a
  # spot reclaim, a drain, a crash — loses the outputs and forces a rerun of
  # everything upstream. The copy is best-effort by design, which is precisely
  # why nothing fails when it is skipped, and precisely why it needs a scenario:
  # skipping it is silent.
  #
  # The key is the other half. The daemon is asked for "<handle>/<output>",
  # which is where the data is on disk — NOT the volume handle the ATC knows
  # the artifact by. Asking with the handle mirrors nothing at all, and reports
  # success while doing it.
  Scenario: A step's output is copied to a second node
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a second node whose daemon can hold mirrored copies
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "vol-result" holding "compiled binary"
    When the worker records where the step's outputs went
    Then the other node holds a copy of the output "result" containing "compiled binary"

  # The realistic shape — a task producing a binary, a report and logs — where
  # the failure mode is copying the first and stopping. One survivor out of
  # three still loses the build.
  Scenario: Every output is copied, not just the first
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a second node whose daemon can hold mirrored copies
    And the step "release" ran on node "node-1"
    And its output "binary" is the volume "vol-binary" holding "the compiled binary"
    And its output "report" is the volume "vol-report" holding "the test report"
    And its output "logs" is the volume "vol-logs" holding "the build logs"
    When the worker records where the step's outputs went
    Then the other node holds a copy of the output "binary" containing "the compiled binary"
    And the other node holds a copy of the output "report" containing "the test report"
    And the other node holds a copy of the output "logs" containing "the build logs"

  # DISPOSITION — TestDaemonSetBackend_RecordOutputs_TriggerMirrorFailureDoes-
  # NotPanic is not here, and this file is where the reason belongs. It stands
  # a daemon up that answers /mirror with 500 and asserts the step keeps its
  # output anyway.
  #
  # Nothing about that 500 is observable. DaemonClient.TriggerMirror returns
  # nil on 202, on non-202 and on transport failure alike — deliberately, so a
  # mirror can never fail a step that already succeeded — and RecordOutputs
  # neither reads the result nor branches on it. A scenario with the refusal
  # and one without produce identical outcomes, so it could only ever restate
  # what "A step's output is copied to a second node" already proves, and no
  # single change to production could redden it alone.
  #
  # ../features/artifact-daemon.feature removed four scenarios for exactly this
  # reason and wrote down why; this is the fifth, caught before it was written.

  # Registering a resource cache is the same story at a different call site,
  # and it is the one no brine step reached at all: the closing suite calls
  # DaemonClient.RegisterAlias directly and never goes through the backend, so
  # the derivation of the cache's directory from the get step's volume handle
  # was unguarded. Getting that wrong is not a quiet loss — every daemon
  # answers "path not found" and the registration fails outright, which is what
  # the first Then is about.
  Scenario: A cache registered for a get step is copied off its node too
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a second node whose daemon can hold mirrored copies
    And the step "get-handle" ran on node "node-1"
    And its output "dir" is the volume "get-handle-dir" holding "the fetched resource"
    When the worker registers the resource cache "rc-42" for that step's output
    Then registering the cache succeeded
    And the other node holds a copy of the output "dir" containing "the fetched resource"

  # DISPOSITION — the ORDERING halves of the two ginkgo cases above are not
  # here, and this is not an oversight.
  #
  # TestDaemonSetBackend_RecordOutputs_TriggersMirrorAfterAlias asserts POST
  # /register precedes POST /mirror; _RegisterResourceCache_TriggersMirror-
  # BeforeAlias asserts the exact opposite order at the adjacent call site.
  # Both are real rules and both were checked by capturing a call order inside
  # the handler.
  #
  # Neither has an outcome. The two requests touch disjoint state in a real
  # daemon — /register writes the alias registry, /mirror reads the steps tree
  # and never consults an alias — so no answer, no artifact and no error
  # differs on the order they arrive in. What the RecordOutputs rule is really
  # about is a race INSIDE the daemon, between the trigger and the daemon's own
  # mirror run, which the ATC cannot observe at all. And the RegisterResource-
  # Cache rule survives being inverted because RegisterAlias walks every daemon
  # until one accepts: a peer that has not received the copy yet answers 404
  # and the producer, which always has the data, answers 201 either way.
  #
  # Recording the order would need a double that records, which is the pattern
  # ../steps/daemon.go's header rejects. The mirror-happens-at-all half is
  # migrated above, where it has a real outcome; the ordering halves stay in Go.

  # =========================================================================
  # Looking a resource cache up
  # =========================================================================

  # A cache key names data the current web process may know nothing about: the
  # get step that filled it can have run in another build, in another ATC, long
  # ago. The index would be empty, and a lookup that stopped there would hand
  # back a volume bound to no node — a hard failure on read with the bytes
  # sitting one probe away. So a cache-shaped key with no recorded location is
  # looked for by asking the live daemons which one has it.
  #
  # Note what the daemon does and does not hold here: it answers to the cache
  # key through its registry, and has nothing filed under that key in its steps
  # tree. That is what a registered cache actually looks like — the alias
  # points at a get step's output directory — and it is why the read only
  # succeeds if the lookup asks the right question.
  Scenario: A cache the worker has no record of is found by asking the daemons
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And an artifact volume "rc-7" the worker can look up
    And the node's daemon holds the resource cache "rc-7" containing "the cached resource"
    When the worker looks up the volume "rc-7"
    Then the artifact comes back as "the cached resource"

  # And the other side of that gate: when the index DOES know where the cache
  # is, the recorded location is authoritative and the daemons must not be
  # asked.
  #
  # The cost of asking anyway is not the request. A probe answers with a pod
  # address and nothing else, so the volume built from it carries no database
  # row — and every method that writes one, InitializeResourceCache first among
  # them, silently returns nothing. The cache would then be read successfully
  # and never recorded, so the next build repeats the get step, forever, with
  # no error anywhere to explain it.
  Scenario: A cache the worker has already located keeps its database identity
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And an artifact volume "rc-9" the worker can look up
    And the node's daemon holds the resource cache "rc-9" containing "the cached resource"
    And the worker already knows the cache "rc-9" is on node "node-1"
    When the worker looks up the volume "rc-9" and initialises it as a resource cache
    Then the cache is recorded against the worker in the database

  # DISPOSITION — TestDaemonSetBackend_WrapVolumeForLookup_NonRcKeyNeverProbes
  # is not here. It guards against over-probing: an ordinary artifact handle
  # must not cost an EndpointSlice list and a HEAD to every daemon. The test
  # enforces it by failing inside the handler when any request arrives, and
  # there is no outcome behind it — a probe for a key no daemon has an alias
  # for misses, and the lookup then takes exactly the branch it would have
  # taken without probing. Same volume, same bytes, same errors; only the
  # traffic differs. It stays a Go unit test.
