// publish-mgr listens on a Unix socket inside the proxy container and
// owns nft INPUT rules for dynamically-published ports. The dashboard
// posts publish/unpublish actions; this daemon translates them into
// nftables rules and rule-store updates.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

const socketPath = "/run/publish-mgr.sock"

type publishReq struct {
	Protocol      string `json:"protocol"`                // "tcp" | "udp"
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"` // 0 = auto-assign
	Label         string `json:"label"`
}

type publishResp struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
}

type unpublishReq struct {
	HostPort int    `json:"host_port"`
	Protocol string `json:"protocol"`
}

type listEntry struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	Label         string `json:"label"`
}

type manager struct {
	mu        sync.Mutex
	rangeLo   int
	rangeHi   int
	published map[string]listEntry // key: "<proto>/<host_port>"
}

func main() {
	rng := os.Getenv("PROXY_PUBLISH_RANGE")
	lo, hi, err := parseRange(rng)
	if err != nil {
		log.Fatalf("publish-mgr: PROXY_PUBLISH_RANGE invalid %q: %v", rng, err)
	}
	mgr := &manager{
		rangeLo:   lo,
		rangeHi:   hi,
		published: make(map[string]listEntry),
	}

	_ = os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("publish-mgr: listen %s: %v", socketPath, err)
	}
	defer l.Close()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		log.Fatalf("publish-mgr: chmod socket: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", mgr.handlePublish)
	mux.HandleFunc("/unpublish", mgr.handleUnpublish)
	mux.HandleFunc("/list", mgr.handleList)
	log.Printf("publish-mgr listening on %s (range %d-%d)",
		socketPath, lo, hi)
	if err := http.Serve(l, mux); err != nil {
		log.Fatalf("publish-mgr: serve: %v", err)
	}
}

func parseRange(s string) (int, int, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want 'LO-HI', got %q", s)
	}
	lo, err1 := strconv.Atoi(parts[0])
	hi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || lo > hi {
		return 0, 0, fmt.Errorf("invalid range %q", s)
	}
	return lo, hi, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *manager) handleList(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]listEntry, 0, len(m.published))
	for _, e := range m.published {
		out = append(out, e)
	}
	writeJSON(w, 200, out)
}

func (m *manager) handlePublish(w http.ResponseWriter, r *http.Request) {
	// Task 15 implements this.
	writeJSON(w, 501, publishResp{Error: "not implemented yet"})
}

func (m *manager) handleUnpublish(w http.ResponseWriter, r *http.Request) {
	// Task 16 implements this.
	writeJSON(w, 501, publishResp{Error: "not implemented yet"})
}
