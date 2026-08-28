@VT-06 @VT-08 @VT-10
Feature: Getting artifacts from the artifact daemon

  Every artifact a step produces lives on one node's disk, behind that node's
  artifact daemon. Everything below is about the only question a consumer ever
  asks: can I get my artifact, and if not, what am I told instead.

  Which URL was requested, which HTTP verb carried it, and how many times a
  peer was polled are all mechanism. None of it appears here.

  Source: jetbridge_storage_behavioral_spec_20260330 — VT-06 (DaemonSetVolume
  StreamOut), VT-08 (compression), VT-10 (handle identity); plus the
  peer-fallback resilience work (artifact_daemon_resilience_20260425 P1) and
  the durable-tier probe, neither of which carries requirement identifiers.

  These scenarios replace `daemon_client_test.go` (16 cases) and
  `volume_daemonset_test.go` (16 cases). Both already used `httptest.Server`,
  so the doubles were already REAL ADAPTERS — a real HTTP server speaking the
  daemon's wire contract. What they were not was consumer-shaped: several
  asserted `gotPath`, `gotMethod`, `gotBody` and `probeHits` captured by the
  handler, which is the recording-double problem wearing a real server's
  clothes. Here the daemon double records nothing. It holds artifacts, or it
  does not, and the scenario asserts what came back.

  Two spec drifts, noted where they bite: VT-06 says StreamOut MUST validate
  that a source node is present, but it now falls through to daemon discovery
  when one is configured; and VT-08 says compression is IGNORED, which stopped
  being true when the gzip pipe landed. The scenarios below describe the code.

  # -------------------------------------------------------------------------
  # Reading an artifact from the node that produced it (VT-06)
  # -------------------------------------------------------------------------

  @VT-06
  Scenario: An artifact comes back from the node that produced it
    Given an artifact daemon
    And it holds the artifact "abc" containing "tar data here"
    And the artifact was produced on node "node-1"
    When a consumer reads the artifact "abc"
    Then the artifact arrives as "tar data here"

  # The daemon serves exactly one key and 404s everything else, so the bytes
  # arriving at all is what proves the right artifact was asked for. That is
  # the whole of what `Expect(r.URL.Path).To(ContainSubstring(...))` was for.
  @VT-06
  Scenario: An artifact comes back from a daemon whose address is already known
    Given an artifact daemon
    And it holds the artifact "rc-42" containing "cached resource data"
    And the ATC already knows the daemon address
    When a consumer reads the artifact "rc-42"
    Then the artifact arrives as "cached resource data"
    And the volume reports the handle "rc-42" on worker "k8s-worker-1"

  @VT-06
  Scenario: A daemon that does not have the artifact says so
    Given an artifact daemon
    And the artifact was produced on node "node-1"
    When a consumer reads the artifact "missing"
    Then the read fails saying "not found"

  @VT-06
  Scenario: An artifact with no recorded producer and nowhere to look fails clearly
    Given an artifact daemon
    And no producing node was ever recorded
    When a consumer reads the artifact "abc"
    Then the read fails saying "no source node known"

  @VT-06
  Scenario: An empty recorded daemon address is a failure, not a malformed request
    Given an artifact daemon
    And the ATC recorded an empty daemon address
    When a consumer reads the artifact "rc-42"
    Then the read fails saying "no source node known"

  # Not migrated, deliberately: VT-06's "retry up to 3 times with 2-second
  # backoff" clause. The consumer-visible form of it — a daemon that drops the
  # first connection still delivers the artifact — is a scenario worth having
  # and no test in either suite had it. It costs a real 2-second sleep, so it
  # belongs in the same pass that decides whether the backoff should be
  # configurable, not smuggled in here.

  # -------------------------------------------------------------------------
  # What the consumer gets back: compressed, filtered, or neither (VT-08)
  # -------------------------------------------------------------------------

  @VT-08
  Scenario: A consumer that asks for compression can gunzip what arrives
    Given an artifact daemon
    And it holds the archive "abc" containing the file "task.yml" with "platform: linux"
    And the artifact was produced on node "node-1"
    And the consumer asks for a gzip-compressed stream
    When a consumer reads the artifact "abc"
    Then the stream is gzip compressed
    And the archive holds "task.yml" containing "platform: linux"

  @VT-08
  Scenario: A consumer that asks for no compression can hand the bytes straight to tar
    Given an artifact daemon
    And it holds the archive "abc" containing the file "README.md" with "hello"
    And the artifact was produced on node "node-1"
    When a consumer reads the artifact "abc"
    Then the stream is not compressed
    And the archive holds "README.md" containing "hello"

  @VT-08
  Scenario: Asking for one file inside an artifact gets that file and nothing else
    Given an artifact daemon
    And it holds the archive "abc" containing the file "README.md" with "# My Repo"
    And the archive "abc" also contains the file "ci/task.yml" with "platform: linux"
    And the archive "abc" also contains the file "src/main.go" with "package main"
    And the artifact was produced on node "node-1"
    And the consumer asks for the sub-path "ci/task.yml"
    And the consumer asks for a gzip-compressed stream
    When a consumer reads the artifact "abc"
    Then the archive holds "ci/task.yml" containing "platform: linux"
    And the archive holds that entry and nothing else

  @VT-08
  Scenario: Asking for the artifact root gets everything in it
    Given an artifact daemon
    And it holds the archive "abc" containing the file "file1.txt" with "aaa"
    And the archive "abc" also contains the file "file2.txt" with "bbb"
    And the artifact was produced on node "node-1"
    When a consumer reads the artifact "abc"
    Then the archive holds exactly 2 entries

  # A quirk worth stating out loud, because a consumer will meet it: asking for
  # a file the artifact does not contain is NOT an error. The read succeeds and
  # the archive is empty, so the caller finds out by unpacking nothing. Nobody
  # decided this; it falls out of filtering a tar stream. It is here so that
  # changing it is a decision rather than an accident.
  @VT-08
  Scenario: Asking for a file that is not there succeeds with an empty archive
    Given an artifact daemon
    And it holds the archive "abc" containing the file "README.md" with "hello"
    And the artifact was produced on node "node-1"
    And the consumer asks for the sub-path "nonexistent.yml"
    And the consumer asks for a gzip-compressed stream
    When a consumer reads the artifact "abc"
    Then the read succeeds
    And the archive is empty

  # -------------------------------------------------------------------------
  # When the producing node is gone (peer fallback)
  # -------------------------------------------------------------------------

  # The producing node is the one thing in this system guaranteed to disappear:
  # spot preemption, a crash, a drain. What must survive is the build.
  Scenario: A mirrored copy is served when the producing node has left the cluster
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing "peer-served-content"
    And the node that produced the artifact has left the cluster
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the artifact arrives as "peer-served-content"

  # The failure has to name the situation. "connection refused" would send an
  # operator to the network; the artifact is simply gone.
  Scenario: When the node is gone and nobody mirrored it, the failure says so
    Given an artifact daemon
    And the node that produced the artifact has left the cluster
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the read fails saying "or any peer"
    And the read does not fail saying "connection refused"

  # The replacement for "the happy path performs zero peer probes", which
  # counted HEAD requests against the handler. The two copies are deliberately
  # given DIFFERENT contents so the scenario can name which one the consumer
  # received. A build that quietly reads a stale mirror instead of the
  # producer's own copy is the bug that assertion was guarding against, and
  # this states it in the consumer's own terms.
  Scenario: The producer's own copy wins over a mirrored one
    Given an artifact daemon
    And it holds the artifact "handle/output" containing "producer-content"
    And it holds a mirrored copy of the artifact "handle/output" containing "stale-peer-content"
    And the artifact was produced on node "node-1"
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the artifact arrives as "producer-content"

  # -------------------------------------------------------------------------
  # When the ATC restarted and forgot where the artifact was
  # -------------------------------------------------------------------------

  # A web restart wipes the in-memory locator, so every pre-restart artifact is
  # re-wrapped with no producing node at all. Before this fell back to
  # discovery, an agent step reading its own sidecar config during resume
  # failed instantly while the claude process it had lost track of kept
  # spending.
  Scenario: An artifact is found by asking every daemon when the locator was wiped
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing "daemon-served-content"
    And no producing node was ever recorded
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the artifact arrives as "daemon-served-content"

  Scenario: When nothing was recorded and no daemon has it, the failure says that
    Given an artifact daemon
    And no producing node was ever recorded
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the read fails saying "not found on any daemon"

  # -------------------------------------------------------------------------
  # Finding which daemon holds a resource cache
  # -------------------------------------------------------------------------

  # A probe hit is only worth anything if the address it names can actually
  # serve the bytes. Asserting the returned IP equals the fixture's IP proves
  # the fixture; fetching from it proves the probe.
  Scenario: A cached resource is fetchable from the daemon the probe names
    Given an artifact daemon
    And it holds the resource cache "rc-42"
    And it holds the artifact "rc-42" containing "cached resource data"
    When a consumer fetches the resource cache "rc-42" from wherever the probe finds it
    Then the artifact arrives as "cached resource data"

  Scenario: A cache no daemon holds is reported as a miss
    Given an artifact daemon
    When the ATC probes for the resource cache "rc-999"
    Then the probe reports a miss

  Scenario: A cluster with no daemons at all is a miss, not an error
    Given a cluster with no artifact daemons
    When the ATC probes for the resource cache "rc-1"
    Then the probe reports a miss

  # A 200 from this probe means exactly one thing: these bytes are on this node
  # right now. An earlier version fell back to POST /resolve, which answered
  # yes off a fetch from a PEER — so a daemon holding nothing locally could win
  # the race and get bound to as the source. Here the daemon answers /resolve
  # enthusiastically and holds nothing; a hit would mean the fallback is back.
  #
  # The other half of that regression — /resolve also wrote a full copy of the
  # artifact into the daemon pod's /tmp, outside the swept storage path, where
  # nothing ever reclaimed it — has no consumer-visible form at this seam. It
  # is observable only from inside the daemon, and belongs to the daemon's own
  # suite.
  Scenario: A daemon that can only resolve from peers is not a cache hit
    Given an artifact daemon
    And it answers resolve requests but holds nothing locally
    When the ATC probes for the resource cache "rc-42"
    Then the probe reports a miss

  # Capability has to be learned from a response the ATC already sends, and
  # from ANY status. A daemon answering 404 for this key is still the daemon
  # that can warm it; reading the header only off a 200 would mean the
  # capability is known exactly when it is not needed.
  Scenario: A durable tier is learned from a miss, not just from a hit
    Given an artifact daemon
    And it advertises a durable tier
    When the ATC probes for the resource cache "rc-42"
    Then the probe reports a miss
    And the daemon is known to have a durable tier
    And the probe carries back 1 daemon addresses

  Scenario: A daemon that never advertised a durable tier is not credited with one
    Given an artifact daemon
    When the ATC probes for the resource cache "rc-42"
    Then the probe reports a miss
    And the daemon is not known to have a durable tier

  # -------------------------------------------------------------------------
  # NOT WHOLE: asking a daemon to mirror has no scenario
  # -------------------------------------------------------------------------
  #
  # Reading a mirrored copy is covered above. Asking for one is not, and the
  # gap is deliberate rather than overlooked, so it is written down here.
  #
  # DaemonClient.TriggerMirror returns nil on every path BY CONTRACT — 202,
  # non-202, transport failure and a request that could not even be built all
  # return nil, because failing to schedule a mirror must not fail a step that
  # already succeeded. Five step definitions existed for its branches and no
  # scenario ever used them; they were deleted in the vocabulary pass rather
  # than left standing as coverage that was not there (recover them from git
  # if this is picked up).
  #
  # They were not simply wired to scenarios because the only assertion those
  # steps could make was "the producing step is not failed", and against a
  # function that always returns nil that assertion cannot fail. Five green
  # scenarios asserting nothing is worse than an acknowledged gap.
  #
  # Closing it needs a decision about this file's double, which records
  # NOTHING on purpose (see the header of ../steps/daemon.go). Either:
  #   - it records the keys it was asked to mirror, and the scenarios assert
  #     the request arrived — which is the recording-double pattern the header
  #     rejects, defensible here only because there is no output to assert on;
  #   - or the double actually mirrors, moving the artifact into the set it
  #     serves as a mirrored copy, and the scenarios assert the copy can then
  #     be fetched — an output assertion, but a behavioural difference beyond
  #     "it holds its artifacts in a map", since a real daemon mirrors to
  #     peers rather than to itself.
  #
  # The second is truer to the header. Neither is a decision to make in
  # passing, which is why this is a note and not a scenario.

  # -------------------------------------------------------------------------
  # Finding which daemon holds a step artifact
  # -------------------------------------------------------------------------

  # The step-artifact probe has exactly one consumer in the tree: the volume's
  # peer fallback. So its HIT case is not a scenario of its own — it is the
  # four fallback scenarios above, which fetch through it. The ginkgo test for
  # the hit asserted `daemonIP == host`, which proves the fixture rather than
  # the probe. Addendum 2's "routing" row: redundant once the round trip
  # exists, and deleted rather than translated.

  Scenario: An artifact no daemon holds is reported as a miss with no address
    Given an artifact daemon
    When the ATC probes for a mirrored copy of "handle/output"
    Then the probe reports a miss
    And the probe names no daemon

  Scenario: A cluster with no daemons has no mirrored copy either
    Given a cluster with no artifact daemons
    When the ATC probes for a mirrored copy of "handle/output"
    Then the probe reports a miss
    And the probe names no daemon

  Scenario: Two daemons that both miss are still a miss
    Given an artifact daemon
    And the daemon address is published twice
    When the ATC probes for a mirrored copy of "handle/output"
    Then the probe reports a miss

  # An unreachable peer must not cost the build the artifact a live one has.
  Scenario: One live daemon wins over an unreachable one
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing "peer-served-content"
    And a second daemon address that never answers
    And the node that produced the artifact has left the cluster
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the artifact arrives as "peer-served-content"

  # -------------------------------------------------------------------------
  # Asking a daemon to mirror (best-effort by contract)
  # -------------------------------------------------------------------------

  # Mirroring is an optimization. If it does not happen the build's data lives
  # on one node and a node loss forces a rerun — but the step that produced it
  # SUCCEEDED, and must stay succeeded. Every scenario here is that one claim.
  #
  # Not migrated: `TestTriggerMirror_PostsCorrectBody`, which asserted the
  # method, path, body and hit count captured by the handler. It has no
  # consumer-visible equivalent at this seam and this is not a failure of
  # imagination — TriggerMirror returns nil on success, on transport failure
  # and on every non-202 alike, so NOTHING about the request it issued is
  # observable to its caller. It is the PE-08 shape: an integration-boundary
  # contract. Keep it as a Go unit test labelled as one, move it to the live
  # suite, or delete it; do not dress it up as behavior.

  # THESE FOUR SCENARIOS WERE REMOVED because they could not fail.
  #
  # They asserted "the producing step is not failed" for a dead daemon, an
  # erroring daemon, an empty address and a cancelled context. But
  # DaemonClient.TriggerMirror (daemon_client.go:345) returns nil on EVERY
  # path — request-construction failure, transport failure, non-202 and
  # success alike — so the check was constant-false and all four passed for
  # any implementation. Verified by substituting a healthy daemon for the dead
  # one, for the erroring one, and by removing the cancellation: 29/29 still
  # passed each time.
  #
  # The design decision they describe is real and deliberate: a mirror is
  # best-effort and must never fail a build. But a scenario that cannot fail
  # does not guard it, and four of them read as four times the assurance.
  #
  # What is genuinely missing, and is NOT covered anywhere in Go or brine now
  # that TestTriggerMirror_PostsCorrectBody is disposed: nothing asserts a
  # mirror request is ever ISSUED, or carries the right key. A
  # `func TriggerMirror(...) error { return nil }` would satisfy the entire
  # surviving suite. Stating that gap is worth more than four green lines.
