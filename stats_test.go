// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miabi-io/agent/hoststats"
)

func fakeProc(t *testing.T, dir, cpu string) {
	t.Helper()
	files := map[string]string{
		"stat":    cpu + "\n",
		"meminfo": "MemTotal:        8192000 kB\nMemAvailable:    2048000 kB\n",
		"loadavg": "0.42 0.30 0.20 1/234 5678\n",
		"uptime":  "12345.67 45678.90\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatsPusherPostsAReportWithItsIdentity(t *testing.T) {
	var got hoststats.Report
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != statsPath {
			http.NotFound(w, r)
			return
		}
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"data":{"accepted":true}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	p, err := newStatsPusher(Config{ControlURL: srv.URL, Token: "mbc_cluster", Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	p.sampler.ProcPath = dir
	p.id = Identity{Hostname: "worker-1", SwarmNodeID: "swarm-abc"}

	fakeProc(t, dir, "cpu  1000 0 0 1000 0 0 0 0 0 0")
	if wait := p.tick(context.Background(), time.Second); wait != time.Second || hdr != nil {
		t.Fatal("the first tick only takes a baseline and must not push")
	}
	fakeProc(t, dir, "cpu  1750 0 0 1250 0 0 0 0 0 0")
	if wait := p.tick(context.Background(), time.Second); wait != time.Second {
		t.Fatalf("wait = %v after a good push", wait)
	}
	if got.CPUPercent != 75 || got.MemTotalBytes != 8192000*1024 || got.Load1 != 0.42 {
		t.Fatalf("report = %+v", got)
	}
	if hdr.Get("Authorization") != "Bearer mbc_cluster" || hdr.Get("X-Agent-Swarm-Node-ID") != "swarm-abc" ||
		hdr.Get("X-Agent-Hostname") != "worker-1" || hdr.Get("X-Agent-Version") != "1.2.3" {
		t.Fatalf("headers = %v", hdr)
	}
}

// A new agent against an old control plane: back off to a slow recheck instead of retrying every tick.
func TestStatsPusherBacksOffWhenTheEndpointIsMissing(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
		}))
		dir := t.TempDir()
		p, err := newStatsPusher(Config{ControlURL: srv.URL, Token: "mbn_x"})
		if err != nil {
			t.Fatal(err)
		}
		p.sampler.ProcPath = dir
		p.id = Identity{SwarmNodeID: "known"}
		fakeProc(t, dir, "cpu  100 0 0 100 0 0 0 0 0 0")
		_ = p.tick(context.Background(), time.Second)
		fakeProc(t, dir, "cpu  200 0 0 200 0 0 0 0 0 0")
		if wait := p.tick(context.Background(), time.Second); wait != statsRecheckInterval {
			t.Errorf("status %d: wait = %v, want %v", status, wait, statsRecheckInterval)
		}
		if calls != 1 {
			t.Errorf("status %d: calls = %d, want 1", status, calls)
		}
		srv.Close()
	}
}

func TestStatsInterval(t *testing.T) {
	t.Setenv("MIABI_AGENT_STATS_INTERVAL", "")
	if statsInterval() != defaultStatsInterval {
		t.Fatal("default")
	}
	t.Setenv("MIABI_AGENT_STATS_INTERVAL", "0")
	if statsInterval() != 0 {
		t.Fatal("0 disables")
	}
	t.Setenv("MIABI_AGENT_STATS_INTERVAL", "soon")
	if statsInterval() != defaultStatsInterval {
		t.Fatal("garbage falls back to the default")
	}
}
