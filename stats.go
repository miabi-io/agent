// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi/client"
	"github.com/miabi-io/agent/hoststats"
)

const (
	statsPath            = "/api/v1/agent/stats"
	defaultStatsInterval = 15 * time.Second
	// statsRecheckInterval is how long to wait after the control plane said no (an older control
	// plane without the endpoint, or a token it does not accept) before asking again.
	statsRecheckInterval = 10 * time.Minute
)

// statsPusher reports this host's CPU and memory to the control plane, so the node is measured
// without the control plane starting a helper container on it. /proc/stat and /proc/meminfo are
// not namespaced, so the agent's own /proc is the host's.
type statsPusher struct {
	cfg     Config
	api     *client.Client
	sampler *hoststats.Sampler
	id      Identity
	// state is the last failure logged, so a persistent failure is logged once, not every tick.
	state string
}

func statsInterval() time.Duration {
	v := goutils.Env("MIABI_AGENT_STATS_INTERVAL", "")
	if v == "" {
		return defaultStatsInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		logger.Warn("invalid MIABI_AGENT_STATS_INTERVAL, using the default", "value", v, "default", defaultStatsInterval)
		return defaultStatsInterval
	}
	return d
}

// startStatsPusher runs until ctx is cancelled. MIABI_AGENT_STATS_INTERVAL=0 turns it off.
func startStatsPusher(ctx context.Context, cfg Config) {
	interval := statsInterval()
	if interval <= 0 {
		logger.Info("host stats push disabled")
		return
	}
	p, err := newStatsPusher(cfg)
	if err != nil {
		logger.Warn("host stats push disabled: bad TLS config", "error", err)
		return
	}
	p.id = identity(ctx, cfg.DockerHost)
	p.run(ctx, interval)
}

func newStatsPusher(cfg Config) (*statsPusher, error) {
	tlsCfg, err := clientTLS(cfg)
	if err != nil {
		return nil, err
	}
	return &statsPusher{
		cfg: cfg,
		api: client.New(strings.TrimRight(cfg.ControlURL, "/"),
			client.WithHTTPClient(&http.Client{Timeout: 10 * time.Second,
				Transport: &http.Transport{TLSClientConfig: tlsCfg}}),
			client.WithBearerToken(cfg.Token),
			client.WithHeader("Accept", "application/json"),
			client.WithUserAgent("miabi-agent"),
		),
		sampler: &hoststats.Sampler{ProcPath: "/proc"},
	}, nil
}

func (p *statsPusher) run(ctx context.Context, interval time.Duration) {
	_, _ = p.sampler.Sample()
	wait := interval
	for {
		sleep(ctx, wait)
		if ctx.Err() != nil {
			return
		}
		wait = p.tick(ctx, interval)
	}
}

// tick takes one reading, pushes it and returns how long to wait before the next.
func (p *statsPusher) tick(ctx context.Context, interval time.Duration) time.Duration {
	r, err := p.sampler.Sample()
	if errors.Is(err, hoststats.ErrNoBaseline) {
		return interval
	}
	if err != nil {
		// A hidepid or PID-namespaced host: the control plane falls back to sampling by container.
		p.logState("proc", func() { logger.Warn("cannot read host stats from /proc", "error", err) })
		return interval
	}

	err = p.post(ctx, r)
	var he *client.HTTPError
	switch {
	case err == nil:
		p.state = ""
		return interval
	case errors.As(err, &he) && (he.StatusCode == http.StatusNotFound || he.StatusCode == http.StatusMethodNotAllowed):
		p.logState("unsupported", func() {
			logger.Debug("control plane does not accept host stats; it will sample this node itself")
		})
		return statsRecheckInterval
	case errors.As(err, &he) && (he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden):
		p.logState("unauthorized", func() { logger.Warn("host stats push was not authorized", "status", he.StatusCode) })
		// A swarm agent is identified by its node id, which Docker may not have reported at startup.
		if p.id.SwarmNodeID == "" {
			p.id = identity(ctx, p.cfg.DockerHost)
		}
		return statsRecheckInterval
	case errors.As(err, &he):
		p.logState("rejected", func() { logger.Warn("host stats push rejected", "status", he.StatusCode, "error", err) })
		return interval
	default:
		p.logState("unreachable", func() { logger.Debug("host stats push failed", "error", err) })
		return interval
	}
}

func (p *statsPusher) post(ctx context.Context, r hoststats.Report) error {
	req := p.api.Post(statsPath).WithContext(ctx).
		Header("X-Agent-Version", p.cfg.Version).
		JSONBody(r)
	if p.id.Hostname != "" {
		req = req.Header("X-Agent-Hostname", p.id.Hostname)
	}
	if p.id.SwarmNodeID != "" {
		req = req.Header("X-Agent-Swarm-Node-ID", p.id.SwarmNodeID)
	}
	resp, err := req.Do()
	if err != nil {
		return err
	}
	return resp.Error()
}

func (p *statsPusher) logState(state string, log func()) {
	if p.state != state {
		log()
	}
	p.state = state
}
