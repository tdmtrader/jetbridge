Feature: The fixture the Hangar output family stands on

  Requirement 19 admits only the strict native-GCS profile for Hangar, and the
  daemon enforces it: --hangar-enabled is refused without --durable-store=gcs.
  So the filesystem store that let ../features/daemon-durable.feature treat "the
  bucket" as an ordinary directory is not available to this family, and the
  real daemon has to be pointed at a GCS stand-in over HTTP instead.

  It can be, through a seam the production code already has:
  hangar/gcs.NewStorageClient passes a non-empty endpoint to
  option.WithEndpoint together with option.WithoutAuthentication() and
  storage.WithJSONReads(), which is exactly an unauthenticated emulator
  profile. The stand-in is github.com/fsouza/fake-gcs-server — in this process
  locally, and in CI the deployment the Go tier-2 conformance suite already
  uses, named by HANGAR_FAKE_GCS_ENDPOINT, so one deployment serves both
  runners and both share one failure mode.

  THE STAND-IN RECORDS NOTHING. It stores objects with their metadata and
  generations and answers requests; it keeps no request log, which is what
  makes "the bucket holds exactly one object" an outcome rather than a call
  count. Fault injection, precondition ambiguity and truncation stay in Go
  against hangar/gcs's memoryObjectClient, where the interfaces they need are
  unexported by design.

  This file holds ONE scenario, and its job is the fixture rather than the
  product. Everything the Hangar output plane will publish is described in
  ../features/pending/, which is not yet run — see that directory's README.

  # The whole fixture in one chain, said in the only Hangar vocabulary the
  # daemon has today: a raw tar goes in, the daemon canonicalizes it, derives
  # the key itself, stores it in the emulated bucket, and answers with the
  # foundation's attributes. Nothing about the request names a bucket, a scope
  # prefix or an object key; the scope is the only thing the caller says, and
  # the digest, the key and the generation all come back from the server.
  #
  # The generation is the assertion that cannot be satisfied by a daemon
  # talking to a buffer: it is assigned by the bucket at creation and is not
  # knowable before the write lands. The object count is a read of the bucket
  # through the same client, not a memory of what was sent.
  #
  # Reddened by: dropping option.WithEndpoint from hangar/gcs.NewStorageClient
  # (hangar/gcs/gcs.go:58) — the daemon then validates its bucket against real
  # GCS, exits at boot, and the GIVEN reddens, which is the fixture failing
  # loudly rather than a scenario failing later. Second mutation, for the rest
  # of the chain: buildHangarService constructing the store with a bucket name
  # it never validated — the Given stays green and `the Hangar daemon answers
  # 201` reddens on its one line.
  @HOP-19
  Scenario: The real daemon stores a strict tree in the emulated Hangar bucket and names it itself
    Given a real artifact daemon publishing to a Hangar output bucket
    And a step produced a tree whose file "greeting.txt" reads "hello from the node"
    When the tree is published to the scope "brine-fixture"
    Then the Hangar daemon answers 201
    And the published tree is named by the scope "brine-fixture"
    And the published tree carries a store-assigned generation
    And the Hangar output bucket holds exactly 1 objects
