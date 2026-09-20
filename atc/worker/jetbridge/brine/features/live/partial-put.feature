@live-kubernetes @partial-put-fault
Feature: A failed resource must not publish its partial output

  # Explicit fault injection approved by the user: the out executable emits
  # valid version JSON and exits 4. Worker, pool, DB, pod and exec remain real.
  Scenario: A put that named a version and then failed publishes nothing
    Given a build of a job whose pipeline has the resource "some-resource"
    And the resource names a version and then fails
    When the put step runs, publishing version "v3"
    Then the step failed rather than erroring
    And the build reported the put finishing with exit status 4
    And the build published nothing at all
