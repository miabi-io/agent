/*
 * Copyright 2026 Jonas Kaninda
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// containerIDRe is a 64-hex Docker/containerd container identifier.
var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// selfContainerID returns the ID of the container the agent runs in, reported to
// the control plane so the agent's own container is protected from being stopped
// or deleted via the admin containers list. It returns "" when not in a container.
//
// Resolution order, most to least reliable: an explicit MIABI_CONTAINER_ID
// override, the /var/lib/docker/containers/<id>/ bind-mount paths in
// /proc/self/mountinfo, the cgroup path in /proc/self/cgroup, then the hostname
// (Docker's default 12-char short ID). It mirrors the control plane's
// internal/selfcontainer package, which the agent module cannot import.
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
