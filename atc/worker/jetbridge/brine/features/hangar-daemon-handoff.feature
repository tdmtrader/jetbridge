Feature: What the output daemon answers

  Driven against the real binary through the fixture in
  ../../steps/hangar_fixture.go, for the reason ../../steps/realdaemon.go
  records: a double can be made to refuse, but it cannot say the answer is
  RIGHT, and here half of what an operation does is a change to a node's
  filesystem that no response shows.

  There are TWO daemons in this fixture, and there have to be. A Kubernetes
  service account is Pod-wide, so an output-bucket role on the artifact daemon
  would give its cache and strict-input identity the same role, and Req 20
  forbids the output bucket ever being either of those. The isolation is a
  second Pod, a second service account and a second binary.

  NOTHING HERE COUNTS A REQUEST. The scenarios that mean "the daemon was not
  called" say instead that the source is still held.

  # Reddened by: the hold handler deriving the incarnation from the request
  # instead of issuing it — the acknowledgement line reddens.
  @HOP-3 @HOP-7
  Scenario: The daemon acknowledges a hold for a server-issued source incarnation
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the daemon holds the source
    Then the Hangar daemon answers 200, holding the source
    And the source is still held on the node

  @HOP-4 @HOP-5
  Scenario: The daemon witnesses a natural finish, and the witness is what the step reports
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step finishes and the daemon witnesses it
    Then the witnessed exit status is 0
    And the witness is what the step reports

  # The permitted case is asserted FIRST, and it is a separate scenario written
  # above its absence twin: "not eligible" passes on a daemon that refuses
  # everything, exactly as job-admission.feature:43-49 records.
  @HOP-9 @HOP-14
  Scenario: Destructive cleanup is permitted once the witness and the release both exist
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When destructive cleanup is requested
    Then destructive cleanup is permitted

  # The scenario above is this one's control: it is the already-eligible case,
  # and without it "the source is still there" would pass on a daemon that
  # destroys nothing because it does nothing.
  #
  # Assertion ORDER is part of the assertion. brine stops at the first red step,
  # so the acknowledgement is asserted BEFORE the survival check rather than
  # after it (MIGRATION-EVIDENCE.md:218-230) — a survival check written last is
  # never evaluated on a run where the stop went wrong.
  #
  # Reddened by: RequestSourcePreservingStop removing the incarnation directory
  # as part of the stop — the source line reddens while the acknowledgement line
  # above it stays green.
  @HOP-14 @HOP-16
  Scenario: A source-preserving stop leaves the pod's artifact path in place
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step is stopped without destroying its source
    Then the witness is what the step reports
    And the source is still there after the stop

  # Reddened by: DestructiveCleanupEligible returning true whenever the
  # execution record exists, instead of requiring the finish acknowledgement —
  # this reddens on its one line while the scenario above stays green, which is
  # what tells it apart from a daemon that is simply down.
  #
  # That mutation is BOTH arms of CleanupEligible, and it has to be. Removing
  # only the classification arm leaves this scenario green: the hold's own
  # cleanup gate withholds on its own, so nothing here can tell the two apart.
  # The arm is pinned in Go, by
  # TestDestructiveCleanupWaitsForTheOutcomeAndForEveryOpenGate.
  @HOP-9 @HOP-14
  Scenario: Destructive cleanup is refused until the finish witness exists
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    Then the source is still held on the node
    When destructive cleanup is requested before any witness
    Then destructive cleanup is refused

  @HOP-10 @HOP-11
  Scenario: A stale fence is refused while the current fence is served
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    Then the Hangar daemon answers 200, holding the source
    When the owner's lease is taken over
    And a stale fence is presented
    Then the daemon's refusal says "fence"
    And the source is still held on the node

  @HOP-6
  Scenario: A repeated hold with the same identity returns the same acknowledgement
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the same hold is repeated with the same identity
    Then the hold acknowledgement is the one the first hold returned

  # Convention 6. "Repeating the handoff returns the same state" passes for a
  # daemon that ignores the identity entirely, so the twin has to show reuse for
  # DIFFERENT facts is a typed conflict and the original hold still stands.
  #
  # Reddened by: the hold handler comparing only the handoff UUID and not the
  # fence before returning the stored acknowledgement.
  @HOP-6 @HOP-10
  Scenario: A repeated hold with a different fence is a typed conflict, and the first hold still stands
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the same hold is repeated with a different fence
    Then the daemon's refusal says "conflict"
    And the hold acknowledgement is the one the first hold returned
    And the source is still held on the node

  # Reddened by: the source-control route resolving its target with
  # filepath.Join on the request's path instead of the server-derived
  # incarnation root under the daemon's os.Root handle.
  @HOP-7 @HOP-8
  Scenario: A hold request naming a path instead of an incarnation is refused
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    Then the Hangar daemon answers 200, holding the source
    When the hold request names a path instead of an incarnation
    Then the daemon's refusal says "path"

  @HOP-8
  Scenario: A symlink swapped under the source path is refused, and the unswapped path is not
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    Then the source is still held on the node
    When the source path is replaced by a symlink to "/etc"
    Then the daemon's refusal says "containment"

  # Reddened by: the capability middleware checking that a token is VALID
  # without checking that its facet matches the route it arrived on. The control
  # is the same token succeeding at its own operation, asserted first, so this
  # cannot go green on a daemon that rejects the token outright.
  @HOP-3 @HOP-24
  Scenario: A base control capability cannot hold, seal or publish
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When a base control capability is used to "classify"
    Then the Hangar daemon answers 200, holding the source
    When a base control capability is used to "hold"
    Then the daemon's refusal says "facet"
    When a base control capability is used to "seal"
    Then the daemon's refusal says "facet"
    When a base control capability is used to "publish"
    Then the daemon's refusal says "facet"

  @HOP-12 @HOP-13
  Scenario: A writer ticket issued after the seal is a typed refusal, and one issued before it is not
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When a writer ticket is issued before the seal
    Then the Hangar daemon answers 200, holding the source
    When a writer ticket is issued after the seal
    Then the daemon's refusal says "sealed"
