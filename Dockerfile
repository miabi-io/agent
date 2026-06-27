# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /miabi-agent .

FROM alpine:3.20
# Re-declare in this stage so ${VERSION} is in scope for the label below.
ARG VERSION=dev
LABEL org.opencontainers.image.title="Miabi Agent" \
      org.opencontainers.image.description="Node-side runtime for Miabi multi-node: runs on each worker node and exposes its local Docker engine to the control plane over an outbound WebSocket tunnel." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.authors="Jonas Kaninda" \
      org.opencontainers.image.vendor="miabi-io" \
      org.opencontainers.image.url="https://github.com/miabi-io/agent" \
      org.opencontainers.image.source="https://github.com/miabi-io/agent" \
      org.opencontainers.image.documentation="https://github.com/miabi-io/agent#readme" \
      org.opencontainers.image.licenses="Apache-2.0"
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 agent
COPY --from=build /miabi-agent /usr/local/bin/miabi-agent
# Note: reaching /var/run/docker.sock typically requires the host's docker group
# (or running as root). Mount the socket at runtime.
ENTRYPOINT ["miabi-agent"]
