Feature: Run cancellation reaches Hangar bytes only through generic operations

  The store is the output bucket on the GCS API emulator (fake-gcs-server),
  reached through the real output daemon; spec amendment B5 of
  durable_run_cancellation_control makes this the storage-integration tier.
  Real GCS is unverified, and the emulator ignores the delete generation
  precondition the in-memory tier proves.

  The cancellation worker is atccmd's, over a capture coordinator with every
  field production sets. Before each node and store call it or its drain
  makes, a second PostgreSQL connection asks for the Run's row lock with
  NOWAIT; the probe is shown to see a held lock before it is trusted.

  Where a scenario stops the producer's coordinator mid-capture, its capture
  lease is expired, as a restarted web node's would be, so cancellation's
  coordinator takes the capture over at the next fence. Cancellation against
  a capture lease another coordinator still holds is not driven here or by
  any Go test through Coordinator.Cancel. What covers it is below the seam:
  atc/hangaroutput/ambiguity_test.go TestAStaleOwnerCannotAdvanceACapture
  (a coordinator without the live lease is refused at the lease with
  ErrConflict, which the worker keeps as retried Conflict debt) and
  TestATakeoverCarriesTheCaptureThroughToRegistration (once the lease
  expires, the new owner carries the capture to registration).

  Bytes a cancelled Run leaves published stop here: marked, unbound and in no
  manifest. Adopting and reclaiming them needs an object past the 7-day
  adoption grace, which a live publish cannot reach and no clock seam is added
  for. atc/hangaroutput/orphan_test.go,
  atc/db/hangar_output_controller_pass_test.go and
  hangar/output/conformance/orphan_test.go prove it over backdated objects.

  @core-review
  Scenario: Cancellation before the irreversible publish releases the source with nothing published
    Given a Run producer and a ready output node
    And its runtime producer stops capturing before the irreversible publish
    When the production cancellation worker settles its capture
    Then its aborted Run publishes an empty manifest
    And cancellation settled the capture through Hangar's seam with no Run lock held
    And the cancelled capture left no object, candidate or result

  @core-review
  Scenario: Cancellation after the irreversible publish settles the publication receipt as a discard
    Given a Run producer and a ready output node
    And its runtime producer stops capturing after the irreversible publish
    When the production cancellation worker settles its capture
    Then its aborted Run publishes an empty manifest
    And cancellation settled the capture through Hangar's seam with no Run lock held
    And cancellation registered its publication receipt through the repeated publish
    And its publication receipt is settled as a discard without a claim
    And its marked object remains in the bucket unbound

  @core-review
  Scenario: Cancellation first refuses candidate registration of a committed publication receipt
    Given a Run producer and a ready output node
    And its runtime producer publishes a successful review
    And the whole Run is cancelled after publication
    And its Run records the published source release
    When the production cancellation worker settles its capture
    Then its aborted Run publishes an empty manifest
    And cancellation made no node call under a Run lock
    And its publication receipt is settled as a discard without a claim
    And its marked object remains in the bucket unbound

  @core-review
  Scenario: The aborted publication releases an existing hidden candidate's claim
    Given a Run producer and a ready output node
    And its runtime producer publishes a successful review
    And its Run records the published source release
    When the production cancellation worker settles its capture
    Then its aborted Run publishes an empty manifest
    And cancellation made no node call under a Run lock
    And its hidden claim is released in the aborted publication
