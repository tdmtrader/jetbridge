@live-kubernetes
Feature: A put resource consumes actual mounted inputs

  # Both inputs are real uploaded volumes; this is not automatic daemon staging.
  # The official pinned S3 resource uploads to an owned, quota-bound MinIO pod.
  Scenario: The S3 put keeps its inputs and exact output
    Given a task using Kubernetes
    When the S3 resource publishes its mounted binary input
    Then the put preserves both input mounts and forwards the exact resource response
