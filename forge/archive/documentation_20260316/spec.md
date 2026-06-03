# Spec: JetBridge Documentation Update

**Track ID:** `documentation_20260316`
**Type:** docs

## Overview

Update the three main documentation files (JETBRIDGE.md, deploy/chart/README.md, README.md) to reflect all current JetBridge features and configuration. The primary audience is **LLMs reading these docs for context** — structure for dense, explicit, self-contained information retrieval rather than human narrative.

## Requirements

1. **JETBRIDGE.md** — Complete configuration reference with all current `--kubernetes-*`, `--tracing-*`, `--otel-*`, and `--gc-*` flags. Document new features: `skip_download`, configurable base resource types, GCS Fuse, direct image references, health endpoint, OTel tracing/metrics.
2. **deploy/chart/README.md** — Complete Helm values reference covering all sections in values.yaml: tracing, monitoring (ServiceMonitor, AlertingRules), NetworkPolicy, PDB, ingress, security contexts, GCS Fuse, base resource types, service account, image registry.
3. **README.md** — Light update to mention `skip_download`, configurable base resource types, health endpoint, and OTel in the feature summary sections.
4. **LLM-optimized format** — Use tables, explicit flag/value mappings, concrete examples, and structured sections. Minimize prose. Every config option should include its default value and type. Group related options logically.

## Acceptance Criteria

- [ ] All `--kubernetes-*` flags in `atc/atccmd/command.go` are documented in JETBRIDGE.md
- [ ] All tracing/OTel flags are documented
- [ ] All Helm values in `values.yaml` are documented in chart README
- [ ] `skip_download`, sidecars (including `image_artifact`), configurable base resource types, GCS Fuse, health endpoint are documented
- [ ] No stale/incorrect information remains
- [ ] Documents are self-contained — an LLM reading any one doc gets complete context for its scope

## Out of Scope

- Creating new documentation files
- Documenting upstream Concourse features (only JetBridge differences)
- CI agent system docs (already comprehensive)
- Agent feedback API docs (already comprehensive)
- Tutorial/walkthrough style content
