## Session Learnings: Artifact Daemon Permission Hardening (2026-04-12)

### Friction: cp flag portability (4 iterations)
- `cp -a` → `cp -r --preserve=mode,timestamps` (GNU-only, broke macOS tests)
- → `cp -Rp` (POSIX, but -p tries chown which fails as root without CAP_CHOWN)
- → `cp -R` (final — no ownership/mode preservation needed for cache copies)
- **Lesson**: GNU coreutils as root treats chown failure in `cp -p` as a hard error. Non-root just warns. Always consider the root+dropped-caps scenario.

### Friction: Manual capability audit
- Traced every filesystem op across 6 files, mapped to capabilities, determined PASS/FAIL
- Created `capability-auditor` agent to automate this for future hardening work

### Key Technical Insights
- `CAP_DAC_OVERRIDE` bypasses all DAC permission checks (read/write/execute/delete)
- `CAP_DAC_OVERRIDE` does NOT bypass immutable/append-only flags (needs CAP_SYS_ADMIN)
- K8s `fsGroup` does NOT apply to `hostPath` volumes — only emptyDir, PVC, etc.
- Root (uid 0) without capabilities is effectively a regular user that happens to have uid 0
- For a daemon managing a shared hostPath with arbitrary task UIDs: keep root + minimal caps is better than non-root

### Final Security Posture
- Drop ALL capabilities, add only `CAP_DAC_OVERRIDE`
- `allowPrivilegeEscalation: false`, seccomp RuntimeDefault
- Normalize tar extraction permissions (dirs ≥0755, files ≥0644)
- Strip setuid/setgid bits on extraction
- Atomic copy via temp dir + rename (prevents partial state on retries)
- `cp -R` (no -p) to avoid chown attempts