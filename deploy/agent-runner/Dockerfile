FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/agent-runner ./cmd/agent-runner
RUN CGO_ENABLED=0 go build -o /out/function-runner ./cmd/function-runner
RUN CGO_ENABLED=0 go build -o /out/agent-output ./cmd/agent-output

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS claude
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN curl --fail --location --silent --show-error \
      https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/2.1.212/linux-x64/claude \
      --output /tmp/claude \
    && echo "044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e  /tmp/claude" | sha256sum --check --strict \
    && install -D -m 0555 /tmp/claude /out/claude

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
# Pin to the claude-code version the live theborg review job currently runs
# (check a recent agent-review build log before bumping).
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
# NOTE: the Elm 0.19.1 + uglify-js toolchain (WF-2, for front-end tickets) was
# REMOVED from this image — the build pod's egress to external hosts is
# unreliable/blocked: the elm-binary download (github release CDN) hit
# `curl: (35) Connection reset by peer` and the ELM_HOME pre-warm could not reach
# package.elm-lang.org (builds #8/#9/#10 all failed). It only made the agent-runner
# image build flaky while UI tickets are blocked anyway. Re-add it ONLY with a
# network-independent delivery: vendor the elm linux-x64 binary in-repo (or an
# in-cluster mirror) and a pre-populated ELM_HOME, so the build needs no external
# egress. The Elm validation step tolerates a missing `elm` —
# it errors that gate, which only fires on web/elm changes.
COPY --from=build /out/agent-runner /usr/local/bin/agent-runner
COPY --from=claude --chmod=0555 /out/claude /usr/local/bin/claude
# The deterministic version-3 task functions (agent/functions) ship in the same
# image: they run as ordinary task steps beside agent steps, and share the git
# and Go toolchain already installed above.
COPY --from=build /out/function-runner /usr/local/bin/function-runner
COPY --from=build --chmod=0555 /out/agent-output /usr/local/bin/agent-output
COPY --chmod=0555 deploy/agent-runner/smoke.sh /usr/local/bin/agent-runner-image-smoke
COPY --from=build /usr/local/go /usr/local/go
# Root, like every other step image: jetbridge hostPath step volumes are
# kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a
# non-root user gets EACCES writing the flight recorder. IS_SANDBOX=1 lets
# the claude CLI accept --dangerously-skip-permissions when running as root
# (single-tenant, pod-isolated sandbox).
ENV IS_SANDBOX=1
RUN /usr/local/bin/agent-runner-image-smoke
ENV PATH="/usr/local/go/bin:${PATH}"
ENTRYPOINT ["agent-runner"]
