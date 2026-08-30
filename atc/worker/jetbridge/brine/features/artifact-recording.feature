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

  Nothing here asserts that a request was made. Every assertion is on what a
  later read brought back, what the pod carries, or what the database holds.

  The daemon answering is the artifact daemon — cmd/artifact-daemon, built and
  run as a process with its own storage root, the same way
  ../features/artifact-daemon-real.feature runs it. "The node holds this
  output" is therefore files on a disk, and what comes back is what the daemon
  made of them.

  Three scenarios are the exception and say so in their opening Given: the ones
  about copying an output to a SECOND node. A daemon finds the peers it mirrors
  to through EndpointSlices, using a client cmd/artifact-daemon builds with
  rest.InClusterConfig() alone, and the flag that wires the mirror up at all
  makes the process exit outside a cluster — so two real daemons started here
  cannot find each other. Those three run on stand-ins that really do copy to a
  real second server, and the copy is really fetched back from it over the
  wire. See ../steps/artifact_recording.go.

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
  #
  # The daemon is deliberately NOT discoverable here. Against the real daemon
  # the ATC's peer fallback asks /artifacts/steps/<key>, and the daemon answers
  # that by stripping the prefix and retrying the registry — so the alias alone
  # is enough and the index stops being load-bearing. Taking discovery away
  # makes the recorded node the only route again, which is what this scenario
  # is about.
  Scenario: A finished step's output can be read back by name from the node that ran it
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the ATC cannot go looking for daemons it was not told about
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
  # Where a step's data actually lands on the node
  # =========================================================================

  # Everything above describes a step's outputs as though this file knew where
  # they were: a Given puts bytes at steps/<handle>/<output> and the reads take
  # them from there. That is the layout production is SUPPOSED to use, written
  # down twice — once in the pod and once here — so the two can never disagree,
  # and no scenario can see it when production's own two halves do.
  #
  # They are two halves. The directory the pod keeps an output in is built by
  # container.go, which names it after the OUTPUT; the daemon key is built by
  # RecordOutputs, which derives it from the same spec independently. Nothing
  # joins them, and neither is visible from the other. Rename the subdirectory
  # on one side and nothing errors at the time: the node holds the bytes, the
  # index names a directory, and they are different directories.
  #
  # What it costs is a build that fails somewhere else. The alias registration
  # is refused by the daemon — it will not claim a path its node does not hold
  # — and every later read of that artifact 404s on the one machine that has
  # it. The step that suffers is the NEXT one, whose init container asks for a
  # directory that does not exist and stops before its command; the step that
  # was wrong finished green.
  #
  # So the scenarios below stop supplying the layout. The producing step's pod
  # is built, its mounts are followed to the directories they point at, and the
  # bytes are written there — which is what a step does: it writes into the
  # mount it was given, and where that lands is the kubelet's business. Then
  # the ATC records where it thinks the output went, and the read either
  # resolves or it does not. Two independent derivations, one assertion, and no
  # third copy of the layout here to keep them agreeing.

  # A task's named outputs. The failure this closes is a directory named after
  # the VOLUME rather than after the output — steps/<handle>/output-1 instead
  # of steps/<handle>/binary — which is invisible from the pod alone, because
  # the mount the step writes through is at the same place either way. Two
  # outputs rather than one, because a step that produces a binary and a report
  # is the ordinary shape and the index has to be right for both.
  Scenario: A task's outputs are read back from the directories its own pod wrote them to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on node "node-1"
    And it writes "the compiled binary" to its output "binary" in the volume "vol-binary"
    And it writes "the test report" to its output "report" in the volume "vol-report"
    And the bytes reached the node through the mounts the step's own pod gave it
    When the worker records where the step's outputs went
    Then the output "vol-binary" reads back as "the compiled binary"
    And the output "vol-report" reads back as "the test report"

  # The get step, which has no named output at all: the directory it fetched
  # into IS the output, and the index files it under the name "dir". That name
  # is agreed in two places — the pod's subdirectory and the daemon key — and
  # nothing checks that they still say the same word. Call the pod's directory
  # "workdir" while the index goes on saying "dir" and every resource a
  # pipeline gets becomes unreadable by the steps that asked for it, which is
  # most of what a pipeline does.
  Scenario: A get step's resource is read back from the directory its own pod wrote it to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the get step "get-42" ran on node "node-1"
    And it fetched "the fetched resource" into its working directory, which is the volume "get-42-dir"
    And the bytes reached the node through the mounts the step's own pod gave it
    When the worker records where the step's outputs went
    Then the output "get-42-dir" reads back as "the fetched resource"

  # =========================================================================
  # What the next step's fetch does with the daemon's answer
  # =========================================================================

  # The three scenarios below RUN the init container's script. It is
  # production's own text, a real shell reads it, and the assertion is the exit
  # status the kubelet reads — which is its entire contract with the pod: a
  # non-zero exit stops the step before its command, a zero exit lets it
  # through. supervisor_script_test.go already reads the supervisor script this
  # way; nothing had ever read this one.
  #
  # The daemon answering is the artifact daemon itself, asked with the pod's
  # own payload — which is what makes these worth running. The batch names keys
  # the ATC derived and destinations the POD derived, and the daemon has to
  # find the first on its own disk and land a whole directory on the second.
  #
  # Three things are stood in for. The dial, because BusyBox wget is not on the
  # machine running this; the retry backoff, which is not waited out; and the
  # kubelet's creation of the pod's input directories, which have to exist
  # before the daemon will deliver into them. All three are named in
  # ../steps/artifact_recording.go where they are done.

  # An output whose producer's node could not be determined is still an output.
  # The node lookup fails for ordinary reasons — the pod was already gone when
  # the step finished — and the bytes are on a node's disk regardless. What the
  # ATC can still do is remember the DIRECTORY, and that is enough: the daemon
  # resolves a key it was never told about by looking under its own steps tree,
  # so "build-42/result" finds the data and the raw volume handle does not.
  #
  # Skip the index entry because the node is unknown and the next step falls
  # back to asking for the volume handle — a name no daemon has ever heard, for
  # an artifact sitting on the disk of the machine being asked. The fetch 404s,
  # the init container fails, and the build reruns work that was already done.
  Scenario: An output whose node the worker could not identify is still fetched by its directory
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the step "build-42" ran on a node the worker could not identify
    And its output "result" is the volume "vol-result" holding "compiled binary"
    And a later step "consume-42" takes the artifact "vol-result" at "/tmp/build/workdir/from-earlier"
    When the worker records where the step's outputs went
    And that step's pod is built
    And the node's daemon answers its fetch
    Then the fetch succeeded, so the step starts
    And what the step finds at "/tmp/build/workdir/from-earlier" is "compiled binary"

  # And the failure this file exists to make loud. The daemon refuses a batch
  # it could only partly deliver — it answers 500, with an overall status of
  # "error" — and the init container must turn that into a failed build.
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
    And its output "result" is the volume "vol-result" holding "compiled binary"
    And a later step "consume-42" takes the artifact "vol-result" at "/tmp/build/workdir/from-earlier"
    And it also takes the artifact "vol-lost" at "/tmp/build/workdir/lost"
    When the worker records where the step's outputs went
    And that step's pod is built
    And the node's daemon answers its fetch
    Then the fetch failed, so the step never starts

  # Which daemon the pod talks to. The artifact daemon is a DaemonSet: the copy
  # that can serve a hostPath is the one on the machine holding it, and a pod
  # reaches it on the NODE's address, which the kubelet supplies through the
  # downward API. Dial the pod's own loopback instead and the request leaves
  # nothing — the step's network namespace has no daemon in it — so every fetch
  # burns its retry budget and no step with an input ever starts.
  #
  # The assertion is on the address the script builds and on where HOST_IP
  # comes from, not on whether the text "HOST_IP" appears somewhere: a script
  # that mentions it and then dials 127.0.0.1 mentions it just the same.
  Scenario: The fetch dials the daemon on the node its own pod landed on
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "needs-inputs" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    When that step's pod is built
    Then the fetch dials the daemon on the node the pod lands on

  # DISPOSITION — the SINGLE-ARTIFACT resolve script, daemonResolveCommand, has
  # no scenario here and cannot have one. Two rules live only in it: that it
  # dials ${HOST_IP} rather than loopback, and that an empty artifact key makes
  # the init container exit 1 rather than start a step against an input its
  # producer never recorded.
  #
  # Nothing in production calls it. BuildFetchInitContainers builds every pod's
  # fetch from daemonResolveBatchCommand, and the only callers of the
  # single-artifact form are four Go unit tests that invoke the unexported
  # method directly. There is no consumer for a scenario to be about, so the
  # two rules above are asserted on the batch script — which every pod really
  # does run — and the single-artifact form stays where its only callers are.
  #
  # The empty-key rule has no batch counterpart to assert. The batch script
  # carries no such guard, and it does not need one along this path: a producer
  # that recorded nothing leaves the consumer asking for the raw volume handle,
  # which is not empty, and the daemon's "not found" then fails the fetch
  # through the scenario above. An empty key can only be reached by an input
  # whose artifact has no handle at all, which nothing constructs.

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
  # Where the scheduler may put the step, and what it finds when it lands
  # =========================================================================

  # The hard half of the placement rule, and the half nothing in brine could
  # see the VALUE of. ../features/container-pod.feature's "A pod is pinned to a
  # node that can serve its artifacts" walks the required terms looking for the
  # key concourse.dev/artifact-cache and returns the moment it finds one, so a
  # requirement demanding a value no daemon ever writes reads exactly like the
  # correct one.
  #
  # That mistake does not degrade the cluster, it stops it. The label is
  # written by the daemon's own node labeller when it comes up, and the value
  # is "ready"; ask for anything else — or for a different key — and NO node in
  # the fleet matches. Every build pod sits Pending until its timeout, and the
  # only thing the scheduler will say is that no node matched the pod's node
  # affinity.
  #
  # So the assertion is not on the expression. It is on whether a node running
  # an artifact daemon, labelled the way that daemon labels it, satisfies what
  # the pod demands — the question the scheduler itself asks. A requirement
  # nothing can satisfy is invisible from the pod alone and obvious from the
  # pair.
  Scenario: A step may only land on a node whose daemon has declared itself ready
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "needs-a-daemon" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    When that step's pod is built
    Then the node running the artifact daemon can accept the pod

  # The soft half, sharpened. "The next step fetches its input from the
  # directory the producing step wrote it to" already pins a step with ONE
  # located input to that input's node, so dropping the preference outright
  # reddens there as well as here. What a single input cannot show is WHICH
  # node is chosen when a step reads from several, and that is the ordinary
  # case: a task takes a repo, its dependencies and a config, and they are not
  # all in the same place.
  #
  # The rule is the node holding the most of them. Getting it wrong costs
  # nothing visible — the step still runs — it just drags the majority of its
  # inputs across the network first, on every build, and the wider the fan-in
  # the worse the guess.
  Scenario: A step is steered to the node holding most of its inputs
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And the worker already knows the artifact "vol-src" is on node "node-a"
    And the worker already knows the artifact "vol-deps" is on node "node-b"
    And the worker already knows the artifact "vol-config" is on node "node-b"
    And a later step "link" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    And it also takes the artifact "vol-deps" at "/tmp/build/workdir/deps"
    And it also takes the artifact "vol-config" at "/tmp/build/workdir/config"
    When that step's pod is built
    Then the pod prefers the node "node-b"

  # Every directory a step works in is a directory on the node named after the
  # step, and the first time a step runs on a node not one of them is there.
  # The pod says as much: each of those volumes asks the kubelet to create the
  # path if it is missing. Ask instead for a path that must already exist and
  # the kubelet refuses to start the pod — and because the directory is named
  # after a container handle that is new every run, there is no node in the
  # cluster where it does exist. The step fails before its command runs, with
  # an error that never mentions artifacts.
  #
  # Asserted over every node-local directory the pod carries rather than one,
  # because the working directory, the inputs and the outputs are all built by
  # the same call and a regression takes them together.
  Scenario: A step's node-local directories are made on a node that has never held them
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "first-here" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    When that step's pod is built
    Then every directory the pod expects on the node is created if it is missing

  # A task cache and a step's outputs are the same kind of thing on disk and
  # opposite kinds of thing in their lifetime. The steps tree is build-scoped:
  # the daemon's sweeper deletes every child of it once it is older than the
  # TTL, and the mirror walks the same tree and copies what it finds to the
  # peers. A task cache is the one thing on that node meant to OUTLIVE the
  # build, and to stay where it is.
  #
  # File it among the step data and both of those run over it. The cache is
  # swept on the daemon's schedule instead of kept, so every build repopulates
  # it and the feature quietly stops existing; and in the meantime the fleet
  # copies each node's caches to every peer. Nothing errors. The only symptom
  # is that a cached build is never faster than an uncached one.
  Scenario: A task cache is not filed among the step data the daemon sweeps
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "cached-build" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    And it keeps a task cache at "/tmp/build/workdir/.gradle"
    When that step's pod is built
    Then the task cache is filed apart from the step data on that node

  # ../features/container-pod.feature says a retried step is given a cleanup
  # init container. It does not say the cleanup can do anything. That
  # container's entire body is an rm over the node's artifact store, and it
  # reaches the store through a mount — which can be made read-only, at which
  # point the rm fails with EROFS, the init container exits non-zero, and every
  # retry of every reused step dies before its command runs. Swallow that
  # failure instead and the retry meets its own half-written outputs: the
  # "destination path already exists" failure the cleanup exists to prevent.
  #
  # The second Then is the contrast that keeps the first honest. This pod
  # mounts the same store TWICE, and the other mount is read-only on purpose:
  # the fetch container never writes the artifacts itself, it posts the batch
  # and the daemon does the writing. Making every mount writable is not the
  # fix, and this says so.
  Scenario: A retried step clears its workspace through a mount it can write to
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And a later step "retried-build" takes the artifact "vol-src" at "/tmp/build/workdir/src"
    And that step has run here before
    When that step's pod is built
    Then the step's cleanup can really delete what the last attempt left
    And the fetch of its inputs still cannot write there

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
  # And they are the reason this section's daemons are stand-ins while every
  # other section's is the real binary. A daemon mirrors to peers it discovers
  # through EndpointSlices, using a client cmd/artifact-daemon builds with
  # rest.InClusterConfig() alone: there is no --kubeconfig flag, client-go
  # hardcodes the service-account token path, and --node-name — which is what
  # wires the mirror up at all — makes the process exit outside a cluster. Two
  # real daemons started here therefore cannot find each other. Closing that
  # needs a production flag, which is a decision rather than a detail; until
  # then the mirror is asserted the only way it can be, with servers that can
  # be handed a peer.
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
    Given a jetbridge worker whose stand-in daemons can mirror to a peer
    And a second node whose daemon can hold mirrored copies
    And the step "build-42" ran on node "node-1"
    And its output "result" is the volume "vol-result" holding "compiled binary"
    When the worker records where the step's outputs went
    Then the other node holds a copy of the output "result" containing "compiled binary"

  # The realistic shape — a task producing a binary, a report and logs — where
  # the failure mode is copying the first and stopping. One survivor out of
  # three still loses the build.
  Scenario: Every output is copied, not just the first
    Given a jetbridge worker whose stand-in daemons can mirror to a peer
    And a second node whose daemon can hold mirrored copies
    And the step "release" ran on node "node-1"
    And its output "binary" is the volume "vol-binary" holding "the compiled binary"
    And its output "report" is the volume "vol-report" holding "the test report"
    And its output "logs" is the volume "vol-logs" holding "the build logs"
    When the worker records where the step's outputs went
    Then the other node holds a copy of the output "binary" containing "the compiled binary"
    And the other node holds a copy of the output "report" containing "the test report"
    And the other node holds a copy of the output "logs" containing "the build logs"
    # Copying is only half of what happens per output: each also has to be
    # registered with its own node's daemon under the path the disk holds, or
    # a later step reading output 2 or 3 gets a 404 from the node that has the
    # bytes. The Go test asserted both halves per output; asserting only the
    # copies let a change that registers one alias per STEP pass.
    And the output "vol-binary" reads back as "the compiled binary"
    And the output "vol-report" reads back as "the test report"
    And the output "vol-logs" reads back as "the build logs"

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
    Given a jetbridge worker whose stand-in daemons can mirror to a peer
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
  # WHAT THIS PINS, AND WHAT IT DOES NOT.
  #
  # It pins that the probe branch is TAKEN: narrowing resourceCacheKeyPattern,
  # or deleting the branch, reddens it, and the first of those is the silent
  # failure resource_cache_key.go's own comment warns about.
  #
  # It does NOT pin that the probe READ the answer. Dropping the status check
  # from ProbeResourceCache — so every daemon reports a hit — leaves this
  # green, measured twice. The reason is structural: on a hit the volume binds
  # to the probed IP but is ALSO given the daemon client, so peer fallback
  # rescues a wrong binding and the bytes arrive anyway. Publishing a second
  # daemon that holds nothing does not help; that was tried and it still
  # passed. What a gutted probe actually costs is the database identity, and
  # that is the scenario below this one, approached from the other side.
  Scenario: A cache the worker has no record of is found by asking the daemons
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And an artifact volume "rc-7" the worker can look up
    And the node's daemon holds the resource cache "rc-7" containing "the cached resource"
    When the worker looks up the volume "rc-7"
    Then the artifact comes back as "the cached resource"

  # The scenario above says the bytes arrive. It cannot say HOW, because since
  # the daemon became real both routes reach them: the probe finds the alias,
  # and so does the ATC's peer fallback, which asks /artifacts/steps/<key> —
  # a URL the real daemon answers by stripping the prefix and retrying the
  # registry. Deleting the probe branch outright left it green.
  #
  # What separates them is the cost. A probe hit binds the volume by IP, with
  # no database row behind it, so initialising writes nothing and the next
  # build repeats the get step. The fallback path carries the row. So the
  # branch is observable exactly where it hurts.
  #
  # (A first attempt asserted the missing row after a PLAIN lookup, which
  # never initialises anything — the row was absent either way and the check
  # could not fail. It had to be measured to be seen.)
  Scenario: A cache found by asking the daemons arrives with no database identity
    Given a jetbridge worker whose step outputs stay on the node that ran them
    And an artifact volume "rc-7" the worker can look up
    And the node's daemon holds the resource cache "rc-7" containing "the cached resource"
    When the worker looks up the volume "rc-7" and initialises it as a resource cache
    Then the cache arrives without a database identity

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
  # is not here, and the reason first written down was WRONG. It said a probe
  # for an ordinary artifact handle simply misses, so the lookup takes the
  # branch it would have taken anyway — "same volume, same bytes, same errors;
  # only the traffic differs". An audit disproved it.
  #
  # The daemon answers HEAD /resource-caches/{key} out of the SAME registry
  # that POST /register writes, and RecordOutputs registers every step output
  # under its plain volume handle. So an ordinary handle DOES have an alias on
  # its producer's daemon and a probe for it HITS. Remove the isResourceCacheKey
  # gate and an ordinary artifact whose locator entry was lost to a web restart
  # — the exact case DaemonSetVolume.StreamOut's own comment describes — takes
  # the probe branch and comes back as NewDaemonSetVolumeFromIP, which carries
  # no dbVolume. InitializeResourceCache, InitializeStreamedResourceCache and
  # InitializeTaskCache then all silently return nil: the same loss the
  # scenario above this note is about, reached from the other direction.
  #
  # So the rule has an outcome and only the request-counting half does not.
  # It is not here because writing it needs a step for "the worker has
  # forgotten where an output is" that does not exist yet, not because it
  # cannot be written. Until then it stays a Go unit test — which is the one
  # thing the original note got right, for the wrong reason.
