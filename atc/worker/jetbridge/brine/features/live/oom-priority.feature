@RF-09 @live-kubernetes
Feature: A real OOM is diagnosed ahead of its crash loop

  # The kubelet supplies Running, CrashLoopBackOff and last OOMKilled.
  # This explicitly exercises direct compatibility Process.Wait.
  Scenario: An OOM kill is reported ahead of the crash loop it caused
    Given a task using Kubernetes
    When the compatibility watcher diagnoses a task repeatedly killed by its memory limit
    Then the step fails naming "OOMKilled"
    And the failure does not mention "CrashLoopBackOff"
