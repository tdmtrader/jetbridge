Feature: Reaching the artifact daemon over mTLS, and agreeing on who warms a cache

  Two things the ATC has to get right about the artifact daemon before any
  build sees a byte: it has to be able to talk to a daemon that will not talk
  to strangers, and every web process has to independently agree on which
  daemon should pull a cache out of durable storage.

  Both were previously guarded by reading fields off constructed objects. The
  mTLS half counted `transport.TLSClientConfig.Certificates`, checked
  `RootCAs != nil`, and compared `ServerName` to a string; the warm half
  compared two `[]daemonEndpoint` slices for element equality. None of that
  can tell a certificate that is configured from one that is presented, or a
  ServerName that was copied from one that is used to verify anything. The
  scenarios below use a real TLS daemon and a real handshake, and a cluster
  with a daemon on each of two nodes, so the assertion is on the artifact and
  on where the cache ended up.

  Source: daemon_tls_test.go (the ATC-side mTLS client) and daemon_warm_test.go
  (rendezvous ownership). Neither has a requirement identifier; both guard
  named production regressions, recorded below with the scenario that carries
  them.

  # -------------------------------------------------------------------------
  # The mTLS data plane
  # -------------------------------------------------------------------------

  # The daemon is deployed with `--client-auth=require`, so a web whose client
  # certificate is configured but never presented cannot read a single
  # artifact: every get step that needs a peer's output fails, and the build
  # goes red for a reason that looks like a network fault. The daemon here
  # really does refuse anyone it cannot verify, so the artifact arriving is
  # the proof that the certificate reached the handshake.
  Scenario: A daemon that only serves clients it can authenticate serves the ATC
    Given an artifact daemon serving over TLS
    And it only serves clients whose certificate it can verify
    And the daemon holds the artifact "abc" containing "tar data here"
    And the ATC has its client certificate and the daemon's CA
    When a consumer reads the artifact "abc" over mTLS
    Then the artifact arrives over mTLS as "tar data here"

  # The regression this exists for shipped: "certificate is valid for
  # 127.0.0.1, not <podIP>". Daemons are dialled by pod IP, which is assigned
  # at schedule time and can be in no SAN, while the chart issues the daemon a
  # certificate for the headless service name. The ATC therefore has to pin
  # verification to that service name rather than to whatever address it
  # happened to dial. Here the daemon's certificate names ONLY the service and
  # the ATC dials it at an address the certificate never mentions — the exact
  # shape of the outage — and the artifact still has to arrive.
  #
  # This daemon does not demand a client certificate, so the client-certificate
  # question above cannot be what decides this one.
  Scenario: An artifact arrives from a daemon reached at an address its certificate does not name
    Given an artifact daemon serving over TLS
    And its certificate names only the service, not the address it is dialled at
    And the daemon holds the artifact "abc" containing "tar data here"
    And the ATC has its client certificate and the daemon's CA
    When a consumer reads the artifact "abc" over mTLS
    Then the artifact arrives over mTLS as "tar data here"

  # A deliberate decision, and the one an operator meets during a bad cert
  # rollout: when the configured certificate files cannot be loaded, the ATC
  # does NOT quietly carry on over an unauthenticated path. It keeps dialling
  # the daemon the way mTLS says to, and the read fails naming the certificate
  # — so the misconfiguration is visible in a build log the same hour it is
  # deployed, rather than being discovered later as artifacts moving in the
  # clear.
  #
  # Both halves of the sentence carry weight. A client that fell back to
  # plaintext, or that skipped verification to "keep things working", would
  # hand back the artifact and fail the first half; a failure for any unrelated
  # reason would fail the second.
  Scenario: A missing client certificate fails the read rather than quietly dropping to an unauthenticated path
    Given an artifact daemon serving over TLS
    And the daemon holds the artifact "abc" containing "tar data here"
    And the ATC is configured for mTLS but its certificate files are not there
    When a consumer reads the artifact "abc" over mTLS
    Then the read is refused, naming "certificate"

  # -------------------------------------------------------------------------
  # Who warms a cache
  # -------------------------------------------------------------------------

  # Every ATC process must independently choose the same daemon to pull a given
  # cache out of the bucket. When they disagree, N concurrent builds each drag
  # a private copy of the same multi-gigabyte object onto a different node —
  # the exact egress bill the durable tier exists to avoid.
  #
  # Agreement is reached by ranking daemons with a rendezvous hash, and what
  # that hash is keyed on is the whole question. A DaemonSet rolling update
  # replaces every pod at once and the addresses are handed back out from a
  # pool, so ranking on the address would reshuffle the owner of EVERY key at
  # the single most common churn event in the cluster — invalidating every
  # warmed cache in the fleet simultaneously, which is worse than not caching
  # at all. Ranking on the node name survives it: pods come and go, nodes stay.
  #
  # The cache is reclaimed between the two warms because an age-based sweep
  # runs on every node; without that the second lookup would be a local hit and
  # would never ask the ranking anything.
  Scenario: A rolling update does not move which node owns a cache
    Given artifact daemons on two nodes with one durable store behind them
    And only the durable store holds the object "sha256:cafe" containing "cached resource data"
    When a get step warms the resource cache "rc-42" under content key "sha256:cafe"
    And the sweeper reclaims every node's copy of "rc-42"
    And the DaemonSet rolls and every pod comes back answering on a different address
    And a get step warms the resource cache "rc-42" under content key "sha256:cafe" again
    Then every warm was served
    And both warms left the cache on the same node

  # DISPOSITION — the rest of daemon_tls_test.go stays in Go. daemonURLScheme
  # and daemonTLSServerName are pure functions mapping one input to one string;
  # newDaemonStreamingHTTPClient's "no whole-request timeout" guards a real
  # shipped bug (a timeout severing a tar stream mid-body as "unexpected EOF")
  # but a scenario that reddened on it would need a daemon trickling an
  # artifact for longer than the timeout, which is slow and only weakly
  # discriminating; and the two BuildFetchInitContainers cases read substrings
  # out of a generated shell script, which is the mechanism this file and
  # artifact-daemon.feature both refuse to talk about.
  #
  # DISPOSITION — the rest of daemon_warm_test.go stays in Go too.
  # Order-independence and key spreading are properties of a returned slice
  # over a table of inputs, and at the seam order-independence degenerates into
  # the same "same node twice" assertion as the scenario above with a strictly
  # weaker discriminator. The negative cache's eviction assertion reads an
  # unexported map directly, and its behavioural form would need a sixty-second
  # wait or an injectable clock that does not exist.
