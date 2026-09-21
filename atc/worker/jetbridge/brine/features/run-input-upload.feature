Feature: Publishing uploaded input bytes through the managed node

  This exercises the private node intake, real canonicalizer, TLS and object
  store. Run admission and temporary claim registration are separate operations.
  No task start, Pod completion or quiescence evidence is invented for an upload.

  Scenario Outline: A staged upload is private, bounded and published exactly
    Given a managed input upload node
    When its input upload encounters <case>

    Examples:
      | case                    |
      | "publish"               |
      | "deduplicate"           |
      | "unknown stage"         |
      | "invalid nonce"         |
      | "caller bucket"         |
      | "missing peer"          |
      | "malformed archive"     |
      | "node restart"          |
      | "expired stage"         |
