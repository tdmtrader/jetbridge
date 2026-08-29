@VT-02 @VT-03 @VT-04 @VT-05
Feature: Moving artifacts through volumes

  What a consumer of a volume actually does: put an artifact in, take it back
  out, hand it to the next step. Where it lands on disk, what command moved it,
  and which container ran that command are all mechanism.

  Source: jetbridge_storage_behavioral_spec_20260330 — VT-02 (StreamIn),
  VT-03 (StreamOut), VT-04 (path resolution), VT-05 (stub volumes).

  These scenarios replace ginkgo tests that asserted `call.podName`,
  `call.containerName`, `call.attrs.Purpose` and
  `call.command == ["tar","xf","-","-C","/tmp/build/inputs"]` against a
  recording double. Each scenario below fails for a real consumer; those did
  not.

  Scenario: An artifact comes back out as it went in
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "hello.txt" containing "hello world" is put into volume "inputs" at "."
    When volume "inputs" is read from "."
    Then the artifact "hello.txt" containing "hello world" is there

  # The round trip is NOT symmetric, and a consumer has to know it. StreamIn
  # resolves the path into the extraction target, so the artifact lands at
  # <mount>/sub/dir/nested.txt. StreamOut keeps the mount as the extraction
  # root and passes the path as a member SELECTOR, so members come back
  # carrying their path — whichever path you ask for. VT-02 and VT-03 specify
  # exactly this; no test had made it visible, because the ginkgo tests
  # compared command strings and command strings do not show it.
  Scenario: A member keeps its path when read back from that path
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "nested.txt" containing "deep content" is put into volume "inputs" at "sub/dir"
    When volume "inputs" is read from "sub/dir"
    Then the artifact "sub/dir/nested.txt" containing "deep content" is there

  Scenario: The same member is reachable from the volume root
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "nested.txt" containing "deep content" is put into volume "inputs" at "sub/dir"
    When volume "inputs" is read from "."
    Then the artifact "sub/dir/nested.txt" containing "deep content" is there

  # The story the volume-to-volume ginkgo test was really about. It asserted
  # two pod names, two command strings, and that the fake's canned bytes
  # reached the fake's recorded stdin. What matters is that the artifact
  # arrives.
  Scenario: One step's output becomes the next step's input
    Given a volume "output" mounted at "/tmp/build/workdir/output"
    And another volume "input" mounted at "/tmp/build/workdir/input"
    And a file "result.json" containing "built ok" is put into volume "output" at "."
    When the contents of volume "output" are moved into volume "input"
    And volume "input" is read from "."
    Then the artifact "result.json" containing "built ok" is there

  @VT-05
  Scenario: A stub volume refuses to be read rather than panicking
    Given a volume "real" mounted at "/tmp/build/inputs"
    And a stub volume "stub" with no cluster behind it
    When volume "stub" is read from "."
    Then it fails rather than panicking, saying "no executor"

  @VT-05
  Scenario: A stub volume refuses to be written rather than panicking
    Given a volume "real" mounted at "/tmp/build/inputs"
    And a stub volume "stub" with no cluster behind it
    When a file is put into volume "stub"
    Then it fails rather than panicking, saying "no executor"

  Scenario: A cluster failure reaches the reader rather than being swallowed
    Given a volume "real" mounted at "/tmp/build/inputs"
    And volume "broken" sits on a cluster that cannot run commands
    When volume "broken" is read from "."
    Then it fails rather than panicking, saying "exec failed"

  # A volume's identity is what the artifact repository keys on. A volume that
  # reported the wrong handle would hand the next step somebody else's
  # artifact — which is why this is asserted rather than assumed.
  Scenario: A volume identifies itself by its database handle
    Given a persisted volume on this worker
    Then the volume identifies itself by its database handle
    And the volume names the worker it lives on
    # The DB row is what survives a web restart; a volume that lost it would be
    # invisible to garbage collection.
    And both volume kinds still carry their database row

  # VT-08's other half. Every other streaming scenario uses gzip, which cannot
  # show that StreamIn decompresses anything: bsdtar auto-detects gzip, and
  # libarchive auto-detects zstd as well, so removing the decompressor leaves
  # them all passing. S2 is the encoding tar has no reader for, so it is the
  # one that proves the runtime did the work rather than the extractor.
  @VT-08
  Scenario: An artifact compressed with s2 is decompressed on the way in
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "packed.txt" containing "compressed payload" is put into volume "inputs" compressed with s2
    When volume "inputs" is read from "."
    Then the artifact "packed.txt" containing "compressed payload" is there

  # The write half of the swallow check. The scenario above it covers a read
  # that cannot reach the cluster; a WRITE that cannot reach it and reports
  # success is worse, because the step carries on believing its output landed
  # and the next step reads an empty directory.
  Scenario: A cluster failure reaches the writer rather than being swallowed
    Given a volume "real" mounted at "/tmp/build/inputs"
    And volume "broken" sits on a cluster that cannot run commands
    When a file is put into volume "broken"
    Then it fails rather than panicking, saying "exec failed"

  # ==========================================================================
  # Artifacts that live on another node (VT-06, VT-07)
  # ==========================================================================

  # Everything above moves bytes through a pod's own filesystem. Every input a
  # step did NOT produce itself arrives the other way: over the network, from
  # the artifact daemon on the node that produced it. That fetch has its own
  # failure modes, and until now none of them were stated as an outcome — the
  # tests that covered them built the volume by hand, swapped the HTTP
  # transport for one that rewrote every URL, and finished by reading a
  # counter the request handler had incremented.
  #
  # Below, the node is a real Node in the cluster, its address is resolved the
  # way production resolves it, and the daemon is a real HTTP server whose one
  # named difference is how it treats a connection.

  # VT-06's "retry up to 3 times" clause, which artifact-daemon.feature
  # deliberately left for this pass. A daemon pod that is rescheduled mid-build
  # drops connections that were already open; without the retry, every such
  # drop is a red build for an artifact sitting intact on disk one
  # reconnection away.
  #
  # This scenario really waits: the backoff is two seconds and two connections
  # are dropped, so it costs about four seconds. That is the price of stating
  # the retry as something a consumer experiences instead of as a call count.
  @VT-06
  Scenario: An artifact still arrives from a daemon that drops the first connections
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon drops the first 2 connections
    When the next step fetches the artifact from that node
    Then the artifact "release.tgz" containing "built on another node" is there

  # The other half of the same loop, and the more dangerous one. A daemon that
  # never completes a connection has to become a FAILED read. The alternative —
  # an empty stream and no error — sets a step to work on an input that was
  # never delivered, and it dies later on a missing file with nothing pointing
  # at the real cause.
  #
  # About four seconds as well, and for the same reason: three attempts.
  @VT-06
  Scenario: A daemon that never answers is a failed read, not an empty artifact
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon never completes a connection
    When the next step fetches the artifact from that node
    Then the read fails rather than handing back an empty artifact

  # "Gone" and "broken" send an operator to different places: a missing
  # artifact is a pipeline bug, a daemon returning 500 is an outage on that
  # node. artifact-daemon.feature covers the 404, so a 5xx was until now
  # indistinguishable from a miss anywhere in these features.
  @VT-06
  Scenario: A daemon that is failing says so rather than looking like a miss
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon is failing and answers every request with an internal error
    When the next step fetches the artifact from that node
    Then the read fails rather than handing back an empty artifact
    And the failure says the daemon is broken rather than that the artifact is gone

  # A producer that is asked and refuses is not the same situation as a
  # producer there is nobody to ask, and until now only the second was stated.
  # Every fallback scenario in these features — here and in
  # artifact-daemon.feature — takes the producing node OUT of the cluster, so
  # the ATC never gets as far as an address: the fallback it exercises is the
  # one that starts from having nowhere to look. The node in the two
  # scenarios below is still there. It resolves, the ATC builds the address
  # and dials it, and the fetch dies on the wire — the connection dropped in
  # the first, the port closed against it in the second — and everything that
  # keeps the build alive happens after that. A runtime that treated a failed
  # dial as the end of the search would pass every scenario written before
  # these two and lose the build on every rescheduled daemon pod.
  #
  # Both cost about four seconds: three attempts and two backoffs before the
  # producer is given up on, which is the retry that the scenarios above pin.

  # The half that keeps the build: the artifact is one mirror away, and the
  # step gets it. The peer's copy holds different text from the producer's, so
  # the scenario names which one arrived rather than only that something did.
  @VT-06
  Scenario: A peer's mirror still arrives when the producing node's daemon has stopped answering
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node's daemon never completes a connection
    And a peer daemon holds a mirrored copy of it containing "mirrored to a peer"
    And the ATC can ask the other daemons for a mirrored copy
    When the next step fetches the artifact from that node
    Then the artifact "release.tgz" containing "mirrored to a peer" is there

  # The half that diagnoses: nothing has the artifact, and the failure has to
  # be about the search, not about the first dial. "connection refused"
  # reaching the build log means the ATC stopped at the producer — it sends an
  # operator to hunt a network fault on a node that is up, for an artifact
  # that no daemon in the cluster is holding.
  @VT-06
  Scenario: A refused producer that nobody mirrored fails as a search that came up empty
    Given an artifact on another node holding the file "release.tgz" containing "built on another node"
    And that node is still in the cluster, and its daemon port refuses the connection
    And the ATC can ask the other daemons for a mirrored copy
    When the next step fetches the artifact from that node
    Then the read fails rather than handing back an empty artifact
    And the failure names the node and its peers rather than the refused connection

  # VT-07, and the write-side twin of "A cluster failure reaches the writer
  # rather than being swallowed" above. The locator that remembers which node
  # produced an artifact lives in memory, so a web restart forgets it; when
  # daemon discovery is not configured there is then nowhere at all to send
  # the bytes. Reporting success there is the worst available outcome: the
  # step finishes green and the next one reads an empty directory.
  @VT-07
  Scenario: Writing into an artifact with no daemon to send it to fails rather than reporting success
    Given an artifact whose producing node the web has forgotten, and no way to look it up
    When the step writes its output into that artifact
    Then the write fails rather than reporting an output that never left the web

  # ==========================================================================
  # Which volumes a step is handed, and where its pod is put (CO-05, CO-06,
  # CO-10, CO-12)
  # ==========================================================================

  # Before any of the above can happen, the worker has to decide what volumes
  # a step gets and which node to ask for. Both decisions are made in
  # Worker.buildVolumeMountsForSpec and the storage backend, and the cases
  # below are the ones no scenario reached.

  # CO-05 at the worker seam. An output that lands where an input already is
  # must reuse that input's volume rather than getting a second one. Two
  # volumes on one path is not untidiness: the step's outputs are registered
  # from one list and their locations recorded from another, so the artifact
  # the next step fetches and the artifact this step wrote would be different
  # objects with different handles.
  #
  # container-pod.feature states the same rule about the POD's volumes, which
  # a different function builds. The two have to agree, and nothing had said
  # so on this side.
  @CO-05
  Scenario: An output that lands on an input's path reuses that input's volume
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "shared-path-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/shared"
    And it produces an output at "/tmp/build/workdir/shared"
    When the container is created but not yet run
    Then the caller is handed 2 volumes in all
    And the caller is handed a volume mounted at "/tmp/build/workdir/shared"

  # The realistic spelling of the case above: Concourse output paths routinely
  # carry a trailing slash, and "shared/" and "shared" are the same directory.
  # Normalising the path is what makes them match — without it the dedup
  # quietly stops applying to most real pipelines while the scenario above
  # keeps passing.
  @CO-05
  Scenario: A trailing slash on the output path does not defeat the dedup
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "trailing-slash-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/shared"
    And it produces an output at "/tmp/build/workdir/shared/"
    When the container is created but not yet run
    Then the caller is handed 2 volumes in all
    And the caller is handed a volume mounted at "/tmp/build/workdir/shared"

  # CO-06/CO-12. A task declares its caches relative to its working directory
  # — `caches: [{path: my-cache}]` — and Kubernetes rejects a relative
  # mountPath outright, so a cache path left unresolved is a pod the API
  # server refuses to create. Every other cache in these features is absolute,
  # so the branch that resolves one ran nowhere.
  @CO-06 @CO-12
  Scenario: A cache named relative to the working directory is mounted inside it
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "relative-cache-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it caches "my-cache"
    When the container is created but not yet run
    Then the caller is handed 2 volumes in all
    And the caller is handed a volume mounted at "/tmp/build/workdir/my-cache"

  # CO-10. A step reads its inputs from the artifact daemon on whatever node
  # it lands on, and any input that is not already there crosses the network
  # first. container-pod.feature states the REQUIREMENT — the node must be
  # running an artifact daemon at all — and that is not the same thing: it is
  # satisfied by every node in the cluster. The preference on top of it is
  # what actually keeps the bytes local, and nothing in these features had
  # mentioned it, so a step placed away from every one of its own inputs was
  # invisible.
  @CO-10
  Scenario: A step is preferably placed on the node holding most of its inputs
    Given a jetbridge worker that places steps near their inputs
    And an input artifact that already lives on node "node-1"
    And an input artifact that already lives on node "node-2"
    And an input artifact that already lives on node "node-2"
    When the step is scheduled
    Then the step prefers to run on node "node-2"
