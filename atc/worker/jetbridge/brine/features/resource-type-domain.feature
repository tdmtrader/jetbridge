Feature: Resource types persist configuration and create check work

  Source: all 22 specs in atc/db/resource_type_test.go. Collection, dependency
  trees, check builds/events/tracing, plan construction, and clearing versions
  all use production objects; related assertions are grouped by user journey.

  Scenario: Resource-type collections preserve fields, defaults, and activity
    When the real resource type domain evaluates profile "collection"
    Then the resource type domain result is "count=4;fields=true;merged=one;base=value;active=1"

  Scenario: Resource-type filtering follows the persisted dependency chain
    When the real resource type domain evaluates profile "filter"
    Then the resource type domain result is "tree=leaf,middle,root;count=3"

  Scenario: A resource type associates with its real config scope
    When the real resource type domain evaluates profile "scope"
    Then the resource type domain result is "scope=true"

  Scenario: Resource-type checks enforce concurrency and preserve events and traces
    When the real resource type domain evaluates profile "build"
    Then the resource type domain result is "created=true;started=true;events=3;trace=true;blocked=false;manual=true;after=true;ids=true"

  Scenario: Resource-type plans resolve base and nested image policy
    When the real resource type domain evaluates profile "plans"
    Then the resource type domain result is "base=true;nested=true;interval=true;privileged=true;local=true;recursive=true"

  Scenario: Clearing versions handles empty and shared histories
    When the real resource type domain evaluates profile "clear"
    Then the resource type domain result is "zero=0;removed=2;shared-empty=true"
