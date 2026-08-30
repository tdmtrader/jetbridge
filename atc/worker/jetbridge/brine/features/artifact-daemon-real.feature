Feature: The artifact daemon itself, not a stand-in for it

  Every other artifact scenario drives a DOUBLE of the daemon — a real
  http.Server answering its routes out of a map. That double is the right tool
  for asking what the ATC does with an answer: it can be made to 404, to
  refuse, to go away on command.

  What it cannot do is tell you the answer is RIGHT. Three things this suite
  already asserted turned out to rest on the double's own implementation
  rather than on the daemon's:

    - "the archive holds X containing Y" read bytes the double was handed
      pre-built. Nothing asserted that the daemon, asked for a directory on
      its disk, produces a tar whose members carry their relative paths.
    - "an output whose node the worker could not identify is still fetched by
      its directory" is green because the double looks up "steps/"+key in a
      map. Had the daemon's filesystem fallback regressed, that scenario would
      have stayed green while every build after a web restart broke.
    - the double's /register invents the rule that a daemon whose node does
      not hold the path answers 404, and refuses a path outside the storage
      root. Both are decisive properties of the scheme, and both were guessed.

  All three were then checked against the real daemon and all three were
  right — but right by luck rather than by construction, which is what these
  scenarios change. Here the daemon is the actual binary, built from
  cmd/artifact-daemon and run as a process with its own storage root. No
  Kubernetes is involved: the daemon only builds a client when asked to label
  a node, and nothing here asks.

  # -------------------------------------------------------------------------
  # What a step's output looks like coming back out
  # -------------------------------------------------------------------------

  # The single most depended-on claim in the contract. Every "the archive
  # holds X containing Y" elsewhere assumes this shape.
  Scenario: A directory comes back as a tar carrying each file at its own path
    Given a real artifact daemon
    And a step wrote "top level" into its output "build-42/result/root.txt"
    And a step wrote "one down" into its output "build-42/result/sub/nested.txt"
    When the ATC asks it for the artifact "build-42/result"
    Then the artifact arrives
    And the archive carries 2 files
    And the archive carries a file at "root.txt"
    And the archive carries a file at "sub/nested.txt"
    And the file at "sub/nested.txt" reads "one down"

  # The fallback a whole scenario in artifact-recording.feature leans on:
  # after a web restart the worker has forgotten where an output is, and the
  # daemon has to find it on its own disk without being told.
  Scenario: An artifact nobody registered is still found on the disk it sits on
    Given a real artifact daemon
    And a step wrote "never announced" into its output "orphan/result/f.txt"
    When the ATC asks it for the artifact "orphan/result"
    Then the artifact arrives
    And the file at "f.txt" reads "never announced"

  # -------------------------------------------------------------------------
  # Registering an alias, and the two refusals that make it mean something
  # -------------------------------------------------------------------------

  Scenario: A registered key serves the directory it was registered for
    Given a real artifact daemon
    And a step wrote "by its alias" into its output "handle/output/f.txt"
    When the ATC registers "vol-alias" as living at the step output "handle/output"
    Then the daemon answers 201
    And the ATC asks it for the registered artifact "vol-alias"
    And the artifact arrives
    And the file at "f.txt" reads "by its alias"

  # Without this, every daemon would accept every registration and "which node
  # can serve this artifact" would stop meaning anything.
  Scenario: A daemon whose disk does not hold the path refuses the registration
    Given a real artifact daemon
    When the ATC registers "vol-absent" as living at the step output "nothing/here"
    Then the daemon answers 404
    And the refusal explains "not found"

  # The containment rule. A daemon that accepted this would serve any file on
  # the node to anyone who asked for the right key.
  Scenario: A path outside the storage root is refused, not registered
    Given a real artifact daemon
    When the ATC registers "vol-escape" as living at the absolute path "/etc"
    Then the daemon answers 400
    And the refusal explains "outside the storage root"
