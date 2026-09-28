package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Server is a node agent: signed endpoints only.
type Server struct {
	NodeName string
	APIKey   string
	Now      func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Handler returns the agent mux:
//
//	GET /health — signed health report
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if _, err := VerifyAuth(r, s.NodeName, s.APIKey, s.now()); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ReadHealth())
	})
	return mux
}

// Health is what the panel probes.
type Health struct {
	OK     bool    `json:"ok"`
	Uptime int64   `json:"uptime_s"` // agent process uptime
	Load1  float64 `json:"load1"`
	MemUse float64 `json:"mem_used_ratio"` // 0..1
}

var startedAt = time.Now()

// ReadHealth samples minimal host health from /proc (Linux).
func ReadHealth() Health {
	h := Health{OK: true, Uptime: int64(time.Since(startedAt).Seconds())}
	h.Load1 = readLoadAvg1()
	h.MemUse = readMemRatio()
	return h
}

// Degraded reports whether the health is unstable but reachable.
func Degraded(h Health) bool {
	// load > 4 per available CPU pair or >90% memory — good enough heuristics
	// for a 1-2 core vps, easy to tune later per node.
	return h.Load1 > 4 || h.MemUse > 0.9
}

func readLoadAvg1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	var l float64
	fmt.Sscanf(string(b), "%f", &l)
	return l
}

func readMemRatio() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, avail int64
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "MemTotal:") {
			fmt.Sscanf(ln, "MemTotal: %d", &total)
		}
		if strings.HasPrefix(ln, "MemAvailable:") {
			fmt.Sscanf(ln, "MemAvailable: %d", &avail)
		}
	}
	if total <= 0 {
		return 0
	}
	return float64(total-avail) / float64(total)
}
