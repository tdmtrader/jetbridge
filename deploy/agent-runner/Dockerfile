FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/agent-runner ./cmd/agent-runner
RUN CGO_ENABLED=0 go build -o /out/harvest-runner ./cmd/harvest-runner

FROM node:20-bookworm-slim
# Pin to the claude-code version the live theborg review job currently runs
# (check a recent agent-review build log before bumping).
RUN npm install -g @anthropic-ai/claude-code@2.0.1 \
    && apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
# Elm 0.19.1 toolchain for the WF-2 elm-build harvest gate (`elm make
# --optimize` on web/elm). The gate only shells out to `elm`; the committed
# bundle is diff-checked, so uglify-js is intentionally NOT installed here.
# elm ships as a single gzipped linux-x64 binary (~6 MiB gz, ~30 MiB on disk).
ENV ELM_HOME=/usr/local/share/elm
RUN curl -fsSL -o /tmp/elm.gz \
      https://github.com/elm/compiler/releases/download/0.19.1/binary-for-linux-64-bit.gz \
    && gunzip /tmp/elm.gz \
    && chmod +x /tmp/elm \
    && mv /tmp/elm /usr/local/bin/elm \
    && elm --version

# Pre-warm ELM_HOME so in-pod `elm make` needs no network: build web/elm once
# at image-build time (this also fails the build loudly if elm can't resolve
# the repo's dependencies). Only web/elm is copied in — the source tree is not
# otherwise part of the runtime image.
COPY web/elm /tmp/elm-prewarm/web/elm
RUN cd /tmp/elm-prewarm/web/elm \
    && elm make --optimize --output /dev/null src/Main.elm \
    && rm -rf /tmp/elm-prewarm
COPY --from=build /out/agent-runner /usr/local/bin/agent-runner
# harvest-runner shares the image (§2.8.1: the harvest step's main
# container is the agent-step image; git already present above).
COPY --from=build /out/harvest-runner /usr/local/bin/harvest-runner
COPY --from=build /usr/local/go /usr/local/go
# Root, like every other step image: jetbridge hostPath step volumes are
# kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a
# non-root user gets EACCES writing the flight recorder. IS_SANDBOX=1 lets
# the claude CLI accept --dangerously-skip-permissions when running as root
# (single-tenant, pod-isolated sandbox).
ENV IS_SANDBOX=1
ENV PATH="/usr/local/go/bin:${PATH}"
ENTRYPOINT ["agent-runner"]
