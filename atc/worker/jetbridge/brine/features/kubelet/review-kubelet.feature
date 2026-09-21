@review @review-kubelet
Feature: The real kubelet enforces detached review lifecycle boundaries

  Run only against the explicitly marked disposable K3s CI cluster. Real
  PostgreSQL, node control, BusyBox, SPDY, worker and container memory are used.
  Only the model process is deterministic. Missing cluster evidence fails.

  Scenario Outline: Session lifetime and exact interruption are physical facts
    Then a real review kubelet handles <case>

    Examples:
      | case |
      | "active cancellation" |
      | "TERM-resistant cancellation" |
      | "read-only named input" |
      | "a session deadline" |
      | "a killed worker" |
