// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func forwarderFor(t *testing.T, srv *httptest.Server) (*Forwarder, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	f := NewForwarder(ForwarderConfig{
		ControlURL: srv.URL, Token: "mbn_x", NodeSlug: "edge-1",
		RedisAddr: mr.Addr(), Stream: "goma:analytics", BatchSize: 10,
	})
	if f == nil {
		t.Fatal("forwarder not built")
	}
	t.Cleanup(func() { _ = f.rdb.Close() })
	if err := f.ensureGroup(context.Background()); err != nil {
		t.Fatalf("ensureGroup: %v", err)
	}
	return f, mr
}

func push(t *testing.T, f *Forwarder, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := f.rdb.XAdd(context.Background(), &redis.XAddArgs{
			Stream: f.cfg.Stream,
			Values: map[string]any{streamEventField: `{"ts":1,"name":"mb-ws1-web","status":200}`},
		}).Err(); err != nil {
			t.Fatalf("XAdd: %v", err)
		}
	}
}

func pending(t *testing.T, f *Forwarder) int64 {
	t.Helper()
	p, err := f.rdb.XPending(context.Background(), f.cfg.Stream, forwarderGroup).Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	return p.Count
}

func TestForwarderPostsAndAcks(t *testing.T) {
	var got struct {
		BatchID string            `json:"batch_id"`
		Events  []json.RawMessage `json:"events"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/provider/edge-1/analytics" {
			t.Errorf("posted to %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer mbn_x" {
			t.Errorf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"accepted":3}}`))
	}))
	defer srv.Close()

	f, _ := forwarderFor(t, srv)
	push(t, f, 3)

	n, err := f.drainOnce(context.Background())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if n != 3 || len(got.Events) != 3 || got.BatchID == "" {
		t.Fatalf("moved %d events, batch=%+v", n, got)
	}
	if p := pending(t, f); p != 0 {
		t.Errorf("%d entries still pending after a 2xx", p)
	}
}

// The ACK must wait for the manager: a lost reply costs a replay, which the
// manager deduplicates, but ACKing early would lose the events outright.
func TestForwarderKeepsEventsWhenTheManagerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	f, _ := forwarderFor(t, srv)
	push(t, f, 2)

	if _, err := f.drainOnce(context.Background()); err == nil {
		t.Fatal("expected an error when the manager is down")
	}
	if p := pending(t, f); p != 2 {
		t.Errorf("pending = %d, want the 2 unacked events kept for retry", p)
	}
}

// A batch the manager will never accept has to be dropped, or it blocks every
// event behind it forever.
func TestForwarderDropsPermanentlyRejectedBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"BAD_REQUEST"}}`))
	}))
	defer srv.Close()

	f, _ := forwarderFor(t, srv)
	push(t, f, 2)

	n, err := f.drainOnce(context.Background())
	if err != nil {
		t.Fatalf("a permanent rejection must not surface as a retryable error: %v", err)
	}
	if n != 2 {
		t.Errorf("moved %d, want 2 dropped", n)
	}
	if p := pending(t, f); p != 0 {
		t.Errorf("%d entries still pending after a permanent rejection", p)
	}
}

// 429 is the manager asking to slow down, not a bad batch.
func TestForwarderRetriesOnRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f, _ := forwarderFor(t, srv)
	f.cfg.Interval = time.Millisecond
	push(t, f, 1)

	if _, err := f.drainOnce(context.Background()); err == nil {
		t.Fatal("a rate limit must be retried, not dropped")
	}
	if p := pending(t, f); p != 1 {
		t.Errorf("pending = %d, want the event kept", p)
	}
}

// An unreadable entry still has to be ACKed, or it stalls the stream.
func TestForwarderAcksUnparseableEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("nothing parseable should have been posted")
	}))
	defer srv.Close()

	f, _ := forwarderFor(t, srv)
	if err := f.rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: f.cfg.Stream, Values: map[string]any{"junk": "1"},
	}).Err(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	if _, err := f.drainOnce(context.Background()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if p := pending(t, f); p != 0 {
		t.Errorf("%d unreadable entries left pending", p)
	}
}

func TestNewForwarderNeedsFullConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  ForwarderConfig
	}{
		{"no redis", ForwarderConfig{Stream: "s", NodeSlug: "n"}},
		{"no stream", ForwarderConfig{RedisAddr: "a", NodeSlug: "n"}},
		{"no slug", ForwarderConfig{RedisAddr: "a", Stream: "s"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if NewForwarder(tt.cfg) != nil {
				t.Error("forwarder built from incomplete config")
			}
		})
	}
}

func TestEventPayload(t *testing.T) {
	// "e" is what Goma actually writes; the others are tolerated aliases.
	for _, key := range []string{"e", "event", "data"} {
		if raw, ok := eventPayload(map[string]any{key: `{"a":1}`}); !ok || string(raw) != `{"a":1}` {
			t.Errorf("%s: got %q ok=%v", key, raw, ok)
		}
	}
	if _, ok := eventPayload(map[string]any{"other": "x"}); ok {
		t.Error("unknown field reported as an event")
	}
	if _, ok := eventPayload(map[string]any{streamEventField: ""}); ok {
		t.Error("empty payload reported as an event")
	}
}
