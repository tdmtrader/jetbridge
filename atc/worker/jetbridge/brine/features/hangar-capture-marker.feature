Feature: What the artifact daemon answers about a capture's marker

  Driven against the real binary through the fixture in
  ../../steps/hangar_fixture.go, for the reason ../../steps/realdaemon.go
  records: a double can be made to refuse, but it cannot say the answer is
  RIGHT, and here half of what an operation does is a change to a node's
  filesystem that no response shows.

  There is ONE daemon per node: the artifact daemon serves the output plane's
  capture routes beside its cache and strict-input routes. A capture's only
  node-local state is one marker file in its step directory, and the marker is
  held, sealed or released (a tombstone). The output namespace is never the
  cache's or the strict input's; the daemon refuses to start if any two
  coincide.

  NOTHING HERE COUNTS A REQUEST. The scenarios that mean "the daemon was not
  called" say instead that the held step directory is still on the node.

  # Reddened by: the hold handler writing no held marker before creating the
  # step directory — the acknowledgement line reddens.
  @HOP-3 @HOP-7
  Scenario: The daemon acknowledges a hold for the capture's step directory
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the daemon writes the held marker
    Then the Hangar daemon answers 200, the marker held
    And the held step directory is still on the node

  @HOP-4 @HOP-5
  Scenario: The daemon witnesses a natural finish, and the witness is what the step reports
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
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
    And the daemon writes the held marker
    And the step finishes and the daemon witnesses it
    When destructive cleanup is requested
    Then destructive cleanup is permitted

  # The scenario above is this one's control: it is the already-eligible case,
  # and without it "the step directory is still there" would pass on a daemon that
  # destroys nothing because it does nothing.
  #
  # Assertion ORDER is part of the assertion. brine stops at the first red step,
  # so the acknowledgement is asserted BEFORE the survival check rather than
  # after it (MIGRATION-EVIDENCE.md:218-230) — a survival check written last is
  # never evaluated on a run where the stop went wrong.
  #
  # Reddened by: RequestSourcePreservingStop removing the step directory
  # as part of the stop — the step-directory line reddens while the acknowledgement line
  # above it stays green.
  @HOP-14 @HOP-16
  Scenario: A source-preserving stop leaves the pod's artifact path in place
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    When the step is stopped without destroying its step directory
    Then the witness is what the step reports
    And the step directory is still there after the stop

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
    And the daemon writes the held marker
    Then the held step directory is still on the node
    When destructive cleanup is requested before any witness
    Then destructive cleanup is refused

  @HOP-10 @HOP-11
  Scenario: A stale fence is refused while the current fence is served
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    Then the Hangar daemon answers 200, the marker held
    When the execution is taken over under a newer fence
    And a stale fence is presented
    Then the daemon's refusal says "fence"
    And the held step directory is still on the node

  @HOP-6
  Scenario: A repeated hold with the same identity returns the same acknowledgement
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    When the held marker is requested again with the same identity
    Then the held marker is the one the first request wrote

  # Convention 6. "Repeating the hold returns the same marker" passes for a
  # daemon that ignores the Pod entirely, so the twin has to show that a hold
  # from a DIFFERENT Pod -- a recreated one -- is a typed conflict and the
  # original hold still stands.
  #
  # Reddened by: the hold handler returning the stored marker without comparing
  # the Pod it was held for.
  @HOP-6 @HOP-10
  Scenario: A repeated hold from a different pod is a typed conflict, and the first hold still stands
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    When the held marker is requested again for a different pod
    Then the daemon's refusal says "conflict"
    And the held marker is the one the first request wrote
    And the held step directory is still on the node

  # Reddened by: the hold route accepting a field it does not declare -- a
  # caller-chosen location silently dropped is one an operator never hears
  # about.
  @HOP-7 @HOP-8
  Scenario: A hold request naming a path instead of a step is refused
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    Then the Hangar daemon answers 200, the marker held
    When the held-marker request names a path instead of a step
    Then the daemon's refusal says "path"

  @HOP-8
  Scenario: A symlink swapped in for the step directory is refused, and the unswapped path is not
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    Then the held step directory is still on the node
    When the step directory is replaced by a symlink to "/etc"
    Then the daemon's refusal says "not a directory"

  # Reddened by: the capability middleware checking that a token is VALID
  # without checking that its facet matches the route it arrived on. The control
  # is the same token succeeding at its own operation, asserted first, so this
  # cannot go green on a daemon that rejects the token outright.
  @HOP-3 @HOP-24
  Scenario: A base control capability cannot hold, seal or publish
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    When a base control capability is used to "classify"
    Then the Hangar daemon answers 200, the marker held
    When a base control capability is used to "hold"
    Then the daemon's refusal says "facet"
    When a base control capability is used to "seal"
    Then the daemon's refusal says "facet"
    When a base control capability is used to "publish"
    Then the daemon's refusal says "facet"


  # The CONTROL, and it is the regression the refusal must not become: without
  # it, "a capture-held pause pod is refused" passes on a runtime that has
  # stopped replacing pause pods at all. It is a separate scenario written
  # above its twin for the same reason `Destructive cleanup is permitted once
  # the witness and the release both exist` is -- brine's registry gives one
  # sentence exactly one input type, and these two chains start from different
  # states.
  @HOP-12 @HOP-16
  Scenario: An ordinary step's terminal pause pod is still replaced
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When an ordinary step's pause pod reaches a terminal state
    Then the pause pod is recreated

  # A pause pod that dies before the step's command runs is REPLACED, and a
  # replacement is a new Pod UID getting a write-capable mount over the step's
  # tree. For a capture-selected step that tree is the held step directory, and
  # a held step directory may not receive one. This is the one
  # destructive path with no execution identity to ask about, which is why it
  # goes through the ledger classifier instead.
  #
  # Reddened by: Container.Run replacing the terminal pause Pod without first
  # consulting the ledger classifier -- this scenario reddens on `the pause pod
  # is not recreated` and the control scenario above stays green.
  @HOP-12 @HOP-16
  Scenario: Pause pod recreation over a held step directory is refused, and an ordinary one still recreates
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    When its pause pod reaches a terminal state
    Then the pause pod is not recreated
    And the runtime's refusal says "durable output capture holds the source"

  # A takeover is the only concurrency this runner can say, and it says it
  # sequentially: the execution is admitted again under the next fence, and
  # the old owner's fence is then stale.
  @HOP-10
  Scenario: A takeover advances the fence, and the previous owner's fence stops being served
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    When the execution is taken over under a newer fence
    Then the Hangar daemon answers 200, the marker held
    When a stale fence is presented
    Then the daemon's refusal says "fence"

  # "The marker is released" needs its presence half, and the release is the
  # only thing that turns a held marker into a released tombstone: the scenario
  # asserts the step directory is held, releases the marker, and asserts the
  # hold's gate is closed while the bytes stay.
  #
  # The FAILING producer between them is not decoration. The control plane
  # releases a capture whose row is terminal, and a row is terminal only after
  # the node's finish or stop. Without this line the scenario asked
  # for a state production cannot reach.
  #
  # Reddened by: the release route leaving the hold's gate open -- the first
  # check, the presence half, stays green and only "the marker is released and
  # the step directory remains" reddens.
  @HOP-9 @HOP-11
  Scenario: A released marker closes the hold's gate, and a held one does not
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    Then the held step directory is still on the node
    When the step fails
    And the daemon releases the marker
    Then the marker is released and the step directory remains
