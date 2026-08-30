Feature: Copying a step's output off the node that made it

  Every artifact a step produces lives on exactly one node's disk. Losing that
  node — a spot reclaim, a drain, a crash — loses the outputs and forces a
  rerun of everything upstream, so the daemon copies each one to a peer as soon
  as it settles. The copy is best-effort by design: nothing fails when it is
  skipped, which is precisely why skipping it is silent, and precisely why it
  needs a scenario.

  ../features/artifact-daemon.feature has carried this as a WRITTEN-DOWN GAP
  since the migration began: "asking a daemon to mirror has no scenario". From
  the ATC's side it cannot be closed at all. DaemonClient.TriggerMirror returns
  nil on 202, on non-202, on a transport failure and on a request it could not
  even build — deliberately, so that failing to schedule a copy never fails a
  step that already succeeded — so `func TriggerMirror(...) error { return nil }`
  satisfies every ATC-side assertion there is.

  The assertion therefore has to be made at the other end: ask the producer,
  then READ THE ARTIFACT OFF THE PEER. Both are real artifact-daemon processes
  with storage roots of their own, and every Then below reads the second one's
  disk — which files came with the copy, and what they say. Nothing counts
  requests.

  # -------------------------------------------------------------------------
  # The two nodes, and the one piece of scaffolding between them
  # -------------------------------------------------------------------------
  #
  #   producer — holds the output and is asked to copy it. Started with
  #              --kubeconfig, --node-name, --namespace and a --service-name of
  #              its own, so its peers come from a real EndpointSlice on the
  #              suite's real API server. Until --kubeconfig landed this was
  #              impossible: peer discovery goes through EndpointSlices,
  #              main.go built that client from rest.InClusterConfig() alone,
  #              and --node-name — which is what wires the mirror up at all —
  #              made the process exit outside a cluster.
  #   peer     — the other node's daemon. No --node-name, so it builds no
  #              Kubernetes client, has no peers of its own and cannot pass a
  #              copy on. What arrives there arrived from the producer.
  #
  # One TCP forwarder sits between them, and it is the same one the cross-node
  # scenarios use. A daemon PUTs to peers on its OWN --port (main.go hands
  # *port to NewMirror) and binds the wildcard, so two daemons on one host can
  # only be told apart by the address they answer on. In a cluster the problem
  # does not exist — every pod has its own network namespace and every daemon
  # is 7780 on its own address. The forwarder restores that: it parses nothing,
  # answers nothing, records nothing, and a check fetches an artifact only the
  # peer holds through the published address before a scenario's first step
  # runs — so a host that will not let the two listeners coexist says so in one
  # sentence rather than leaving six scenarios to fail as "the copy never
  # came".
  #
  # A copy is SCHEDULED, not performed inline — POST /mirror answers 202 before
  # the tar walk starts — so every arrival here is polled to a deadline, and
  # every scenario that asserts no copy watches the peer for two seconds and
  # fails the moment one appears.

  # -------------------------------------------------------------------------
  # The copy
  # -------------------------------------------------------------------------

  # The headline: the gap closed. Two files, one of them a directory down,
  # because the realistic failure is a walk that copies the top level and
  # stops — and a truncated copy that reports success is worse than no copy at
  # all, since the build that later reads it finds a directory missing half its
  # contents and no error anywhere.
  Scenario: An output asked for is copied to the other node, whole
    Given a daemon that mirrors, and the node it mirrors to
    And the producer's disk holds the output "build-42/result" with the file "binary" reading "the compiled binary"
    And the producer's disk holds the output "build-42/result" with the file "reports/junit.xml" reading "one down"
    When the ATC asks it to mirror "build-42/result"
    Then the daemon accepted the mirror request
    And the peer held nothing under that key before
    And the copy arrives on the peer under the key "build-42/result"
    And the copy carries 2 files
    And the copy's file at "reports/junit.xml" reads "one down"

  # The path that carries real traffic. An ordinary step's output arrives by
  # PUT /stream-in, and the daemon copies it onward WITHOUT being asked —
  # nothing in the ATC calls /mirror for it. A daemon that only copied on
  # request would leave every ordinary output on one disk while every dashboard
  # and every metric said replication was on.
  Scenario: A write is copied onward with nobody asking
    Given a daemon that mirrors, and the node it mirrors to
    When a step streams the output "streamed/result" into the producer with the file "data.txt" reading "nobody asked for this copy"
    Then the peer held nothing under that key before
    And the copy arrives on the peer under the key "streamed/result"
    And the copy's file at "data.txt" reads "nobody asked for this copy"

  # A step that produces nothing still registers a volume, and the next step
  # still asks for it. An empty output that fails to copy — a tar of an empty
  # directory the receiver will not take — turns a legal build into one that
  # cannot find its own input after a node is lost.
  Scenario: An output with nothing in it is copied too
    Given a daemon that mirrors, and the node it mirrors to
    And the producer's disk holds the empty output "check-99/result"
    When the ATC asks it to mirror "check-99/result"
    Then the peer held nothing under that key before
    And the copy arrives on the peer under the key "check-99/result"
    And the copy carries 0 files

  # The realistic shape of a step's tail: several outputs finished together and
  # all handed over before any has been copied.
  #
  # WHAT IT DOES NOT PIN, corrected after an audit measured it. This was
  # written claiming to catch "a queue that overflows, a pool that stops taking
  # work". It cannot. WorkerPool's channel holds 64 and --mirror-concurrency
  # defaults to 4, so ten submissions leave four in flight and six queued —
  # nowhere near full — and Submit has no drop path to regress: when the
  # channel IS full it blocks. Reaching that property needs upwards of seventy
  # keys against peers slow enough to keep the workers busy, which is a
  # different scenario and a much slower one.
  #
  # What it does pin is worth keeping on its own: every key handed over in one
  # burst arrives, so a mirror that serves the first and forgets the rest is
  # caught. That is a real failure and no single-key scenario sees it.
  Scenario: Ten outputs handed over at once all arrive
    Given a daemon that mirrors, and the node it mirrors to
    And the producer's disk holds 10 outputs, each with a file of its own
    When the ATC asks it to mirror all 10 of them at once
    Then every one of those copies arrives on the peer

  # -------------------------------------------------------------------------
  # The two negatives, which are what make the four above mean anything
  # -------------------------------------------------------------------------
  #
  # A daemon that copied every artifact to every peer it could see would pass
  # every scenario in the section above. These are the two it fails.
  #
  # NOT WHOLE: the re-fanout refusal. A copy that ARRIVED from a peer must not
  # be mirrored onward, or two daemons trade a key forever. This file first
  # claimed that was visible only as a request count and so could not be a
  # scenario here. An audit showed the claim is false, and gave the shape:
  # give the peer its own --kubeconfig, --node-name, --namespace and
  # --service-name, add a THIRD plain daemon, and forward to it the way the
  # producer is already forwarded to. The assertion is then "the third node's
  # disk stays empty", with the producer-to-peer hop in the same scenario as
  # its positive control — pure outcome, nothing counted. It is not written
  # because it wants a three-node topology this file does not yet build, not
  # because it is unreachable.

  # --mirror-replicas=0 is how an operator turns replication off. Both halves
  # matter: no copy is made, AND the request is still accepted — the ATC
  # discards the answer, so a daemon that refused would be failing mirrors
  # invisibly.
  #
  # ON WHAT THE ABSENCE HALF CAN AND CANNOT CATCH, corrected after an audit
  # traced it. Two independent guards stop the copy: main.go never enters its
  # `if *mirrorReplicas != 0` block, so no Mirror is built; and Mirror.Trigger
  # opens with `if m == nil || m.replicas == 0 { return }`. Either alone is
  # sufficient, so removing either alone leaves this green. It is defence in
  # depth, and the scenario pins the OUTCOME an operator relies on rather than
  # either guard — an earlier version of this comment claimed it caught both,
  # which was an AND read as an OR.
  Scenario: With its mirror switched off a daemon copies nothing and still takes the request
    Given a daemon with its mirror switched off, and the node it would mirror to
    And the producer's disk holds the output "no-mirror/result" with the file "f.txt" reading "stays where it was made"
    When the ATC asks it to mirror "no-mirror/result"
    Then the daemon accepted the mirror request
    And no copy ever arrives on the peer under the key "no-mirror/result"
    And the producer still serves the output it was asked to mirror

  # The rule that makes replication mean replication. Every daemon's OWN
  # endpoint is in the EndpointSlice it reads, so without this filter
  # --mirror-replicas=2 buys a second copy on the same disk as the first: the
  # numbers all look right and the node is still a single point of loss.
  # POD_IP, from the downward API, is the only way a daemon learns which
  # address is its own.
  #
  # On one host with one routable address, the address published for the peer
  # is also the address the producer answers on — the forwarder is what lets
  # one address mean both — so a producer told that address is its own has been
  # told the only peer it can see is itself. What must follow is that it copies
  # nothing, rather than counting a copy on its own disk as replication.
  Scenario: A daemon told an address is its own does not copy to it
    Given a daemon told the peer's address is its own, and the node it would mirror to
    And the producer's disk holds the output "solo/result" with the file "f.txt" reading "one copy is no copies"
    When the ATC asks it to mirror "solo/result"
    Then the daemon accepted the mirror request
    And no copy ever arrives on the peer under the key "solo/result"
    And the producer still serves the output it was asked to mirror

  # -------------------------------------------------------------------------
  # WHAT STAYED IN GO, and why
  # -------------------------------------------------------------------------
  #
  # - TestMirrorJob_PutsCarryMirrorOriginHeader, and the matching refusal in
  #   handleStreamIn to re-trigger on a write that carries the header. Without
  #   it two daemons trade an artifact indefinitely, which is a real defect —
  #   but a re-fanout delivers the copy to a daemon that ALREADY HAS IT, so no
  #   artifact, no answer and no error differs either way. It is visible only
  #   as a request count, on any number of nodes, and a scenario for it here
  #   could only be a recording double. Left in Go by the rule, not by any
  #   limitation of this topology.
  #
  # - The Mirror.Evacuate family: flushing the unmirrored on preemption notice,
  #   respecting the budget, refusing new work afterwards. Evacuation fires
  #   from exactly one place, the preemption watcher's callback, and main.go
  #   builds that watcher with DefaultPreemptionMetadataURL — a constant naming
  #   metadata.google.internal, with no flag to point it elsewhere. The path
  #   cannot be entered from outside the process at all, so the behaviour is
  #   not expressible here at any price; its Go tests assert the list of keys a
  #   peer was PUT, which is a request record in any case. Closing this needs a
  #   production seam (an overridable metadata URL, or an evacuate verb), which
  #   is a decision rather than a detail.
  #
  # - The per-peer outcome vocabulary — ok / rejected / unreachable — and the
  #   worker pool's concurrency and drain semantics. Both are unexported state,
  #   and the artifact a rejecting peer leaves behind is exactly what a peer
  #   that was never chosen leaves behind: nothing. "Ten outputs handed over at
  #   once" is the pool property that DOES have an outcome, and it is above.
  #
  # - TestMirrorJob_Run_StreamsBodyInsteadOfBuffering, which asserts the PUT
  #   announces no Content-Length, so the tar streams rather than staging in
  #   heap — 4GB of daemon RSS, once. That is a property of the request; the
  #   artifact that arrives is identical either way.
  #
  # - TestMirrorJob_TarWalkWaitsForExclusiveHolder: the mirror's walk holding
  #   the handle's shared lock while a stream-in replace holds it exclusively.
  #   The outcome — a truncated copy mirrored as complete — needs the walk and
  #   the replace interleaved at an instant no external caller can choose.
  #
  # - The mirror's key rules (a key naming the store rather than an artifact, a
  #   key with a relative segment) are not duplicated here. They are asserted
  #   against POST /mirror in ../features/daemon-containment.feature, where the
  #   refusal and its reason are the subject.
