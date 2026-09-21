Feature: Registering an uploaded input before granting ownership

  The real node publishes bytes and PostgreSQL retains the logical reservation.
  Registration and a consumer claim use one transaction; no execution or
  quiescence evidence is fabricated for uploaded bytes.

  Scenario Outline: Publication evidence composes with durable ownership
    Given a managed input upload node
    When its input registration encounters <case>

    Examples:
      | case                         |
      | "register and claim"         |
      | "reservation replay"         |
      | "missing reservation"        |
      | "reservation rollback"       |
      | "conflicting reservation"    |
      | "registration rollback"      |
      | "registration replay"        |
      | "changed nonce"              |
      | "changed generation"         |
      | "changed node"               |
      | "changed signature"          |
      | "expired reservation"        |
      | "pending adoption shield"    |
      | "pending reclaim shield"     |
      | "reclaim first"              |
      | "commit after deadline"      |
      | "reservation late commit"    |
      | "database nonce mutation"    |
      | "database receipt mutation"  |
      | "policy at risk"             |
