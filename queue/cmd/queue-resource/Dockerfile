# The queue resource type image. Build from the queue module dir:
#   docker build -f cmd/queue-resource/Dockerfile --build-arg GO_IMAGE=<go image> --build-arg BASE=<alpine image> \
#     --build-arg QUEUE_CONFIG=<path to the queue config, in the context> .
# Pin both images by digest at the build site; none is named here.
ARG GO_IMAGE
ARG BASE

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/queue-resource ./cmd/queue-resource

# HOOK: a later stage adds the compose-hook tools here, built FROM build or
# another pinned image, and copies them into the final image below.

FROM ${BASE}
RUN apk add --no-cache git openssh-client ca-certificates
# The queue config the pipeline names as source.config_file. QUEUE_CONFIG is
# its path in the build context (the module dir); CI passes the planned one.
ARG QUEUE_CONFIG=queue.yaml
COPY ${QUEUE_CONFIG} /etc/mq/queue.yaml
COPY --from=build /out/queue-resource /usr/local/bin/queue-resource
RUN mkdir -p /opt/resource \
 && ln -s /usr/local/bin/queue-resource /opt/resource/check \
 && ln -s /usr/local/bin/queue-resource /opt/resource/in \
 && ln -s /usr/local/bin/queue-resource /opt/resource/out
