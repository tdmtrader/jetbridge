@VT-06 @VT-08 @VT-10
Feature: Getting artifacts from the artifact daemon

  Real production daemons serve owned files and registry aliases. These cases
  cover known addresses, absent producers, discovery, peer fallback and caches.
  Named-node reads, compression and filtering moved to live/daemon-node-read.feature.
  Connection retry and server failures are NOT covered by any brine scenario:
  they are held by the Go tests TestVT06_* and TestVT08_* in
  atc/worker/jetbridge/behavioral_volume_test.go. (This line previously pointed
  at volume-streaming.feature, which has never carried them.)
  Peer-only setup still uses the real_peer.go fixture tracked in REMAINING-DOUBLES.md.

  @VT-06
  Scenario: An artifact comes back from a daemon whose address is already known
    Given an artifact daemon
    And it holds the artifact "rc-42" containing "cached resource data"
    And the ATC already knows the daemon address
    When a consumer reads the artifact "rc-42"
    Then the artifact arrives as "cached resource data"
    And the volume reports the handle "rc-42" on worker "k8s-worker-1"

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

  Scenario: A mirrored copy is served when the producing node has left the cluster
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing the file "f.txt" with "peer-served-content"
    And the node that produced the artifact has left the cluster
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the archive holds "f.txt" containing "peer-served-content"

  Scenario: When the node is gone and nobody mirrored it, the failure says so
    Given an artifact daemon
    And the node that produced the artifact has left the cluster
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the read fails saying "or any peer"
    And the read does not fail saying "connection refused"

  Scenario: An artifact is found by asking every daemon when the locator was wiped
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing the file "f.txt" with "daemon-served-content"
    And no producing node was ever recorded
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the archive holds "f.txt" containing "daemon-served-content"

  Scenario: When nothing was recorded and no daemon has it, the failure says that
    Given an artifact daemon
    And no producing node was ever recorded
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the read fails saying "not found on any daemon"

  Scenario: A cached resource is fetchable from the daemon the probe names
    Given an artifact daemon
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

  Scenario: A daemon that can only resolve from peers is not a cache hit
    Given a daemon that can resolve "rc-42" containing "peer cache bytes" only from a peer
    When the ATC probes for the resource cache "rc-42"
    Then the probe reports a miss

  Scenario: A durable tier is learned from a miss, not just from a hit
    Given an artifact daemon with a durable tier
    When the ATC probes for the resource cache "rc-42"
    Then the probe reports a miss
    And the daemon is known to have a durable tier
    And the probe carries back 1 daemon addresses

  Scenario: A daemon that never advertised a durable tier is not credited with one
    Given an artifact daemon
    When the ATC probes for the resource cache "rc-42"
    Then the probe reports a miss
    And the daemon is not known to have a durable tier

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

  Scenario: Repeated discovery of an empty daemon is still a miss
    Given an artifact daemon
    And the daemon address is published twice
    When the ATC probes for a mirrored copy of "handle/output"
    Then the probe reports a miss

  Scenario: One live daemon wins over an unreachable one
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing the file "f.txt" with "peer-served-content"
    And the unreachable daemon count is 1
    And the node that produced the artifact has left the cluster
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the archive holds "f.txt" containing "peer-served-content"

  Scenario Outline: A holder is identified and read — unreachable peers: <unreachable>
    Given an artifact daemon
    And it holds a mirrored copy of the artifact "handle/output" containing the file "f.txt" with "peer-served-content"
    And the unreachable daemon count is <unreachable>
    When the ATC probes for a mirrored copy of "handle/output"
    Then the named daemon serves "f.txt" containing "peer-served-content"

    Examples:
      | unreachable |
      | 0           |
      | 1           |
