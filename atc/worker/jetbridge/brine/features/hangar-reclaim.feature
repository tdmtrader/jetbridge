Feature: What the web's deleting passes leave in the output bucket

  The reclaim pass and the orphan sweep are the web's, run under one advisory
  lock, and they are the only things that delete from the output namespace:
  node daemons publish and never delete. Both are driven here as production
  runs them -- atc/hangaroutput/reclaim over this scenario's real PostgreSQL
  and the fixture's emulated output bucket, deleting through the web's own
  delete client by exact generation.

  Kept in Go and cited rather than duplicated: the advisory lock between two
  webs, a delete that loses its response, and a generation that moves between
  the listing and the delete (atc/hangaroutput/drain_sweep_test.go and the
  reclaim specs).

  # A read warrant is minted together with a read lease under the exact
  # lifecycle, and reclaim admission refuses any generation with a live lease
  # -- in the candidate query, under the lifecycle lock, and in the schema's
  # exclusion trigger. So the exact generation CANNOT be reclaimed between
  # minting a warrant and the archive read: the first half of this scenario
  # pins that, with every claim released so the leases are the only
  # protection left. The second half is the out-of-band case the lease cannot
  # prevent: the generation removed from the bucket by anything else, after
  # which the archive read under a still-unspent warrant fails closed as a
  # typed outcome and never returns a tree.
  #
  # Reddened by: ReclaimCandidates and AdmitReclaim ignoring
  # hangar_read_leases -- the admission line reddens.
  @HOP-36 @HOP-37 @HOP-38 @HOP-46
  Scenario: A live read warrant keeps its exact generation from reclaim, and an out-of-band delete makes the archive read fail closed
    Given an artifact daemon serving the output plane over authenticated TLS
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And the consumer binds the output inside its own transaction
    When two archive read warrants are minted for the published generation
    And every claim on the published generation is released
    And the reclaim pass is asked to admit the published generation
    Then reclaim admission is refused while the read leases are live
    When the archive read under the first warrant is made
    Then the first warrant's archive read returned the exact published tree
    When the published generation is deleted out of band
    Then the second warrant's archive read fails closed as "not found"

  # The orphan sweep deletes an object only when its marker names THIS store,
  # no lifecycle row exists for it, nothing pending or publishing could still
  # give it one, and it is older than twice the capture deadline -- and then
  # only by the exact generation the listing reported. Every other object is
  # counted and left alone. The deleted orphan is the control for the two
  # survivals: "survives" passes on a sweep that deletes nothing.
  #
  # Reddened by: Sweep.classify dropping the marker.Store comparison -- the
  # foreign line reddens while the deletion line above it stays green.
  @HOP-45 @HOP-47
  Scenario: The orphan sweep deletes an old orphan marked for this store by its exact generation, and counts what it leaves
    Given a real artifact daemon publishing to a Hangar output bucket
    And the output bucket holds an orphan marked for this store, one marked for another store, and one with no marker
    When the orphan sweep runs with every object past twice the capture deadline
    Then the sweep deleted exactly the orphan marked for this store, by its listed generation
    And the object marked for another store survives the sweep, counted as foreign
    And the object with no marker survives the sweep, counted as unmarked
