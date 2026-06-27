/*
 * Copyright 2026 Jonas Kaninda
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/logger"
)

// Config configures the agent runtime.
type Config struct {
	ControlURL  string // e.g. https://panel.example.com
	Token       string // join token (mbn_...)
	DockerHost  string // e.g. unix:///var/run/docker.sock
	Insecure    bool   // skip TLS verification of the control plane
	Version     string // agent build version, reported to the control plane
	ContainerID string // the agent's own container id, reported so it is protected from removal
}

const connectPath = "/api/v1/agent/connect"

// Run connects to the control plane and serves Docker over the tunnel until ctx
// is cancelled, reconnecting with exponential backoff.
func Run(ctx context.Context, cfg Config) error {
	backoff := time.Second
	for {
		if err := serve(ctx, cfg); err != nil && ctx.Err() == nil {
			logger.Warn("agent disconnected", "error", err, "retry_in", backoff.String())
		} else if ctx.Err() == nil {
			backoff = time.Second // a clean session resets backoff
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// serve runs one connection lifecycle.
func serve(ctx context.Context, cfg Config) error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.Token)
	header.Set("X-Agent-Version", cfg.Version)
	if cfg.ContainerID != "" {
		header.Set("X-Agent-Container-ID", cfg.ContainerID)
	}

	dialer := *websocket.DefaultDialer
	if cfg.Insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	ws, _, err := dialer.DialContext(ctx, wsURL(cfg.ControlURL), header)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	sess, err := tunnelServer(ws)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	logger.Info("connected to control plane", "control_url", cfg.ControlURL)

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err
		}
		go pipeToDocker(stream, cfg.DockerHost)
	}
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

// wsURL converts the control-plane base URL to the agent WebSocket endpoint.
func wsURL(base string) string {
	base = strings.TrimRight(base, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + connectPath
}
