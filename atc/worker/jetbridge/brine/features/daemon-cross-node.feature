Feature: The daemon's own fallback to the node that has the artifact

  A build's inputs are produced on whatever node had capacity, and consumed on
  whatever node had capacity next. When those are different nodes, something
  has to move the bytes. This is that something, at the layer nothing in this
  suite has ever reached: not the ATC asking a second daemon, but a daemon
  asking its PEERS — EndpointSlice discovery, a HEAD probe, a GET of a tar, an
  extraction, and a promotion by rename, all of it inside cmd/artifact-daemon.

  artifact-daemon.feature covers the ATC's half and says twice, in as many
  words, that this half could not be had: "peer discovery is in-cluster-only,
  so two real daemons cannot find each other outside one", and of the other
  half of that same regression, "it is observable only from inside the daemon,
  and belongs to the daemon's own suite". daemon-containment.feature hands back
  two of its own cases for the same reason. All three were right when they were
  written. The daemon has since gained a --kubeconfig flag, so it can be
  pointed at a cluster it is not running inside, and the thing they were
  waiting for is now buildable: two real daemon processes that discover each
  other through a real API server. The bytes arrive on a node that never held
  them, which is a consumer-visible outcome, so it belongs here.

  WHAT IS REAL HERE, which is nearly everything:

    - Two artifact-daemon PROCESSES, built from cmd/artifact-daemon, each with
      its own storage root. The one the consumer talks to runs with
      --kubeconfig, --node-name and --namespace, which is what wires its
      PeerResolver at all.
    - A real kube-apiserver (the suite's envtest control plane, already
      running for pod-watch-real.feature — this feature adds nothing to its
      cost), a real Node object, and a real EndpointSlice, read live on every
      probe. The API server's validation is part of the test: it refuses
      loopback addresses in an EndpointSlice, which no fake clientset ever
      did, and that is the first thing it caught here.
    - A real tar, produced by the holding daemon's own walk of its disk and
      extracted by the asking daemon's own extractor.

  THE ONE PIECE OF SCAFFOLDING is a TCP forwarder, and it is worth being
  precise about it. A daemon probes peers on the port it was itself given
  (main.go passes *port straight to NewPeerResolver) and binds the wildcard, so
  two daemons on one host can never be at the same port and can never reach
  each other. In a cluster the question does not arise: each pod has its own
  network namespace and every daemon is 7780 on its own address. The forwarder
  restores exactly that and nothing more — it binds the published address at
  the asking daemon's port and moves bytes to the holding daemon. It parses no
  request, writes no response and counts nothing. Both ends of every request
  below are real daemons.

  Two things about that address are checked before any scenario runs, because
  both fail as "the artifact did not arrive" — the same symptom as a broken
  fallback, and the one that would send the next reader into the daemon. A
  host with no routable IPv4 at all cannot publish an endpoint a real API
  server will accept, and a host that routes the published address to the
  wildcard listener instead of the specific one has put the asking daemon on
  both ends. Each is reported as itself, in a sentence. Neither is skipped:
  brine has no skip, and a scenario that went green on a host which could not
  run it would be worse than one that says why it cannot.

  WHAT EVERY SCENARIO ASSERTS IS THE OUTCOME: which bytes are in the directory
  the consumer named. Nothing here counts probes or PUTs. The scenario that has
  to tell "served its own copy" apart from "served the peer's" does it by
  giving the two nodes DIFFERENT bytes under the same key and naming which one
  arrived, because a probe count passes just as well against a daemon that
  fetched from the peer and then discarded the answer.

  AND THE MISS IS A SCENARIO. A family in which every fetch succeeds cannot
  tell a working fallback from a daemon that hands back whatever a peer has for
  any key it is asked about, so the key nobody holds is here too, with the
  destination checked for the half-written tree a "best effort" would leave.

  WHAT STAYS IN GO, and why:

    - peers_test.go's retry cases. Fetch retries three times with a backoff
      and succeeds on the third; the only way to stage that is a peer that
      answers 503 twice and then a tar, which is a scripted double, and the
      OUTCOME of a retried fetch is byte-identical to a first-attempt one.
      Nothing a consumer can see distinguishes them.
    - TestPeerProbe_ConcurrentFirstHitWins and TestPeerProbe_UsesShortTimeout.
      Both are assertions about ELAPSED TIME against unreachable addresses —
      "three peers timed out in ten seconds, not thirty". That is a claim
      about how the probe is scheduled, not about what arrives, and a wall
      clock is the only instrument for it.
    - TestPeerFetch_RestrictiveModesNormalized. Modes ARE visible on the
      delivered file, but the archive it needs carries a 0700 directory and a
      setuid bit, and a directory the producing daemon walks cannot be given
      either through this route: tarDirectory skips directories entirely, and
      an artifact with a setuid file is not something a step produces here.
      daemon-containment.feature already covers the same extractor through
      PUT /stream-in, where the archive is authored directly.

    Also noted rather than fixed, because it is not this file's to fix:
    peers_test.go's TestResolveEndpoint_PeerFallback sets up two servers,
    asserts nothing at all, and ends with `_ = localTS // verify it compiles`.
    Its comment says a full end-to-end test would need a fake EndpointSlice
    and is "better suited for a live integration test". That test is the
    first scenario below, and the Go one can go.

  # -------------------------------------------------------------------------
  # The headline: bytes arrive on a node that never held them
  # -------------------------------------------------------------------------

  # Reddens if the daemon stops querying peers on a local miss (delete step 3
  # of resolveOne), if Probe stops finding the peer (drop the "steps/" prefix
  # from the probe URL, or filter the peer out as self), or if Fetch stops
  # promoting what it extracted to the destination the caller named.
  Scenario: An artifact this node never held arrives from the node that has it
    Given two real artifact daemons, this node's and another node's
    And the other node's daemon holds "cross/output" containing "bytes only the other node ever had" at "data.txt"
    When a consumer asks this node's daemon to resolve "cross/output"
    Then the resolved artifact's "data.txt" reads "bytes only the other node ever had"

  # The negative control, and the reason the rest of this file means anything.
  # A daemon that fetched indiscriminately — Probe returning the first peer
  # that answers at all rather than the one that answers 200 — passes every
  # scenario above it and fails this one. So does a daemon that reports a miss
  # after leaving a partial extraction at the destination: the status says
  # not_found either way, and only the directory can tell you which happened.
  #
  # The wording of the refusal is load-bearing too. "connection refused" would
  # send an operator to the network; the artifact is simply nowhere.
  Scenario: A key no node holds is a miss, not somebody else's artifact
    Given two real artifact daemons, this node's and another node's
    And the other node's daemon holds "cross/output" containing "bytes only the other node ever had" at "data.txt"
    When a consumer asks this node's daemon to resolve "absent/output"
    Then the resolve is refused with 404
    And the refusal names "not found on this node or any peer"
    And nothing was left at the destination

  # -------------------------------------------------------------------------
  # Which copy wins
  # -------------------------------------------------------------------------

  # The two nodes hold the same key with DIFFERENT bytes, which is the only
  # way to name which one the consumer received. Counting peer probes was the
  # other option and it is weaker: it passes against a daemon that fetches
  # from the peer and then throws the answer away, and it fails against a
  # daemon that probes for a reason that has nothing to do with this build.
  #
  # This is not hypothetical. A daemon quietly preferring a peer's older copy
  # is a build reading inputs that its own node has already superseded, and
  # the same defect on the ATC side already has a scenario in
  # artifact-daemon.feature ("The producer's own copy wins over a mirrored
  # one"). This is the daemon-side twin of it.
  #
  # Reddens if resolveOne consults peers before its own filesystem, or if the
  # local filesystem branch stops short-circuiting.
  Scenario: The node's own copy wins over the other node's different one
    Given two real artifact daemons, this node's and another node's
    And the other node's daemon holds "shared/output" containing "the other node's older bytes" at "data.txt"
    And this node's daemon already holds "shared/output" containing "this node's own bytes" at "data.txt"
    When a consumer asks this node's daemon to resolve "shared/output"
    Then the resolved artifact's "data.txt" reads "this node's own bytes"

  # -------------------------------------------------------------------------
  # What survives the trip
  # -------------------------------------------------------------------------

  # An artifact is not a bag of bytes, it is a tree, and the trip flattens it
  # into a tar and back. Links are where that round trip has actually broken:
  # hard links were dropped SILENTLY by the extractor for a whole release —
  # the entry fell through a switch, the extraction still reported success,
  # and the caller got a tree missing files it had asked for.
  #
  # Reddens if the producer follows links instead of recording them (the
  # target check fails on a plain file, even though the content is identical),
  # if the extractor stops materializing symlink entries, or if containment
  # starts refusing a legal sibling target — that last one fails the whole
  # fetch, which is exactly right: refusing everything is not containment.
  Scenario: A link inside the artifact is still a link on the far side
    Given two real artifact daemons, this node's and another node's
    And the other node's daemon holds "linked/output" containing "the bytes behind the link" at "real.txt"
    And that artifact has a link "alias.txt" pointing at "real.txt"
    When a consumer asks this node's daemon to resolve "linked/output"
    Then the resolved artifact's "alias.txt" is a link to "real.txt"
    And the resolved artifact's "alias.txt" reads "the bytes behind the link"

  # One file under two names. Package caches and node_modules are full of
  # them, and "arrives once" is not a smaller version of the artifact — it is
  # a step that fails looking for a file the producer definitely wrote.
  #
  # Reddens if the producing walk starts skipping names that share an inode,
  # or if it starts emitting the second name as a tar hard-link entry and the
  # extractor goes back to dropping that type.
  Scenario: A file the artifact names twice arrives under both names
    Given two real artifact daemons, this node's and another node's
    And the other node's daemon holds "twice/output" containing "one file, two names" at "first.txt"
    And that artifact names the file "first.txt" a second time as "second.txt"
    When a consumer asks this node's daemon to resolve "twice/output"
    Then the resolved artifact's "first.txt" reads "one file, two names"
    And the resolved artifact's "second.txt" reads "one file, two names"

  # -------------------------------------------------------------------------
  # Size
  # -------------------------------------------------------------------------

  # Twelve megabytes is twelve times the largest request body this daemon will
  # read: every JSON handler wraps its body in a MaxBytesReader at 1 MiB. The
  # artifact routes are streamed and deliberately not capped that way, and the
  # distinction is invisible until an artifact crosses the line — at which
  # point every build with a real dependency tree fails and no test says why.
  #
  # The content is a deterministic non-repeating stream, so the digest catches
  # what a length check cannot: a truncated transfer that was padded, or a
  # block delivered twice. Reddens if a body cap is applied to the artifact
  # routes or to the peer fetch, if the fetch timeout stops accommodating a
  # transfer of this size, or if the tar is closed before the last block.
  Scenario: An artifact far larger than any request the daemon reads still arrives whole
    Given two real artifact daemons, this node's and another node's
    And the other node's daemon holds "big/output" containing 12 megabytes at "big.bin"
    When a consumer asks this node's daemon to resolve "big/output"
    Then the resolved artifact's "big.bin" is 12582912 bytes
    And the resolved artifact's "big.bin" is byte-for-byte what the other node wrote
