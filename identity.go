// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/jkaninda/logger"
)

type Identity struct {
	Hostname    string // the Docker host's name (not the agent container's)
	SwarmNodeID string // this engine's id within the swarm; empty when not a member
}

// identity queries the local Docker daemon's /info over the same socket the agent
// relays. It is deliberately hand-rolled rather than pulling in the Docker SDK: the
// agent is a byte pump with three dependencies, and one JSON field is not worth a
// module that would multiply its size.
//
// Best-effort by design. A failure here must never stop the agent connecting — it
// only means the control plane learns a little less about this node.
func identity(ctx context.Context, dockerHost string) Identity {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialDocker(dockerHost)
			},
		},
	}
	// The host in the URL is ignored — the dialer above decides where to connect —
	// but it must be present and valid.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", nil)
	if err != nil {
		return Identity{}
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("could not read the node's identity from Docker", "error", err)
		return Identity{}
	}
	defer func() { _ = resp.Body.Close() }()

	var info struct {
		Name  string `json:"Name"`
		Swarm struct {
			NodeID string `json:"NodeID"`
		} `json:"Swarm"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		logger.Warn("could not decode Docker info", "error", err)
		return Identity{}
	}
	return Identity{Hostname: info.Name, SwarmNodeID: info.Swarm.NodeID}
}
