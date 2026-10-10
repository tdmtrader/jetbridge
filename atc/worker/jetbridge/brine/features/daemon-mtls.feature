Feature: Reaching the artifact daemon over mTLS

  These cases exercise the production ATC volume client against a real daemon.

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
  # Like the deployed daemon, this one requires client authentication too.
  # The missing-ServerName mutation still isolates this scenario: the first
  # scenario's certificate also names the dial address, while this one does not.
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
