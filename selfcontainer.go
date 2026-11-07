// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// containerIDRe is a 64-hex Docker/containerd container identifier.
var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// Reported so the agent's own container is protected from the admin containers list; mirrors internal/selfcontainer.
func selfContainerID() string {
	if v := strings.TrimSpace(os.Getenv("MIABI_CONTAINER_ID")); v != "" {
		return v
	}
	if id := scanForID("/proc/self/mountinfo", "/containers/"); id != "" {
		return id
	}
	if id := scanForID("/proc/self/cgroup", ""); id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil {
		h = strings.TrimSpace(h)
		if len(h) == 12 && isHexID(h) {
			return h
		}
	}
	return ""
}

func scanForID(path, must string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if must != "" && !strings.Contains(line, must) {
			continue
		}
		if id := containerIDRe.FindString(line); id != "" {
			return id
		}
	}
	return ""
}

func isHexID(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
