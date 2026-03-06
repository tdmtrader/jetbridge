# Spec: finish-setup

**Track ID:** `finish_setup_20260303`
**Type:** chore

## Overview

Migrate all Conductor tracks and supporting files to the Forge directory structure to complete the Conductor-to-Forge transition.

## Requirements

1. Copy all active Conductor tracks to `forge/tracks/`
2. Copy all archived Conductor tracks to `forge/archive/`
3. Sort tracks by status — completed/archived tracks go to `forge/archive/`
4. Deduplicate tracks that exist in both `conductor/tracks/` and `conductor/archive/`
5. Rebuild `forge/tracks.md` with all migrated tracks and correct links

## Acceptance Criteria

- [x] All 35 completed/archived tracks are in `forge/archive/`
- [x] Active tracks remain in `forge/tracks/`
- [x] `forge/tracks.md` lists all tracks with correct links
- [x] No data loss — all spec.md, plan.md, cgx.md, metadata.json, learnings.md, and note files preserved

## Out of Scope

- Removing the `conductor/` directory (user can do this when confident)
- Changing track formats or metadata schemas
- Backfilling empty plan.md files for tracks that were never planned granularly
