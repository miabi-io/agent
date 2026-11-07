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

// MIABI_CA_CERT still verifies against the operator CA; --insecure verifies nothing and allows impersonation.

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

func clientTLS(cfg Config) (*tls.Config, error) {
	pem := caPEM(cfg.CACert)

	// Insecure wins if both are set, but warn that the supplied CA then does nothing.
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

	// Add to the system pool rather than replace it, so a public cert still verifies.
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

// MIABI_CA_CERT accepts inline PEM, base64 PEM (newlines don't survive env transports), or a file path.
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
	// Fall back to reading it as a path so a relative one still works.
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
