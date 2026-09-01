Feature: Resource behavior crosses the production client, API, and database

  Source: 90 initial specs: 27 non-injected-error specs in
  go-concourse/concourse/{resource,resourceversions}_test.go; 13 persisted
  success/not-found specs in atc/api/resources_test.go; 30 query, mutation, and
  clear-version specs in atc/api/versions_test.go; and 20 corresponding
  lifecycle, filtering, pinning, visibility, and sharing specs in
  atc/db/resource_test.go. Injected 500s, build relationships, cache-volume
  invalidation, and check-plan construction are deliberately not counted.

  Scenario: Resource and resource-type identity round-trip through the client
    When the production resource client completes journey "identity"
    Then the resource journey result is "list=1;found=true;name=image;missing=false;types-found=true;types=1"

  Scenario: Version cursors, filters, and pagination use persisted versions
    When the production resource client completes journey "pages"
    Then the resource journey result is "all=5;empty-pages=true;limited=2;next=true;filter=1;missing=false"

  Scenario: Version enablement, pinning, comments, and missing resources are observable
    When the production resource client completes journey "mutations"
    Then the resource journey result is "disable=true;disabled-state=true;enable=true;pin=true;pinned-ref=3;comment=release candidate;unpin=true;missing=true"

  Scenario: Clearing versions returns real counts and preserves unrelated scopes
    When the production resource client completes journey "clear"
    Then the resource journey result is "removed=5;remaining=0;preserved=true;type-removed=2"

  Scenario: Shared resource discovery follows a persisted common scope
    When the production resource client completes journey "shared"
    Then the resource journey result is "found=true;resources=image,image-two;types=0;missing=false"

  Scenario: Manual resource, resource-type, and prototype checks create builds
    When the production resource client completes journey "checks"
    Then the resource journey result is "resource=true:started;type=true:started;prototype=true:started"

  Scenario: Public flags preserve explicit and default configuration
    When the production resource client completes journey "public"
    Then the resource journey result is "true=true;default=false;false=false"
