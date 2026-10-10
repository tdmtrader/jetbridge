Feature: Local resource caches and the artifact index

  Scenario: A cache already on a ready node is served
    Given an artifact daemon serving local resource caches
    And the node already holds the resource cache "rc-42" containing "already on this node"
    When a get step looks up the resource cache "rc-42"
    Then the resource cache is found
    And the cached artifact reads "already on this node"

  Scenario: A daemon pod the API marked not ready is not a cache hit
    Given an artifact daemon serving local resource caches
    And the node already holds the resource cache "rc-42" containing "on a dying pod"
    And the API has marked the daemon pod not ready
    When a get step looks up the resource cache "rc-42"
    Then the resource cache is not found

  Scenario: A cache whose alias vanishes before the read still gets its bytes
    Given an artifact daemon serving local resource caches
    And the node already holds the resource cache "rc-42" containing "swept away mid-read"
    And a peer still holds a mirrored copy of "rc-42" containing "served by the peer"
    When a get step looks up the resource cache "rc-42", and the node's alias vanishes before the bytes are read
    Then the resource cache is found
    And the cached artifact reads "served by the peer"

  Scenario: An artifact is found on the node that recorded it
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1"
    And the artifact "def" is recorded on node "node-2"
    Then the artifact "abc" is held on node "node-1"
    And the artifact "def" is held on node "node-2"

  # The directory is the daemon key the next step's init container fetches
  # from, so losing it is as bad as losing the node.
  Scenario: An artifact remembers the directory it was stored in
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1" in directory "container-abc/output"
    Then the artifact "abc" is held on node "node-1"
    And the artifact "abc" is stored in directory "container-abc/output"

  Scenario: An artifact nobody recorded is not held anywhere
    Given an empty artifact index
    Then the artifact "nonexistent" is not held anywhere

  Scenario: A collected artifact stops resolving
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1"
    And the artifact "abc" is collected
    Then the artifact "abc" is not held anywhere

  # The index answers two questions — where is this, and which node holds it —
  # and only the first one was ever asked about a key the index does not hold.
  # The second is the one production asks. Two readers branch on its answer:
  # the reaper skips a destroyed handle it cannot place, and the scheduler
  # counts one node per input it can place and sends the pod to the winner.
  #
  # A node lookup that says "yes" for a key it has never seen hands both of
  # them the empty node name as if it were a real placement. The reaper then
  # tries to resolve an address for "" once per unplaceable handle on every GC
  # pass and logs a failure for each; the scheduler lets "" collect a vote per
  # unrecorded input, and once those outvote the node that genuinely holds the
  # data the winning preference is the empty one — which is no preference at
  # all, so the pod lands anywhere and every local read becomes a cross-node
  # fetch. Neither reader ever notices, because the phantom answer is
  # well-formed.
  #
  # Both kinds of absence are here — a key that was collected and a key that
  # was never recorded — and a held key sits alongside them, so a lookup that
  # simply denied everything could not pass either.
  Scenario: The node lookup names nobody for artifacts the index does not hold
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1"
    And the artifact "def" is recorded on node "node-2"
    And the artifact "def" is collected
    Then the artifact "abc" is held on node "node-1"
    And no node is named for the artifact "def"
    And no node is named for the artifact "never-recorded"

  # The ginkgo case spawned 300 goroutines over 26 COLLIDING keys and asserted
  # nothing at all — it was a race-detector probe, and this repository's unit
  # tier does not run with -race (CLAUDE.md: "do not use --race"), so it
  # proved nothing there either. Distinct keys turn it into a claim that can
  # fail: nothing recorded concurrently may be lost.
  Scenario: Records made at the same moment are all kept
    Given an empty artifact index
    When 100 artifacts are recorded at the same moment
    And 100 more are recorded and collected at the same moment
    Then every artifact that was recorded is still held
    And every artifact that was collected is gone

  # Cancellation of running commands is covered in live/cancellation.feature.
