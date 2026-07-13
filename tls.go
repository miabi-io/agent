// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jkaninda/logger"
)

// Miabi is self-hosted first: a control plane on a private LAN, a homelab, or an
// air-gapped network has no public DNS name for Let's Encrypt to validate, so a
// self-signed or private-CA certificate is not a mistake — it is the only option.
// An agent that refuses to talk to one simply cannot be used there.
//
// There are two ways to survive that, and they are not equivalent:
//
//	MIABI_CA_CERT  — trust THIS certificate authority. Verification still happens;
//	                 it is merely anchored on the operator's own CA. A man in the
//	                 middle cannot forge a certificate the CA did not sign.
//	--insecure     — trust ANY certificate. No verification at all. Anyone able to
//	                 intercept the connection can impersonate the control plane —
//	                 which drives Docker on every node.
//
// Prefer the first. The second stays only as the last resort for someone who cannot
// get their CA to the node.

// tlsDialer returns a WebSocket dialer configured for the agent's TLS posture, or
// nil when the platform defaults are already correct (a publicly-trusted cert).
func tlsDialer(cfg Config) *websocket.Dialer {
	tlsCfg, err := clientTLS(cfg)
	if err != nil {
		logger.Warn("TLS configuration was ignored", "error", err)
		return nil
	}
	if tlsCfg == nil {
		return nil
	}
	d := *websocket.DefaultDialer
	d.TLSClientConfig = tlsCfg
	return &d
}

// clientTLS builds the agent's TLS config. Returns nil (use the defaults) when the
// control plane presents a publicly-trusted certificate, which is the goal state.
func clientTLS(cfg Config) (*tls.Config, error) {
	pem := caPEM(cfg.CACert)

	// Skipping verification wins if both are set, because it is what the operator
	// explicitly asked for — but say plainly that the CA they supplied is then doing
	// nothing, rather than leaving them to believe they are protected by it.
	if cfg.Insecure {
		if pem != "" {
			logger.Warn("MIABI_CA_CERT is ignored because TLS verification is disabled: " +
				"the agent accepts ANY certificate. Drop --insecure to have the CA actually verify one.")
		}
		logger.Warn("TLS verification is DISABLED: the agent accepts any certificate for the control plane. " +
			"Anyone able to intercept this connection can impersonate a control plane that drives Docker on this node.")
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit, warned-about opt-in
	}
	if pem == "" {
		return nil, nil // publicly-trusted certificate: the system pool is right
	}

	// Add the operator's CA to the system pool rather than replacing it, so an agent
	// trusting a private CA does not lose the ability to verify a public one (a
	// control plane behind a real certificate today, a private one tomorrow).
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("MIABI_CA_CERT does not contain a valid PEM certificate")
	}
	logger.Info("trusting a custom certificate authority for the control plane")
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// caPEM resolves the CA material from MIABI_CA_CERT, which accepts three forms so an
// operator can use whichever their environment makes easy:
//
//	PEM        — the certificate inline.
//	base64     — the same PEM, base64-encoded. This is what the control plane sends,
//	             because a certificate is multi-line and an environment variable is a
//	             poor place for newlines: they survive some transports and not others,
//	             and a PEM that arrives with its line breaks eaten is not a PEM at all.
//	             One flat token has no such failure mode.
//	a file path — a CA already on the node (its system trust anchor, bind-mounted in).
//	             Usually the best option: the host already trusts it, and it stays
//	             correct when the CA is rotated there.
func caPEM(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return ""
	case strings.Contains(v, "BEGIN CERTIFICATE"):
		return v
	case strings.HasPrefix(v, "/"):
		return readCAFile(v)
	}
	// Not obviously PEM and not an absolute path: try base64, and fall back to reading
	// it as a path so a relative one still works.
	if decoded, err := base64.StdEncoding.DecodeString(v); err == nil &&
		strings.Contains(string(decoded), "BEGIN CERTIFICATE") {
		return string(decoded)
	}
	return readCAFile(v)
}

func readCAFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		logger.Warn("could not read the CA certificate file", "path", path, "error", err)
		return ""
	}
	return string(b)
}
