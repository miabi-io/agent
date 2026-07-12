# Miabi Agent

The node-side runtime for Miabi **multi-node**. It runs on each worker node,
dials the Miabi control plane over an **outbound** WebSocket, and exposes the
node's local Docker engine through a multiplexed tunnel.

All orchestration logic lives on the control plane — the agent is a thin, dumb
Docker proxy. It needs **no** database or Redis access, only:

- outbound HTTPS/WSS to the control plane (NAT/firewall friendly), and
- the local Docker socket.

## How it works

```
control plane (Miabi)  ──WS+yamux──►  agent  ──►  /var/run/docker.sock
        (opens a stream per Docker request)        (pipes each stream to Docker)
```

The control plane drives the node's Docker via the standard Docker API over the
tunnel; the agent just copies bytes between each tunnel stream and the socket.

## Configuration (environment)

| Variable                | Required | Default                        | Description |
|-------------------------|----------|--------------------------------|-------------|
| `MIABI_CONTROL_URL` | yes      | —                              | Control plane base URL, e.g. `https://miabi.example.com` |
| `MIABI_NODE_TOKEN`  | yes      | —                              | Join token shown once when the node was added (`mbn_…`) |
| `DOCKER_HOST`           | no       | `unix:///var/run/docker.sock`  | Local Docker endpoint |

The control plane reconnect is automatic with exponential backoff.

## Run

### Install script

Checks that Docker is present and running (it does not install Docker), then starts the
agent and verifies it stayed up:

```sh
curl -fsSL https://get.miabi.io/agent | \
  MIABI_CONTROL_URL=https://miabi.example.com MIABI_NODE_TOKEN=mbn_xxxxxxxx bash
```

### Docker

```sh
docker run -d --name miabi-agent --restart unless-stopped \
  -e MIABI_CONTROL_URL=https://miabi.example.com \
  -e MIABI_NODE_TOKEN=mbn_xxxxxxxx \
  -v /var/run/docker.sock:/var/run/docker.sock \
  miabi/agent:latest
```

### Binary

Via environment, or the equivalent flags (`--control-url`, `--token`, `--insecure`) — each flag
defaults to its env var and wins when both are set:

```sh
MIABI_CONTROL_URL=https://miabi.example.com \
MIABI_NODE_TOKEN=mbn_xxxxxxxx \
./miabi-agent

# or
./miabi-agent --control-url https://miabi.example.com --token mbn_xxxxxxxx
```

## Build

```sh
go build -ldflags "-X main.version=$(git describe --tags --always)" -o miabi-agent .
# or
docker build --build-arg VERSION=$(git describe --tags --always) -t ghcr.io/miabi-io/agent .
```

## Security

The agent grants the control plane access to the node's Docker socket — treat the
node as fully managed by the control plane. The join token authenticates the
tunnel and is stored **hashed** on the control plane; rotate it from the node's
page if leaked.

## License

AGPL-3.0-or-later — see [LICENSE](./LICENSE). A commercial license is available
for uses that don't fit the AGPL; see the [Miabi](https://github.com/miabi-io/miabi)
project's licensing.
