Feature: What the artifact daemon refuses

  This is the security half of the daemon's contract: not what it serves, but
  what it declines to serve, write, or destroy. Every scenario here runs the
  ACTUAL artifact-daemon binary as a process with its own storage root on a
  free port (../steps/realdaemon.go builds it once and starts one per
  scenario). Nothing is stood in for, and that is not a preference — a double
  of a guard is a guard you wrote, and asserting that it refuses proves only
  that you remembered to make it refuse.

  The rules under test were not designed up front. They accreted over five
  adversarial review rounds, and the same shape of defect was found four
  times: a fix landed at the site the reproduction happened to use and the
  identical escape stayed open one route over. /artifacts/ got a containment
  check and /stream-in/ did not; "." was rejected and "steps" was not; the
  registry was validated at two of the five places that read it. So the
  scenarios below are deliberately organised BY RULE and swept ACROSS ROUTES,
  because that is the axis along which this code has actually broken.

  Two things every scenario here does, and both exist because their absence is
  how a containment test lies:

    - It asserts the state OUTSIDE the boundary, not just the status. A
      refusal that arrives after the RemoveAll is not a refusal, and only the
      victim file can tell you which happened.
    - It asserts WHY the refusal came, not merely that it was not a 200. A
      scenario that accepts any non-2xx passes when the daemon 500s for an
      unrelated reason, which is the exact failure mode this family has —
      "refused for the wrong reason" is indistinguishable from "guarded" if
      you only look at the number.

  And every refusal here is paired with a permission. A validator that refuses
  everything passes every containment test ever written and takes the cluster
  down; the charset rule in this very daemon was once tightened to
  ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$ and began 400ing "café", "_out" and
  ".git" — all legal Concourse identifiers, all a broken build, none visible
  in any test. So the ordinary key, the permitted overwrite, the legitimate
  internal symlink and the contained destination are asserted beside the
  refusals, in the same scenario, against the same daemon.

  WHAT STAYED IN GO, and why:

    - destlock_test.go asserts the size of an unexported sync.Map and the
      capacity of an unexported channel. Neither is observable through any
      route; the tests are in-package for exactly that reason, and exporting
      them to reach from here would be worse than the duplication.
    - containment_test.go's PeerResolver.Fetch cases that concern PROMOTION
      rather than extraction — replacing an unrelated occupant of the
      destination, and eight concurrent fetches racing into one destination —
      are properties of Fetch's own rename dance. Reaching them needs a second
      real daemon, and two real daemons cannot find each other outside a
      cluster: peer discovery goes through EndpointSlices and main.go builds
      that client with rest.InClusterConfig() alone. Closing that needs a
      production flag, which is a decision rather than a detail.
    - Everything containment_test.go asserts about the EXTRACTION RULE itself
      is here, because stream-in and Fetch share one extractor. The archive is
      the same, the refusal is the same code, and PUT /stream-in is the route
      an operator can actually reach.

  One seam that sharing leaves uncovered, and it is worth being precise about
  because it is invisible from the scenarios. The shared code is
  extractTarToRoot, which takes a root handle the CALLER owns. Stream-in owns
  one already — it opens steps/, then a temp directory inside it — so it calls
  extractTarToRoot directly and never goes through extractTarToDir, the
  path-taking wrapper that does the os.OpenRoot for callers holding only a
  directory name. Fetch is such a caller. So gutting the WRAPPER — dropping
  its OpenRoot and writing entries through the ambient filesystem — reddens
  containment_test.go's PeerFetch cases and leaves every scenario below green.
  Demonstrated, not reasoned: it happened by accident while this file was
  being written, and all seventeen scenarios passed. The extraction rule is
  covered here; the wrapper that hands the rule its boundary is not, and
  closing that needs the second real daemon the previous paragraph rules out.

  # ==========================================================================
  # The key in the request, before any archive is read
  # ==========================================================================

  # The key becomes a filesystem path before anything else happens, and Go's
  # ServeMux cleans the UNESCAPED path — so "%2e%2e%2f" survives routing and
  # arrives at the handler already decoded to "../". A literal "../" never
  # gets there; the mux 301s it. The encoded form is the vector, and it is
  # what these scenarios send.
  #
  # This one is the worst of the six escapes that followed: an arbitrary
  # recursive delete of any tree on the node, returning 204. Deleting the
  # daemon's own "." check from validateRequestKey, or replacing the segment
  # loop with a check for a literal "../" prefix, reddens it.
  Scenario: A key that climbs out of the storage root deletes nothing, and an ordinary key still deletes
    Given a real artifact daemon guarding its storage root
    And a directory outside that root holding a file that reads "original"
    And the store holds a file "steps/build-1/out/f.txt" reading "KEEP"
    When the ATC sends "DELETE" for an artifact key that climbs out to the directory outside the root
    Then the daemon replies with 400
    And the refusal says "has an empty or relative segment"
    And the file outside the storage root still reads "original"
    When the ATC sends "DELETE" for the artifact key "steps/build-1/out"
    Then the daemon replies with 204
    And the store has nothing at "steps/build-1/out"

  # Stream-in is the write half of the same rule, and it has a second failure
  # mode the read verbs do not: the handler used to os.RemoveAll(dest) before
  # it validated, so a REFUSED request still destroyed whatever a legitimate
  # step had put there. That is why the middle act here aims the traversal at
  # an artifact that already exists and then reads it back — a scenario that
  # only checked the status would be green against a daemon that refused after
  # deleting.
  #
  # The last act is the other half of the same claim, and it has to be here:
  # if "refused overwrite leaves the tree intact" were the only assertion, a
  # daemon that never overwrote anything would pass it. Moving the validation
  # after the RemoveAll reddens act two; making the rename a no-op reddens
  # act three.
  Scenario: A stream-in whose key climbs out of steps/ plants nothing, and a permitted overwrite still replaces
    Given a real artifact daemon guarding its storage root
    And the store holds a file "victim/precious.txt" reading "original"
    And the store holds a file "steps/build-x/out/keep.txt" reading "KEEP-ME"
    When the ATC streams the "ordinary" archive in under the percent-encoded key "../victim"
    Then the daemon replies with 400
    And the refusal says "has an empty or relative segment"
    And the store's file "victim/precious.txt" reads "original"
    And the store has nothing at "victim/new.txt"
    When the ATC streams the "ordinary" archive in under the percent-encoded key "../steps/build-x/out"
    Then the daemon replies with 400
    And the refusal says "has an empty or relative segment"
    And the store's file "steps/build-x/out/keep.txt" reads "KEEP-ME"
    When the ATC streams the "ordinary" archive in under the key "build-x/out"
    Then the daemon replies with 201
    And the store's file "steps/build-x/out/new.txt" reads "NEW"
    And the store has nothing at "steps/build-x/out/keep.txt"

  # /register takes an ABSOLUTE path chosen by the caller rather than a key
  # the daemon joins, so it is a different validator with the same job. An
  # accepted alias to a path outside the root is an arbitrary read of the
  # node: GET /artifacts/<key> then serves any file the daemon can open.
  #
  # The alias-store assertion is the part that is easy to leave out and
  # expensive to lose. This handler used to persist the alias BEFORE it
  # finished validating, so a 400 left a poisoned entry in aliases.json that
  # was reloaded at the next boot — the refusal was undone by a restart. It
  # is asserted against the file on disk, not the in-memory registry, because
  # that is where the defect lived. Registering "vol-file" first is what makes
  # it non-vacuous: the file exists and names one alias, and the refused one
  # is absent from it.
  #
  # "vol-file" also carries a regression of its own. When GET's directory
  # branch moved to the alias path and the file branch was left opening the
  # key — the key that did not exist, which is why the fallback ran — a
  # file-valued alias started 500ing. No test had ever registered an alias to
  # a file.
  Scenario: An alias to a path outside the storage root is refused, and never reaches the alias store
    Given a real artifact daemon guarding its storage root
    And a directory outside that root holding a file that reads "original"
    And the store holds a file "legacy/blob.tar" reading "LEGACY-BYTES"
    When the ATC registers "vol-file" as living at the stored path "legacy/blob.tar"
    Then the daemon replies with 201
    And the alias store on disk names "vol-file"
    When the ATC registers "pwn" as living at the file outside the storage root
    Then the daemon replies with 400
    And the refusal says "resolves outside the storage root"
    And the alias store on disk does not name "pwn"
    When the ATC sends "GET" for the artifact key "pwn"
    Then the daemon replies with 404
    And the answer does not contain "original"
    When the ATC sends "GET" for the artifact key "vol-file"
    Then the daemon replies with 200
    And the answer's bytes are "LEGACY-BYTES"

  # /resolve is the mTLS-EXEMPT endpoint — reachable with no client
  # certificate at all — and it takes a destination the caller chose. An
  # unvalidated dest is an arbitrary WRITE, and copyArtifact makes it worse
  # than that: it creates its temp directory as a SIBLING of dest and does
  # os.RemoveAll(dest) first, so dest == the storage root writes into the
  # root's parent (a host directory in production) and then removes the entire
  # store. Rel reports "." for that case, and the first cut of the validator
  # accepted it — which is why "the root itself" is its own act with its own
  # refusal text rather than being folded into the outside-the-root one.
  Scenario: A resolve destination outside the storage root is refused, and the root itself is not a destination
    Given a real artifact daemon guarding its storage root
    And a directory outside that root holding a file that reads "original"
    And the store holds a file "steps/srcbuild/out/payload.txt" reading "PAYLOAD"
    When the ATC asks it to resolve "srcbuild/out" into a destination outside the storage root
    Then the daemon replies with 400
    And the refusal says "resolves outside the storage root"
    And nothing was created at that destination outside the root
    And the file outside the storage root still reads "original"
    When the ATC asks it to resolve "srcbuild/out" into the storage root itself
    Then the daemon replies with 400
    And the refusal says "is the storage root itself"
    And the store's file "steps/srcbuild/out/payload.txt" reads "PAYLOAD"
    When the ATC asks it to resolve "srcbuild/out" into the stored destination "resolved/input"
    Then the daemon replies with 200
    And the store's file "resolved/input/payload.txt" reads "PAYLOAD"

  # /resolve-batch is the least authenticated way into resolveOne: mTLS-exempt
  # like /resolve, and it takes a key and a dest PER ITEM. It was unvalidated
  # in the first cut of this work and was found by re-deriving the acceptance
  # criteria, not by a review.
  #
  # The claim that needs the two-item form is ordering. Validating each item as
  # its goroutine starts would refuse item 1 correctly and still let item 0
  # copy — a side effect from a refused request. Asserting that the FIRST
  # destination is empty is what distinguishes "validated up front" from
  # "validated eventually"; the status alone cannot.
  Scenario: One destination outside the root refuses the whole batch, before any of it has copied
    Given a real artifact daemon guarding its storage root
    And the store holds a file "steps/batchsrc/out/payload.txt" reading "PAYLOAD"
    When the ATC asks it to resolve "batchsrc/out" into the stored destination "resolved/first" and into a destination outside the storage root
    Then the daemon replies with 400
    And the refusal says "item 1"
    And the refusal says "resolves outside the storage root"
    And the store has nothing at "resolved/first"
    And nothing was created at that destination outside the root
    When the ATC asks it to resolve "batchsrc/out" into the stored destinations "resolved/first" and "resolved/second"
    Then the daemon replies with 200
    And the store's file "resolved/first/payload.txt" reads "PAYLOAD"
    And the store's file "resolved/second/payload.txt" reads "PAYLOAD"

  # ==========================================================================
  # Names that describe the store rather than name an artifact
  # ==========================================================================

  # Every route below is a SINGLE-ARTIFACT verb, and each of these names
  # addresses the shape of the store instead. The amplification is total:
  # DELETE /artifacts/steps removed every artifact on the node and answered
  # 204; GET /artifacts/steps tarred the lot; POST /mirror {"key":"."} tars
  # every artifact on the node and PUTs it to every peer — one unauthenticated
  # request each.
  #
  # Three things here were each shipped green once and are the reason this is
  # a sweep rather than three scenarios:
  #
  #   - "." was rejected on the reasoning that these are single-artifact verbs.
  #     That reasoning is right and was applied to exactly one string. The
  #     structural names are the rest of it.
  #   - os.Root refuses "." for Remove and RemoveAll only. Root.Stat(".") and
  #     Root.Open(".") succeed and enumerate the whole store, so a draft that
  #     deleted the "." check "because os.Root refuses it" would have kept
  #     DELETE safe and opened GET. Hence every verb, and hence the assertion
  #     that no answer carried the store's listing.
  #   - the names are folded for case because APFS and NTFS fold, so
  #     DELETE /artifacts/STEPS reached the same directory that /steps did
  #     while an exact-string map let it through. Not exploitable on a Linux
  #     production node — but a guarantee that depends on the filesystem is
  #     not a guarantee.
  #
  # The mirror of an ordinary key at the end is the permission: /mirror must
  # still accept work, or every artifact silently stops being replicated.
  Scenario: A name that describes the store, not an artifact, is refused on every verb that takes one
    Given a real artifact daemon guarding its storage root
    And the store holds a file "steps/keep/out/f.txt" reading "KEEP"
    And the store holds an alias file naming "legit"
    When the ATC sends every per-artifact verb for the percent-encoded artifact key "."
    Then every one of those answers was 400
    And none of those answers listed the store's contents
    When the ATC asks the daemon to mirror the key "."
    Then the daemon replies with 400
    And the refusal says "names the storage root itself"
    When the ATC asks the daemon to mirror the key "../../etc"
    Then the daemon replies with 400
    And the refusal says "has an empty or relative segment"
    When the ATC sends "DELETE" for each of the artifact keys "steps, STEPS, Steps, sTePs, artifacts, caches, resource-caches, aliases.json, ALIASES.JSON, aliases.json.tmp"
    Then every one of those answers was 400
    And every one of those refusals said "names a structural path"
    When the ATC streams the "ordinary" archive in under each of the keys "steps, STEPS, aliases.json"
    Then every one of those answers was 400
    And every one of those refusals said "names a structural path"
    And the store has nothing at "steps/steps"
    When the ATC sends "GET" for each of the artifact keys "steps, aliases.json"
    Then every one of those answers was 400
    And every one of those refusals said "names a structural path"
    When the ATC asks the daemon to mirror the key "keep/out"
    Then the daemon replies with 202
    And the alias store on disk names "legit"
    And the store's file "steps/keep/out/f.txt" reads "KEEP"

  # The permission side of the key rule, and the reason it is spelled out at
  # this length. durable.ValidateKey exists, looks like the right validator,
  # and caps keys at two segments — and real traffic runs to three:
  # "steps/build-42/result" and "caches/job-42/build-abc.tar" are ordinary.
  # Reusing it would have refused production traffic, which is a worse outage
  # than the bug, because refusing to deliver artifacts is at least visible.
  #
  # The charset half is the same mistake one layer down. Concourse's own
  # identifier rule admits any Unicode letter and is only a warning, and the
  # user-controlled segment arrives here as handle + "/" + subdir — so "café",
  # "_out", "-lead" and ".git" are legal pipeline config today. A daemon that
  # 400s them breaks builds and shows up in no test. Safety here comes from
  # the traversal check and the root handle, not from a charset.
  #
  # The last act is the decoding claim: validation reads the DECODED path, so
  # a benign percent-encoding has to still resolve. Without it, a validator
  # could pass everything above by refusing all encoded input.
  Scenario: The keys production actually sends are still accepted
    Given a real artifact daemon guarding its storage root
    And the store holds a file "steps/build-42/out/f.txt" reading "x"
    And the store holds a file "build-42-output.tar" reading "x"
    And the store holds a file "steps/build-99" reading "x"
    And the store holds a file "steps/build-42/result" reading "x"
    And the store holds a file "caches/job-42/build-abc.tar" reading "x"
    When the ATC sends "HEAD" for each of the artifact keys "build-42-output.tar, steps/build-99, steps/build-42/result, caches/job-42/build-abc.tar"
    Then every one of those answers was 200
    When the ATC streams the "ordinary" archive in under each of the keys "café/out, _out/x, -lead/x, .git/x"
    Then every one of those answers was 201
    When the ATC sends "GET" for the artifact key "steps/build%2D42/out"
    Then the daemon replies with 200

  # ==========================================================================
  # What an archive may contain
  # ==========================================================================

  # An archive arriving from a peer, from the shared bucket, or from a fly
  # upload is untrusted input: it may name any path and point a link anywhere.
  # os.Root contains the WRITES an extraction performs, but it deliberately
  # does not contain the LINKS — "Symlink does not validate oldname, which may
  # reference a location outside the root" — so a handle alone yields a safe
  # extraction that leaves an outward-pointing link on disk for the next
  # consumer to follow with an ordinary os.ReadFile. Both halves are needed,
  # and both are here.
  #
  # A row per refusal REASON, because "refused" for the wrong reason is the
  # failure this family produces. The two absolute-target rows look alike and
  # are not: a hard link is a different tar type, and the extractor once
  # handled Dir/Reg/Symlink only, so a TypeLink entry vanished silently and
  # the extraction still reported success. Dropping the TypeLink arm reddens
  # that row and nothing else.
  #
  # Every row also asserts the two things a status cannot: the destination was
  # never promoted, and no extraction temp directory was left behind for a
  # later reader to address as an artifact.
  #
  # It does NOT assert that the file outside the root is unchanged, and the
  # reason is worth knowing. That assertion was here and it could not fail:
  # measured, by removing the daemon's own symlink-containment check and
  # driving every row at it. The file still read "original". os.Root creates a
  # symlink but never FOLLOWS one, so the write through it fails with
  # "mkdirat hatch: file exists" before it can reach anything — containment
  # before capability, holding even with the daemon's own guard deleted.
  #
  # The escaping rows still pair the link with a write through it, which is
  # the shape of the real attack and what the source case used. What the rows
  # actually pin is the status and the refusal REASON: with the guard removed
  # row 3 reddens because the daemon reports an incidental EEXIST instead of
  # naming the escape.
  Scenario Outline: An archive that reaches outside the artifact is refused — <case>
    Given a real artifact daemon guarding its storage root
    And a directory outside that root holding a file that reads "original"
    When the ATC streams the "<case>" archive in under the key "probe/out"
    Then the daemon replies with 400
    And the refusal says "<reason>"
    And the store has nothing at "steps/probe/out"
    And no extraction temp directory is left under steps

    Examples:
      | case                          | reason                           |
      | traversing entry name         | path escapes from parent         |
      | absolute symlink              | targets an absolute path         |
      | symlink out of the artifact   | resolves outside the destination |
      | hard link out of the artifact | targets an absolute path         |
      | unsupported entry type        | has unsupported type             |

  # Corrupt input is a different question from hostile input, and the daemon
  # has to answer it differently. mirror.go reads any non-201 from a peer as
  # "the peer rejected it", so an archive-attributable failure reported as 500
  # makes a poisoned or truncated artifact look like the RECEIVING NODE is
  # broken — an operator then goes and restarts the wrong thing. The first
  # draft of this classification sent malformed tar and every os.Root
  # traversal refusal to 500: the two most likely hostile inputs both landing
  # on the peer-fault side of the split invented to prevent exactly that.
  #
  # A truncated archive is the likeliest corrupt input in production and takes
  # a different path out of the extractor than a malformed header does, which
  # is why both are here. Widening the ErrRefused classification to cover
  # everything, or narrowing it to nothing, reddens this.
  Scenario: A corrupt archive is the archive's fault, and the daemon says which fault it was
    Given a real artifact daemon guarding its storage root
    When the ATC streams the "malformed" archive in under the key "m/o"
    Then the daemon replies with 400
    And the refusal says "reading tar"
    When the ATC streams the "truncated" archive in under the key "t/o"
    Then the daemon replies with 400
    And the refusal says "write file"
    When the ATC streams the "ordinary" archive in under the key "ok/o"
    Then the daemon replies with 201

  # Containment must not become prohibition. An artifact's own internal links
  # are part of the payload format — two worktrees in one artifact sharing a
  # dependency tree by relative link is the real case, and node_modules is the
  # real name — so a rule that refused every symlink would pass every scenario
  # above and quietly corrupt every artifact that had one.
  #
  # The link is asserted three ways because each catches a different way of
  # getting this wrong: that it is still a symlink (not silently dereferenced
  # into a copy), that its target was not rewritten, and that it still
  # RESOLVES to the shared tree inside the artifact. The hard link is here for
  # the fourth: it used to be dropped on the floor by an extractor that
  # switched on three types, and the extraction reported success anyway —
  # silent data loss that no status code could show.
  Scenario: Containment is not prohibition — an artifact's own links survive and still resolve
    Given a real artifact daemon guarding its storage root
    When the ATC streams the "internal links" archive in under the key "build-x/out"
    Then the daemon replies with 201
    And the store's link "steps/build-x/out/app/node_modules" points at "../shared"
    And the store's file "steps/build-x/out/app/node_modules/pkg.txt" reads "deps"
    And the store's file "steps/build-x/out/a/b/hard" reads "payload"

  # A refusal that happens partway through an extraction has already written
  # entries. Three separate things have to be true afterwards and none of them
  # follows from the status:
  #
  #   - the destination must not exist, so a reader that treats any
  #     steps/{key} directory as a complete artifact never sees a partial one;
  #   - the extraction temp directory must be gone, not merely unpromoted —
  #     otherwise it sits under steps/ where it is addressable as an artifact
  #     and countable by the sweeper;
  #   - the error must name the REAL cause. A retry that re-runs over its own
  #     residue reports "file exists", and an operator reading that goes
  #     looking for a disk problem instead of a poisoned archive.
  #
  # The last act is the landmine claim. It is not enough that the absolute
  # symlink was refused: what mattered in the original reproduction was that
  # the link never became a readable path afterwards, because the read that
  # follows it uses an ordinary open with no root handle.
  # It used to also assert the refusal does not say "file exists". That could
  # never fail on this route. The property came from PeerResolver.Fetch, whose
  # THREE-ATTEMPT retry loop can re-run an extraction over its own residue and
  # report an incidental EEXIST instead of the real cause. handleStreamIn has
  # no retry loop, and each request extracts into a fresh random .in-tmp-<rand>
  # directory, so no sequence of stream-ins can produce that residue. The
  # property is real and is still covered in the daemon's own Go suite; it is
  # simply not reachable through this door.
  Scenario: A refused extraction leaves nothing behind — no partial tree, no temp directory, no landmine
    Given a real artifact daemon guarding its storage root
    And a directory outside that root holding a file that reads "original"
    When the ATC streams the "good entries then a bad one" archive in under the key "residue/out"
    Then the daemon replies with 400
    And the refusal says "targets an absolute path"
    And the store has nothing at "steps/residue"
    And no extraction temp directory is left under steps
    When the ATC streams the "absolute symlink to the file outside" archive in under the key "evil"
    Then the daemon replies with 400
    When the ATC sends "GET" for the artifact key "steps/evil/pwn"
    Then the daemon replies with 404
    And the answer does not contain "original"

  # ==========================================================================
  # Boundaries that bind after the fact
  # ==========================================================================

  # The steps/ boundary is a NESTED HANDLE, not an argument, and this is the
  # scenario that says why the difference matters. An earlier version passed
  # the boundary as a parameter and then validated against a different one —
  # s.storagePath rather than the root the caller handed over — so a symlink
  # under steps/ pointing at the store ROOT satisfied "contained" while
  # plainly escaping steps/. PUT /stream-in/x/link/aliases.json then destroyed
  # the alias file and answered 201.
  #
  # The link is planted DIRECTLY ON DISK rather than through the API, and that
  # is the point. An absolute symlink is refused at ingress now, so this link
  # can no longer be created through stream-in — but one may predate the rule,
  # or arrive by another path. A boundary that only holds for links it created
  # itself is not a boundary.
  #
  # The status recorded here is 500, and it is the odd one out in this file.
  # The refusal comes from the nested root declining to mkdir through the
  # link, not from a validator declining the key, so it is attributed as an
  # environment failure rather than an archive one. That is a real wart and
  # this scenario is the record of it — but the refusal still NAMES the link
  # it stopped at, which is what distinguishes it from an unrelated 500.
  Scenario: A symlink already on disk under steps/ cannot carry a stream-in out to the store root
    Given a real artifact daemon guarding its storage root
    And the store holds an alias file naming "legit"
    And a symlink is planted on disk at "steps/x/link" pointing at the storage root
    When the ATC streams the "ordinary" archive in under the key "x/link/aliases.json"
    Then the daemon replies with 500
    And the refusal says "x/link"
    And the alias store on disk names "legit"

  # Registering a contained path is not enough, and this is the scenario that
  # shows why validating only at registration is a snapshot. The registry is a
  # cache of STRINGS: an alias registered legitimately can have its target
  # swapped for a link afterwards, and aliases.json is reloaded at boot from
  # whatever was persisted — including entries written before any of these
  # rules existed. So the check has to live in the lookup, where a new caller
  # cannot write around it.
  #
  # Swept across both routes that read the registry because the first attempt
  # added the check at the two sites the reproduction happened to use and left
  # three reading it raw, /resolve among them. Both routes are exercised
  # WORKING first: the alias resolves, the resource cache is served, and the
  # daemon advertises that it holds it. Then the target is swapped underneath
  # and all three stop — which is the only way to tell a guard that evicts
  # poison from one that never worked.
  Scenario: An alias whose target is swapped for a link out of the root stops being served everywhere
    Given a real artifact daemon guarding its storage root
    And a directory outside that root holding a file that reads "original"
    And the store holds a file "steps/a1/d/payload.txt" reading "PAYLOAD"
    And the store holds a file "steps/rc/f" reading "ok"
    When the ATC registers "alias1" as living at the stored path "steps/a1/d"
    Then the daemon replies with 201
    When the ATC registers "rc-9" as living at the stored path "steps/rc/f"
    Then the daemon replies with 201
    When the ATC asks it to resolve "alias1" into the stored destination "resolved/before"
    Then the daemon replies with 200
    And the store's file "resolved/before/payload.txt" reads "PAYLOAD"
    When the ATC asks for the resource cache "rc-9"
    Then the daemon replies with 200
    And the answer's bytes are "ok"
    When the ATC asks whether the daemon holds the resource cache "rc-9"
    Then the daemon replies with 200
    When the path behind "alias1" is swapped for a link to the directory outside the root
    And the ATC asks it to resolve "alias1" into the stored destination "resolved/after"
    Then the daemon replies with 404
    And the refusal says "not found on this node or any peer"
    And the store has nothing at "resolved/after"
    When the path behind "rc-9" is swapped for a link to the file outside the root
    And the ATC asks for the resource cache "rc-9"
    Then the daemon replies with 404
    And the answer does not contain "original"
    When the ATC asks whether the daemon holds the resource cache "rc-9"
    Then the daemon replies with 404
