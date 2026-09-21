@review-linux
Feature: Credentials belong to one invocation and one execution

  Scenario Outline: A Run admits at most one session credential delivery
    Given a Run producer and a ready output node
    Then its owner-bound credential handoff encounters <case>

    Examples:
      | case |
      | "accepted" |
      | "ready replay" |
      | "claimed replay" |
      | "foreign principal" |
      | "same display name" |
      | "revoked role" |
      | "unknown result" |
      | "waiting for start" |
      | "cancelled Run" |
      | "aborted build" |
      | "closed execution" |
      | "activation hold" |
      | "unpinned producer image" |
      | "a different pinned image" |
      | "no pinned image" |
      | "a tag-only pin" |
      | "oversized input" |
      | "role revoked during input" |
      | "cancelled during input" |
      | "concurrent delivery" |
      | "HTTP accepted" |
      | "HTTP ready replay" |
      | "HTTP anonymous" |
      | "HTTP viewer" |
      | "HTTP foreign owner" |
      | "HTTP custom create role" |
      | "HTTP operator hold" |
      | "HTTP shared client" |
      | "HTTP shared client replay" |
