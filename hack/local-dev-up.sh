#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' \
	'ERROR: the fully local Colima/KinD workflow has been retired on this Mac.' \
	'Docker work must use ./hack/borg-docker.sh and theborg as documented in docs/docker-on-theborg.md.' \
	'The testcontainers Kubernetes suites remain CI-only because theborg container ports are not reachable here.' >&2
exit 2
