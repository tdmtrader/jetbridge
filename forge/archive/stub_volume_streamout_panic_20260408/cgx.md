# CGX — Stub Volume StreamOut Panic

## Session Log

_No sessions recorded yet._

## Friction Points

_None yet._

## Decisions

- **Option A chosen:** Return `DaemonSetVolume` from `FindDaemonResourceCache` instead of `NewStubVolume`, so that both the init-container path and the `StreamFile` path work correctly for daemon cache hits.
