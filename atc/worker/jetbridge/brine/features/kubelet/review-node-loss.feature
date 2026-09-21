@review @review-kubelet
Feature: Session credentials do not survive loss of the node runtime

  Run separately after the other kubelet scenarios in disposable Linux CI.
  This kills the owned K3s container and restarts it with its disk intact;
  it does not simulate a power failure or authorize any production node action.

  Scenario: Destroying the node runtime destroys its original session memory
    Then a real review kubelet handles "a lost node runtime"
