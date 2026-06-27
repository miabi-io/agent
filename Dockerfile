# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /miabi-agent .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 agent
COPY --from=build /miabi-agent /usr/local/bin/miabi-agent
# Note: reaching /var/run/docker.sock typically requires the host's docker group
# (or running as root). Mount the socket at runtime.
ENTRYPOINT ["miabi-agent"]
