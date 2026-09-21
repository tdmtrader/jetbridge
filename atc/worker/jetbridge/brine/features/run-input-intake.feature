Feature: Authenticated local input intake

  A caller uploads bytes for one declared template input. The real node and
  PostgreSQL publish and protect the object before returning a source grant.
  Expiry releases only the upload's temporary claim, including when no Run was
  ever submitted; an admitted Run retains its independent claim.

  Scenario Outline: Upload authority and temporary ownership compose with Runs
    Given a managed input upload node
    When its authenticated input upload encounters <case>

    Examples:
      | case                        |
      | "valid upload"              |
      | "stable source"             |
      | "foreign principal"         |
      | "unknown team"              |
      | "unknown input"             |
      | "paused template"           |
      | "held activation"           |
      | "wrong epoch"               |
      | "missing authority"         |
      | "unconfigured upload"       |
      | "malformed archive"         |
      | "wrong node"                |
      | "unused upload expires"     |
      | "Run claim survives expiry" |
      | "expired grant"             |
      | "replay after expiry"       |
      | "expiry while held"         |
      | "revoked during stream"     |
      | "custom role revoked during stream" |
