// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command miabi-agent is the node-side runtime for Miabi multi-node.
// It dials the control plane over an outbound WebSocket and pipes each tunnel
// stream to the local Docker socket. All orchestration logic stays on the
// control plane; the agent is a thin, dumb Docker proxy with no DB/Redis access.
//
// Configuration is read from flags, each defaulting to an environment variable so
// the container form (env) and the binary form (flags) are interchangeable; a
// flag wins when both are given.
//
//	--control-url  / MIABI_CONTROL_URL                 control plane base URL, e.g. https://miabi.example.com
//	                                                       (env falls back to MIABI_API_URL)
//	--token        / MIABI_NODE_TOKEN                  join token issued when the node was added (mbn_...)
//	--insecure     / MIABI_AGENT_INSECURE_SKIP_VERIFY  skip TLS verification of the control plane (default false)
//	                 DOCKER_HOST                       local Docker endpoint (default unix:///var/run/docker.sock)
package main

import (
	"context"
	"flag"
	"os/signal"
	"strings"
	"syscall"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
)

// version is set at build time: -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {

	controlURL := flag.String("control-url", goutils.Env("MIABI_CONTROL_URL", goutils.Env("MIABI_API_URL", "")), "control plane base URL, e.g. https://miabi.example.com (env MIABI_CONTROL_URL)")
	token := flag.String("token", goutils.Env("MIABI_NODE_TOKEN", ""), "node join token, mbn_... (env MIABI_NODE_TOKEN)")
	insecure := flag.Bool("insecure", goutils.EnvBool("MIABI_AGENT_INSECURE_SKIP_VERIFY", false), "skip TLS verification of the control plane — last resort; prefer --ca-cert (env MIABI_AGENT_INSECURE_SKIP_VERIFY)")
	caCert := flag.String("ca-cert", goutils.Env("MIABI_CA_CERT", ""), "PEM (or path to one) of the CA that signed the control plane's certificate; verification still happens, anchored on it (env MIABI_CA_CERT)")
	flag.Parse()

	dockerHost := goutils.Env("DOCKER_HOST", "unix:///var/run/docker.sock")
	if goutils.EnvBool("MIABI_DEV_MODE", false) {
		logger.New(logger.WithDebugLevel())
	} else {
		logger.New(logger.WithJSONFormat(), logger.WithInfoLevel())
	}

	cfg := Config{
		ControlURL:  strings.TrimRight(*controlURL, "/"),
		Token:       *token,
		DockerHost:  dockerHost,
		Insecure:    *insecure,
		CACert:      *caCert,
		Version:     version,
		ContainerID: selfContainerID(),
	}
	if cfg.ControlURL == "" || cfg.Token == "" {
		logger.Fatal("control plane URL and node token are required (--control-url/--token or MIABI_CONTROL_URL/MIABI_NODE_TOKEN)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("miabi-agent starting", "version", version, "control_url", cfg.ControlURL)
	if err := Run(ctx, cfg); err != nil && err != context.Canceled {
		logger.Fatal("agent error", "error", err)
	}
	logger.Info("agent stopped")
}
