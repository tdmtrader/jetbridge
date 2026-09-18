@live-kubernetes @VT-06
Feature: Retrying remote artifacts and finding peer copies

  Kubernetes supplies the producer node address. Owned real daemons supply
  bytes and errors; a TCP route drops connections without fabricating replies.
  The same named volume exercises both raw and gzip delivery.

  Scenario: An artifact still arrives from a daemon that drops the first connections
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon drops the first 2 connections
    When the next step fetches the artifact from that node
    Then the artifact "release.tgz" containing "built on another node" is there

  Scenario: A daemon that never answers is a failed read, not an empty artifact
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon never completes a connection
    When the next step fetches the artifact from that node
    Then the read fails rather than handing back an empty artifact

  Scenario: A daemon that is failing says so rather than looking like a miss
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon is failing and answers every request with an internal error
    When the next step fetches the artifact from that node
    Then the read fails rather than handing back an empty artifact
    And the failure says the daemon is broken rather than that the artifact is gone

  Scenario: A peer's mirror still arrives when the producing node's daemon has stopped answering
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon never completes a connection
    And a peer daemon holds a mirrored copy of it containing "mirrored to a peer"
    And the ATC can ask the other daemons for a mirrored copy
    When the next step fetches the artifact from that node
    Then the artifact "release.tgz" containing "mirrored to a peer" is there

  Scenario: A refused producer that nobody mirrored fails as a search that came up empty
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node is still in the cluster, and its daemon port refuses the connection
    And the ATC can ask the other daemons for a mirrored copy
    When the next step fetches the artifact from that node
    Then the read fails rather than handing back an empty artifact
    And the failure names the node and its peers rather than the refused connection
