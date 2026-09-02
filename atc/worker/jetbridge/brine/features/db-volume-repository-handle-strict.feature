Feature: Fixed volume handles survive production repository creation

  Source: VolumeRepository CreateVolumeWithHandle creates a CreatingVolume with a fixed handle.
  The scenario uses the production repository against a fresh PostgreSQL database.

  Scenario: A fixed volume handle is retained
    Given the real volume repository evaluates profile "create/fixed-handle"
    Then the volume repository observation is "handle=fixed-handle"
