// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi/client"
	"github.com/redis/go-redis/v9"
)

// ForwarderConfig configures the analytics forwarder.
type ForwarderConfig struct {
	ControlURL string
	Token      string
	NodeSlug   string
	RedisAddr  string
	RedisPass  string
	Stream     string
	Insecure   bool
	CACert     string
	BatchSize  int
	Interval   time.Duration
}

const (
	forwarderGroup     = "miabi-forwarder"
	forwarderConsumer  = "agent"
	defaultBatchSize   = 500
	defaultInterval    = 5 * time.Second
	forwarderBlock     = 5 * time.Second
	forwarderMaxErrors = 5
)

// Forwarder drains the node gateway's local analytics stream and posts batches to
// the control plane, which appends them to the platform stream.
type Forwarder struct {
	cfg ForwarderConfig
	rdb *redis.Client
	api *client.Client
}

// forwarderSettings is the control plane's answer to "should this node forward,
// and from where" — resolved from the agent's own token.
type forwarderSettings struct {
	Enabled       bool   `json:"enabled"`
	Node          string `json:"node"`
	Stream        string `json:"stream"`
	RedisAddr     string `json:"redis_addr"`
	RedisPassword string `json:"redis_password"`
}

// FetchForwarderConfig asks the control plane how this node should forward. Env
// overrides win, so an operator can still pin it by hand.
func FetchForwarderConfig(ctx context.Context, cfg Config) (ForwarderConfig, error) {
	tlsCfg, err := clientTLS(cfg)
	if err != nil {
		return ForwarderConfig{}, err
	}
	api := client.New(strings.TrimRight(cfg.ControlURL, "/"),
		client.WithHTTPClient(&http.Client{Timeout: 15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg}}),
		client.WithBearerToken(cfg.Token),
		client.WithHeader("Accept", "application/json"),
		client.WithUserAgent("miabi-agent"),
	)
	resp, err := api.Get("/api/v1/agent/analytics-config").WithContext(ctx).Do()
	if err != nil {
		return ForwarderConfig{}, err
	}
	// Without this a 401 would decode to an empty config and read as "forwarding
	// is off for this node" rather than as a failure to retry.
	if err := resp.Error(); err != nil {
		return ForwarderConfig{}, err
	}
	var env struct {
		Data forwarderSettings `json:"data"`
	}
	if err := resp.JSON(&env); err != nil {
		return ForwarderConfig{}, err
	}
	s := env.Data
	if !s.Enabled {
		return ForwarderConfig{}, nil
	}
	return ForwarderConfig{
		ControlURL: cfg.ControlURL, Token: cfg.Token, NodeSlug: s.Node,
		RedisAddr: s.RedisAddr, RedisPass: s.RedisPassword, Stream: s.Stream,
		Insecure: cfg.Insecure, CACert: cfg.CACert,
	}, nil
}

// NewForwarder builds a forwarder, or nil when the node is not configured for it.
func NewForwarder(cfg ForwarderConfig) *Forwarder {
	if cfg.RedisAddr == "" || cfg.Stream == "" || cfg.NodeSlug == "" {
		return nil
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	tlsCfg, err := clientTLS(Config{Insecure: cfg.Insecure, CACert: cfg.CACert})
	if err != nil {
		logger.Warn("analytics forwarder disabled: bad TLS config", "error", err)
		return nil
	}
	httpc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	return &Forwarder{
		cfg: cfg,
		rdb: redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPass}),
		api: client.New(strings.TrimRight(cfg.ControlURL, "/"),
			client.WithHTTPClient(httpc),
			client.WithBearerToken(cfg.Token),
			client.WithHeader("Accept", "application/json"),
			client.WithUserAgent("miabi-agent"),
			client.WithRetry(client.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 10 * time.Second}),
		),
	}
}

// Run drains until ctx is cancelled. Failures are logged and retried: events stay
// in the local stream, which is MAXLEN-capped, so an outage costs history, not disk.
func (f *Forwarder) Run(ctx context.Context) {
	if err := f.ensureGroup(ctx); err != nil {
		logger.Warn("analytics forwarder could not start", "error", err)
		return
	}
	logger.Info("analytics forwarder started", "stream", f.cfg.Stream, "node", f.cfg.NodeSlug)

	errs := 0
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := f.drainOnce(ctx)
		switch {
		case err != nil:
			errs++
			if errs%forwarderMaxErrors == 1 {
				logger.Warn("analytics forward failed", "error", err)
			}
			sleep(ctx, f.cfg.Interval)
		case n == 0:
			errs = 0
			sleep(ctx, f.cfg.Interval)
		default:
			errs = 0
		}
	}
}

