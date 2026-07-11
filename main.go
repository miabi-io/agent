// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command miabi-agent is the node-side runtime for Miabi multi-node.
// It dials the control plane over an outbound WebSocket and pipes each tunnel
// stream to the local Docker socket. All orchestration logic stays on the
// control plane; the agent is a thin, dumb Docker proxy with no DB/Redis access.
//
// Configuration (environment):
//
//	MIABI_CONTROL_URL                 control plane base URL, e.g. https://panel.example.com
//	                                      (falls back to MIABI_API_URL)
//	MIABI_NODE_TOKEN                  join token issued when the node was added (mbn_...)
//	DOCKER_HOST                           local Docker endpoint (default unix:///var/run/docker.sock)
//	MIABI_AGENT_INSECURE_SKIP_VERIFY  skip TLS verification of the control plane (default false)
package main

import (
	"context"
	"os/signal"
	"strings"
	"syscall"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
)

// version is set at build time: -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {
	controlURL := strings.TrimRight(goutils.Env("MIABI_CONTROL_URL", goutils.Env("MIABI_API_URL", "")), "/")
	token := goutils.Env("MIABI_NODE_TOKEN", "")
	dockerHost := goutils.Env("DOCKER_HOST", "unix:///var/run/docker.sock")
	insecure := goutils.EnvBool("MIABI_AGENT_INSECURE_SKIP_VERIFY", false)
	if goutils.EnvBool("MIABI_DEV_MODE", false) {
		logger.New(logger.WithDebugLevel())
	} else {
		logger.New(logger.WithJSONFormat(), logger.WithInfoLevel())
	}

	cfg := Config{
		ControlURL:  controlURL,
		Token:       token,
		DockerHost:  dockerHost,
		Insecure:    insecure,
		Version:     version,
		ContainerID: selfContainerID(),
	}
	if cfg.ControlURL == "" || cfg.Token == "" {
		logger.Fatal("MIABI_CONTROL_URL and MIABI_NODE_TOKEN are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("miabi-agent starting", "version", version, "control_url", cfg.ControlURL)
	if err := Run(ctx, cfg); err != nil && err != context.Canceled {
		logger.Fatal("agent error", "error", err)
	}
	logger.Info("agent stopped")
}
