// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package hoststats

import "errors"

// Report carries no identity or timestamp: the control plane takes the node from the token and stamps receipt.
type Report struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	Load1         float64 `json:"load1"`
	UptimeSeconds uint64  `json:"uptime_s"`
}

// Stats converts the report into a snapshot.
func (r Report) Stats() Stats {
	pct := 0.0
	if r.MemTotalBytes > 0 {
		pct = float64(r.MemUsedBytes) / float64(r.MemTotalBytes) * 100
	}
	return Stats{CPUPercent: r.CPUPercent, MemTotalBytes: r.MemTotalBytes, MemUsedBytes: r.MemUsedBytes, MemPercent: pct}
}

// ErrNoBaseline is returned by Sampler.Sample until it has a previous CPU reading to measure against.
var ErrNoBaseline = errors.New("hoststats: no cpu baseline yet")

// Sampler averages CPU over the whole interval between calls, so bursty workloads are measured fairly.
type Sampler struct {
	ProcPath string
	prev     CPUTimes
	have     bool
}

// Sample returns ErrNoBaseline on the first call and after counters went backwards.
func (s *Sampler) Sample() (Report, error) {
	cur, err := ReadCPU(s.ProcPath)
	if err != nil {
		return Report{}, err
	}
	prev, had := s.prev, s.have
	s.prev, s.have = cur, true
	if !had || cur.Total <= prev.Total {
		return Report{}, ErrNoBaseline
	}
	total, avail, err := ReadMem(s.ProcPath)
	if err != nil {
		return Report{}, err
	}
	st := StatsFrom(prev, cur, total, avail)
	r := Report{CPUPercent: st.CPUPercent, MemTotalBytes: st.MemTotalBytes, MemUsedBytes: st.MemUsedBytes}
	// Extras: a host that hides them still reports CPU and memory.
	r.Load1, _ = ReadLoad1(s.ProcPath)
	r.UptimeSeconds, _ = ReadUptime(s.ProcPath)
	return r, nil
}
