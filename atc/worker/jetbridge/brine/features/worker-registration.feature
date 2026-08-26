@WR-01 @WR-02 @WR-03 @WR-04 @WR-05 @WR-06
Feature: A Kubernetes worker presenting itself to Concourse

  The worker has to appear in `fly workers` looking like any other worker: a
  stable name, a platform, a version, a live lease, an honest container count,
  and the resource types it can run. Everything a scheduler needs to decide
  whether to place work on it.

  Source: k8s_runtime_behavioral_spec_20260331 — WR-01 to WR-06. Migrated
  whole from registrar_test.go, which carried no requirement identifiers.

  # WR-01: the name is derived, not configured, so a restart re-registers the
  # same worker rather than creating a second one.
  @WR-01 @WR-02
  Scenario: A registered worker looks like a worker
    Given a Kubernetes worker registrar for namespace "test-namespace"
    When the worker registers itself
    Then the worker is registered as "k8s-test-namespace"
    And it presents itself as a running linux worker on this Concourse version
    And it belongs to no team and is not ephemeral
    And its lease has not expired
    And its lease expires within 1 minute

  # WR-04. The count is what the scheduler uses for placement, so counting a
  # stranger's pod would make this worker look busier than it is.
  @WR-04
  Scenario Outline: The container count reflects this worker's pods only — <case>
    Given a Kubernetes worker registrar for namespace "test-namespace"
    And <mine> pods belonging to this worker are running
    And <theirs> pods belonging to nobody are running
    When the worker registers itself
    Then it reports <counted> active containers

    Examples:
      | case                  | mine | theirs | counted |
      | nothing running       | 0    | 0      | 0       |
      | one pod               | 1    | 0      | 1       |
      | several pods          | 3    | 0      | 3       |
      | unlabelled bystander  | 1    | 1      | 1       |

  # WR-03. Registration and heartbeat are the same idempotent call; a worker
  # whose lease lapses is dropped from scheduling.
  @WR-03
  Scenario: A heartbeat renews a lease that has run out
    Given a Kubernetes worker registrar for namespace "test-namespace"
    When the worker registers itself
    And the lease expires and the worker heartbeats
    Then its lease has not expired
    # The CEILING is the safety property. A deletion probe widened the TTL from
    # 30 seconds to 24 hours and the suite stayed green — an unbounded lease
    # means the scheduler keeps placing work on a worker that is already gone.
    And its lease expires within 1 minute

  # WR-05
  @WR-05
  Scenario: A worker offers the built-in resource types
    Given a Kubernetes worker registrar for namespace "test-namespace"
    When the worker registers itself
    Then it offers the resource type "git" as image "concourse/git-resource"
    And it offers the resource type "registry-image" as image "concourse/registry-image-resource"
    And it offers the resource type "time" as image "concourse/time-resource"
    And it offers the resource type "s3" as image "concourse/s3-resource"

  # WR-06
  @WR-06
  Scenario: An operator's image overrides reach the registered worker
    Given a Kubernetes worker registrar for namespace "test-namespace"
    And the operator overrides the resource type images with "git=my-registry/custom-git,custom-type=my-registry/custom"
    When the worker registers itself
    Then it offers the resource type "git" as image "my-registry/custom-git"
    And it offers the resource type "custom-type" as image "my-registry/custom"
    And it offers the resource type "registry-image" as image "concourse/registry-image-resource"

  # A worker that cannot record itself must say so. Reporting success would
  # leave the scheduler placing work on a worker Concourse has no record of.
  Scenario: Registration reports a lost database rather than appearing to work
    Given a Kubernetes worker registrar for namespace "test-namespace"
    And the database connection has been lost
    When the worker registers itself
    Then registration fails saying "saving worker"
