@review @review-kubelet
Feature: An installed review completes after its client disconnects

  The committed template and review image run on the disposable CI kubelet.
  HTTP, PostgreSQL, storage, execution, credentials and result reads are real.
  Only the model process is replaced with deterministic output.

  Scenario Outline: A fresh client retrieves the detached review
    Given an installed review survives a disconnected "<surface>" client

    Examples:
      | surface |
      | CLI     |
      | MCP     |
