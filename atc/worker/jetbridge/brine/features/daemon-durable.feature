Feature: The durable tier, against the daemon that implements it

  step-closing.feature already has nine durable-tier scenarios, and they drive
  a DOUBLE of the daemon. That double is the right tool for its question, which
  is what the ATC does with an answer: it can be made to advertise no tier, to
  hold the bytes on a peer, to fail every request.

  It cannot say whether the answers are RIGHT, and for this tier the gap is
  wider than usual — because half of what the durable path does is not an
  answer at all, it is a change to a filesystem. Whether a warmed copy lands
  where the sweeper reclaims it, whether an upload is filed under the content
  key or under a Postgres row id, whether a refused extraction leaves residue
  behind, whether an operator's retention rule reaches the code that deletes:
  none of those appear in a response, and the double had no filesystem to get
  them wrong on.

  Here the daemon is the actual binary from cmd/artifact-daemon, run as a
  process with `--durable-store=filesystem --durable-path`. That makes the
  "bucket" an ordinary directory these scenarios seed and inspect, which is
  what turns "the store was not touched" from a call count into an outcome:
  the store is given DIFFERENT bytes from the node's copy, and the assertion
  names which bytes arrived. Nothing below counts a request or records one.

  No Kubernetes is involved. Nothing passes --node-name — the daemon builds a
  Kubernetes client the moment it is given one, and exits outside a cluster.

  # -------------------------------------------------------------------------
  # What a probe means, and what a warm changes about it
  # -------------------------------------------------------------------------

  # THE invariant the two-phase design exists to protect. Every daemon in the
  # cluster reads the same store, so a probe that answered from it would have
  # every pod saying yes for anything ever stored — the ATC would bind an
  # artifact read to an arbitrary pod, and the node affinity the probe exists
  # to provide would be gone. A cache sitting on node A would be served by
  # node B pulling it back out of object storage, on every build.
  #
  # The store HOLDS the object here, and the warm below proves it holds it in
  # a form this daemon can use — so the 404 cannot be the store being empty or
  # the key being wrong. It can only be the daemon declining to answer for
  # bytes that are not on its own disk.
  #
  # The header is the other half. Capability rides the answer the ATC already
  # asks for, and it is consulted only on a MISS: an ATC talking to daemons
  # that predate the tier must issue zero requests to a route they do not
  # have. A header that appeared on hits alone would be a header nobody reads.
  Scenario: A probe answers for this node's disk, never for the shared store
    Given a real artifact daemon with a durable store
    And the durable store holds the cache "resource-caches/rc-abc" whose file "payload" reads "only in the store"
    # Seeded under the BARE key as well, and this is the point of the scenario
    # rather than a detail. The likely form of the defect is a fallback added
    # after the registry miss — `if s.durable.Has(ctx, key) { 200 }` — which
    # looks up "rc-abc". With the store holding only the class-prefixed
    # spelling that lookup misses and the 404 stands, so the scenario stayed
    # green while every daemon in the cluster said yes for anything ever
    # stored. Holding both spellings means a fallback under either one turns
    # this 404 into a 200.
    And the durable store also holds the cache "rc-abc" whose file "payload" reads "only in the store"
    When the ATC asks it whether the cache "rc-abc" is on this node
    Then the daemon's answer is 404
    And the answer advertises that this daemon can warm from the store
    When the ATC asks it to warm "rc-abc" from the content key "resource-caches/rc-abc"
    Then the daemon's answer is 201
    When the ATC asks it whether the cache "rc-abc" is on this node
    Then the daemon's answer is 200

  # The payoff path, and the placement defect that rides along with it.
  #
  # The two names are different namespaces. The object lives in the store under
  # a retention-class prefix; the local copy must land as a DIRECT child of
  # steps/, because a direct child is the only thing the sweeper reclaims.
  # Using the durable key for the destination would nest it one level down,
  # where nothing ever sweeps it and node disk grows without bound — the way
  # task caches on hostPath already did once.
  #
  # That defect is invisible over HTTP: an artifact read falls back to the
  # registry alias, which points at the copy wherever it landed, so a nested
  # restore serves perfectly well and is only wrong weeks later. The last line
  # therefore reads the directory.
  Scenario: A cache only the store holds arrives, and lands where the sweeper can reclaim it
    Given a real artifact daemon with a durable store
    And the durable store holds the cache "resource-caches/rc-abc" whose file "payload" reads "restored from the store"
    When the ATC asks it to warm "rc-abc" from the content key "resource-caches/rc-abc"
    Then the daemon's answer is 201
    And the answer says the bytes came from the durable store
    When a build fetches the cache "rc-abc" from it
    Then the daemon's answer is 200
    And the cache holds "payload" reading "restored from the store"
    And the node's steps directory holds only "rc-abc"

  # The "not touched" case, done as an outcome rather than as a call count.
  # Going to object storage for something already on this node's disk is the
  # whole cost of the tier with none of its benefit.
  #
  # The store's copy says something DIFFERENT from the node's, which is what
  # makes this fail for a real reason: a daemon that fetched anyway would
  # answer 201 and report the bytes as durable, and — if it also cleared the
  # destination first — would serve the store's text to the build. No request
  # is counted anywhere; the assertion is on which bytes came back.
  Scenario: A cache the node already holds is served from the node, and the store copy is not used
    Given a real artifact daemon with a durable store
    And a get step on this node left the cache "rc-abc" whose file "payload" reads "already on this node"
    And the ATC registers "rc-abc" with it, naming no content key
    And the durable store holds the cache "resource-caches/rc-abc" whose file "payload" reads "an older copy from the store"
    When the ATC asks it to warm "rc-abc" from the content key "resource-caches/rc-abc"
    Then the daemon's answer is 200
    And the answer says the bytes were already here
    When a build fetches the cache "rc-abc" from it
    Then the cache holds "payload" reading "already on this node"

  # Silence is the whole eligibility protocol. The ATC supplies a content key
  # for what it wants kept and stays silent about the rest, and the daemon
  # never derives one — because a cache with no content key is addressed by
  # "rc-42", a Postgres row id, and filing THAT in permanent storage would mean
  # a later build's unrelated row 42 restores this build's bytes. Step outputs
  # would go the same way, and the bucket would grow with every build forever.
  #
  # "Holds exactly" is the assertion, so a promotion under the local alias
  # instead of the content key — which would drop the retention class and put
  # the object outside every lifecycle rule an operator wrote — fails it just
  # as an unwanted upload does.
  #
  # The round trip at the end is what says the object is USABLE rather than
  # merely present: it is warmed back under a name this node has never used,
  # and the file inside it is the one the get step produced.
  Scenario: Only a cache the ATC names a content key for reaches the store, and it comes back whole
    Given a real artifact daemon with a durable store
    And a get step on this node left the cache "rc-nokey" whose file "payload" reads "a step output"
    And a get step on this node left the cache "rc-keyed" whose file "payload" reads "a resource cache"
    When the ATC registers "rc-nokey" with it, naming no content key
    And the ATC registers "rc-keyed" with it under the content key "resource-caches/rc-keyed"
    And the upload of "resource-caches/rc-keyed" lands in the durable store
    Then the durable store holds exactly "resource-caches/rc-keyed"
    When the ATC asks it to warm "rc-restored" from the content key "resource-caches/rc-keyed"
    Then the daemon's answer is 201
    When a build fetches the cache "rc-restored" from it
    Then the cache holds "payload" reading "a resource cache"

  # Reclaim is the only thing standing between a bucket that grows forever and
  # a bucket somebody emptied by accident, and every uncertainty in it must
  # answer KEEP.
  #
  # Four objects, and only the first may go: the other three are each a
  # different reason to keep. Younger than its retention period. In a class
  # nobody configured — which is also what a mistyped class name looks like.
  # No class prefix at all, so nothing claims it.
  #
  # The pass runs on the daemon's own schedule from its own flags, which is
  # the part no unit test can see: the Go test calls sweep() directly, so it
  # stays green if main() stops wiring --durable-retention into the maintainer
  # altogether.
  Scenario: Reclaim removes what the operator's retention rule covers, and nothing else
    Given a real artifact daemon reclaiming the class "resource-caches" after 24 hours
    And the durable store holds the object "resource-caches/rc-old", last written 48 hours ago
    And the durable store holds the object "resource-caches/rc-new", last written 2 hours ago
    And the durable store holds the object "reviews/rc-ancient", last written 10000 hours ago
    And the durable store holds the object "rc-unclassed", last written 10000 hours ago
    When the daemon's reclaim pass removes "resource-caches/rc-old"
    Then the durable store still holds "resource-caches/rc-new"
    And the durable store still holds "reviews/rc-ancient"
    And the durable store still holds "rc-unclassed"

  # Every daemon reads one shared store, so an object in it is untrusted
  # input: it need not have been produced by us, and the daemon runs as root
  # with CAP_DAC_OVERRIDE. The archive here symlinks out of the destination
  # and then writes a file through the link — neither member is hostile alone,
  # which is why containment has to be a property of the handle every write
  # goes through rather than a check on a name.
  #
  # The benign warm first is not decoration. A refusal scenario that asserts
  # only "not 200" passes when the object was never in the store at all, and
  # then the two assertions below pass vacuously too. Warming an ordinary
  # object seeded the same way, through the same route, into the same
  # namespace, rules that out: what is left for the hostile one to fail on is
  # its content.
  #
  # The refusal deliberately does not say why — a store miss and a refused
  # extraction are made indistinguishable so that no caller learns to treat
  # one as fatal — so the two lines that follow it are the "why". The file
  # outside is unchanged, and steps/ holds only the copy that was accepted,
  # which is also how a leftover ".restore-" working directory would show up.
  Scenario: A hostile object in the shared store cannot write outside the copy it is restoring
    Given a real artifact daemon with a durable store
    And the durable store holds the cache "resource-caches/rc-benign" whose file "payload" reads "ordinary bytes"
    And the durable store holds a hostile object under "resource-caches/rc-hostile" that links out to a file reading "original"
    When the ATC asks it to warm "rc-benign" from the content key "resource-caches/rc-benign"
    Then the daemon's answer is 201
    When the ATC asks it to warm "rc-hostile" from the content key "resource-caches/rc-hostile"
    Then the daemon's answer is 404
    And the file it tried to escape to still reads "original"
    And the node's steps directory holds only "rc-benign"

  # A cold cache — nothing on the node, nothing in the store — has to read as
  # an ordinary miss, because the ATC's recovery is to re-run the get step and
  # that must not be reserved for one of the two ways a warm can come back
  # empty. (A store failure answers the same 404 on purpose: the caller's next
  # move is identical, and making the two distinguishable invites somebody to
  # treat one of them as fatal.)
  #
  # The two lines after the status are what stop a failed warm from being
  # worse than no warm at all. A destination promoted before its bytes arrived
  # is an EMPTY directory, and an empty directory is a perfectly good artifact
  # as far as the rest of the daemon is concerned: every later probe reports a
  # hit and every later fetch serves an empty tar to a build that asked for a
  # cache. An alias registered before the restore succeeded does the same
  # damage without even leaving a directory behind to notice.
  Scenario: A warm for something nobody stored is a miss that leaves no trace
    Given a real artifact daemon with a durable store
    When the ATC asks it to warm "rc-cold" from the content key "resource-caches/rc-cold"
    Then the daemon's answer is 404
    And nothing new appeared under the storage root
    When the ATC asks it whether the cache "rc-cold" is on this node
    Then the daemon's answer is 404

  # Both names in a warm request become paths, and neither may be one.
  #
  # The content key is joined onto the store root, so "../../etc" would name
  # any directory on the host — and this process runs as root with
  # CAP_DAC_OVERRIDE. The local name is joined onto steps/ and must be a
  # single segment, because a nested one is the flat-landing defect above
  # arriving by another route: a copy the sweeper never reclaims.
  #
  # The store HOLDS the object both requests are reaching for. That is what
  # keeps this from being the trap every refusal scenario falls into — a 400
  # here cannot be the daemon failing to find anything, because the identical
  # warm with a legal local name is the one that succeeds two scenarios up.
  # It also makes the last line load-bearing: a daemon that let the nested
  # name through would not merely answer differently, it would put a copy at
  # steps/resource-caches/rc-abc that nothing on the node ever reclaims.
  #
  # Each refusal names which of the two was wrong, and the scenario asserts
  # that rather than merely that something was refused.
  Scenario: A warm that names a path instead of a key is refused, and says which name was wrong
    Given a real artifact daemon with a durable store
    And the durable store holds the cache "resource-caches/rc-abc" whose file "payload" reads "a perfectly good object"
    When the ATC asks it to warm "rc-abc" from the content key "../../etc"
    Then the daemon's answer is 400
    And the daemon's answer explains "invalid durable_key"
    When the ATC asks it to warm "resource-caches/rc-abc" from the content key "resource-caches/rc-abc"
    Then the daemon's answer is 400
    And the daemon's answer explains "single path segment"
    And nothing new appeared under the storage root

  # -------------------------------------------------------------------------
  # Left in Go, deliberately
  # -------------------------------------------------------------------------

  # DISPOSITION — TestConcurrentRestoresOfOneKeyCollapse and
  # TestConcurrentStoresOfOneKeyCollapse. "Six builds wanting one cache
  # produce one download" is a claim about how many times the store was
  # asked, and nothing else distinguishes it: the bytes are identical either
  # way, and so is every response. It is observable only through a store that
  # counts its own Gets, which is a recording double. It stays in Go, where
  # the double is honest about being one.

  # DISPOSITION — TestABrokenStoreDegradesInsteadOfPropagating and
  # TestANilTierIsSafe. Both inject a store: one that fails every operation,
  # and a nil tier reached through a possibly-nil pointer. A daemon
  # configured from flags cannot be given either — a filesystem store that
  # cannot be reached is not something --durable-path can express — and the
  # nil case is a Go-level property of the pointer, not a behaviour a build
  # can experience.

  # DISPOSITION — TestDurableRestoreWithoutATierIs501 and
  # TestHeadResourceCacheAdvertisesNothingWithoutATier. Both need a daemon
  # with NO durable store, which is a different process from the one every
  # scenario here starts. The behaviour they protect — an ATC talking to a
  # daemon that predates the tier issues zero warm requests — is already
  # stated in step-closing.feature ("A daemon that predates the durable tier
  # is never asked to warm"), and what the Go tests add on top is the 501/404
  # distinction in the daemon's own logs. Worth its own scenario when someone
  # is next in this file; not worth a second daemon shape today.

  # DISPOSITION — TestRetentionPolicyParsing, TestExpiredKeepsWhateverItCannot
  # JustifyDeleting and TestAnEmptyPolicyExpiresNothing are table-driven tests
  # over two pure functions. The reclaim scenario above covers four of their
  # rows through the running daemon — expired-and-configured, too young,
  # unconfigured class, no class prefix. What is left is a table's proper
  # work: eight malformed --durable-retention strings, where a scenario per
  # row would be eight daemons for eight flag values.
  #
  # Two rows are unreachable from here rather than merely not worth a daemon,
  # and that is worth saying out loud:
  #
  #   - "no timestamp". A store that reports no write time is what would empty
  #     a bucket, since the zero value reads as 1970 and every object looks
  #     ancient. A filesystem store always has an mtime, so the state cannot
  #     be produced through --durable-store=filesystem at all.
  #   - "flat key spelling a configured class". The object is literally named
  #     "resource-caches", with no prefix, and it is the case the no-prefix
  #     check uniquely covers. In a real store it cannot coexist with the
  #     "resource-caches/" prefix the same scenario needs: one name cannot be
  #     both a file and a directory.