func (f *Forwarder) ensureGroup(ctx context.Context) error {
	err := f.rdb.XGroupCreateMkStream(ctx, f.cfg.Stream, forwarderGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// drainOnce forwards at most one batch and returns how many events it moved.
func (f *Forwarder) drainOnce(ctx context.Context) (int, error) {
	res, err := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: forwarderGroup, Consumer: forwarderConsumer,
		Streams: []string{f.cfg.Stream, ">"},
		Count:   int64(f.cfg.BatchSize), Block: forwarderBlock,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	ids := make([]string, 0, f.cfg.BatchSize)
	events := make([]json.RawMessage, 0, f.cfg.BatchSize)
	for _, st := range res {
		for _, msg := range st.Messages {
			ids = append(ids, msg.ID)
			if raw, ok := eventPayload(msg.Values); ok {
				events = append(events, raw)
			}
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// Nothing parseable: ACK anyway, or the batch blocks the stream forever.
	if len(events) == 0 {
		return len(ids), f.ack(ctx, ids)
	}

	batchID, err := randomID()
	if err != nil {
		return 0, err
	}
	if err := f.post(ctx, batchID, events); err != nil {
		var he *client.HTTPError
		// A batch the manager will never accept must not be retried forever.
		if errors.As(err, &he) && he.StatusCode >= 400 && he.StatusCode < 500 && he.StatusCode != http.StatusTooManyRequests {
			logger.Warn("analytics batch rejected, dropping", "status", he.StatusCode, "events", len(events))
			return len(ids), f.ack(ctx, ids)
		}
		return 0, err
	}
	return len(ids), f.ack(ctx, ids)
}

// post sends one batch. ACK happens only after this returns nil, so a lost reply
// costs a replay — which the manager deduplicates on batch id.
func (f *Forwarder) post(ctx context.Context, batchID string, events []json.RawMessage) error {
	body := map[string]any{"batch_id": batchID, "events": events}
	resp, err := f.api.Post("/api/v1/provider/" + f.cfg.NodeSlug + "/analytics").
		WithContext(ctx).JSONBody(body).Do()
	if err != nil {
		return err
	}
	// Do reports transport failures only, so a non-2xx would otherwise read as
	// success and the batch would be ACKed away.
	return resp.Error()
}

func (f *Forwarder) ack(ctx context.Context, ids []string) error {
	return f.rdb.XAck(ctx, f.cfg.Stream, forwarderGroup, ids...).Err()
}

// streamEventField is the field Goma writes each event under, and the only one
// Miabi's consumer reads. Getting it wrong drops every event silently.
const streamEventField = "e"

// eventPayload pulls the JSON event out of a stream entry.
func eventPayload(values map[string]any) (json.RawMessage, bool) {
	for _, k := range []string{streamEventField, "event", "data"} {
		if v, ok := values[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return json.RawMessage(s), true
			}
		}
	}
	return nil, false
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// startForwarder asks the control plane whether this node forwards analytics and
// runs the forwarder if so. Env overrides let an operator pin it by hand. The
// control plane may not be up yet, so it retries rather than giving up at boot.
func startForwarder(ctx context.Context, cfg Config) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		fc, err := FetchForwarderConfig(ctx, cfg)
		if err != nil {
			if attempt == 0 {
				logger.Debug("analytics forwarder config not available yet", "error", err)
			}
			sleep(ctx, time.Minute)
			continue
		}
		applyForwarderEnv(&fc, cfg)
		fwd := NewForwarder(fc)
		if fwd == nil {
			return
		}
		fwd.Run(ctx)
		return
	}
}

// applyForwarderEnv lets the operator override any resolved setting.
func applyForwarderEnv(fc *ForwarderConfig, cfg Config) {
	if v := goutils.Env("MIABI_NODE_SLUG", ""); v != "" {
		fc.NodeSlug = v
	}
	if v := goutils.Env("MIABI_GATEWAY_REDIS_ADDR", ""); v != "" {
		fc.RedisAddr = v
	}
	if v := goutils.Env("GATEWAY_REDIS_PASSWORD", ""); v != "" {
		fc.RedisPass = v
	}
	if v := goutils.Env("MIABI_ANALYTICS_STREAM", ""); v != "" {
		fc.Stream = v
	}
	fc.ControlURL, fc.Token = cfg.ControlURL, cfg.Token
	fc.Insecure, fc.CACert = cfg.Insecure, cfg.CACert
}
