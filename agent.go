// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/yamux"
	"github.com/jkaninda/logger"
	"github.com/miabi-io/wstunnel"
)

// Config configures the agent runtime.
type Config struct {
	ControlURL string // e.g. https://panel.example.com
	Token      string // join token (mbn_...)
	DockerHost string // e.g. unix:///var/run/docker.sock
	Insecure   bool   // skip TLS verification of the control plane (last resort)
	// CACert trusts a specific certificate authority for the control plane
	CACert      string
	Version     string // agent build version, reported to the control plane
	ContainerID string // the agent's own container id, reported so it is protected from removal
}

const connectPath = "/api/v1/agent/connect"

// Run connects to the control plane and serves Docker over the tunnel until ctx
// is cancelled, reconnecting with exponential backoff. The WebSocket + yamux
// transport (framing, keepalive, dial, reconnect loop) is the shared wstunnel
// module, so the agent and control plane always speak the same wire protocol.
func Run(ctx context.Context, cfg Config) error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.Token)
	header.Set("X-Agent-Version", cfg.Version)
	if cfg.ContainerID != "" {
		header.Set("X-Agent-Container-ID", cfg.ContainerID)
	}

	if id := identity(ctx, cfg.DockerHost); id.Hostname != "" || id.SwarmNodeID != "" {
		if id.Hostname != "" {
			header.Set("X-Agent-Hostname", id.Hostname)
		}
		if id.SwarmNodeID != "" {
			header.Set("X-Agent-Swarm-Node-ID", id.SwarmNodeID)
		}
		logger.Info("node identity", "hostname", id.Hostname, "swarm_node_id", id.SwarmNodeID)
	}
	opts := wstunnel.ClientOptions{
		URL:    wstunnel.URL(cfg.ControlURL, connectPath),
		Header: header,

		Dialer:   tlsDialer(cfg),
		Insecure: cfg.Insecure,
		OnConnect: func() {
			logger.Info("connected to control plane", "control_url", cfg.ControlURL)
		},
		OnError: func(err error) {
			logger.Warn("agent disconnected", "error", err)
		},
	}
	// Each accepted stream is one Docker API request the control plane opened;
	// pipe it to the local Docker daemon. The handler blocks for the session's
	// lifetime, so wstunnel.Serve reconnects when it returns.
	return wstunnel.Serve(ctx, opts, func(_ context.Context, sess *yamux.Session) error {
		for {
			stream, err := sess.AcceptStream()
			if err != nil {
				return err
			}
			go pipeToDocker(stream, cfg.DockerHost)
		}
	})
}

// pipeToDocker proxies one tunnel stream to the local Docker daemon.
func pipeToDocker(stream net.Conn, dockerHost string) {
	defer func() { _ = stream.Close() }()
	dconn, err := dialDocker(dockerHost)
	if err != nil {
		logger.Error("dial docker socket", "error", err)
		return
	}
	defer func() { _ = dconn.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dconn, stream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, dconn); done <- struct{}{} }()
	<-done
}

// dialDocker connects to the local Docker daemon described by DOCKER_HOST.
func dialDocker(host string) (net.Conn, error) {
	switch {
	case host == "" || strings.HasPrefix(host, "unix://"):
		path := strings.TrimPrefix(host, "unix://")
		if path == "" {
			path = "/var/run/docker.sock"
		}
		return net.Dial("unix", path)
	case strings.HasPrefix(host, "tcp://"):
		return net.Dial("tcp", strings.TrimPrefix(host, "tcp://"))
	default:
		return net.Dial("unix", host)
	}
}
