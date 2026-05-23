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
	"os/exec"
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

func (m *manager) nextFreePort() (int, bool) {
	for p := m.rangeLo; p <= m.rangeHi; p++ {
		used := false
		for _, e := range m.published {
			if e.HostPort == p {
				used = true
				break
			}
		}
		if !used {
			return p, true
		}
	}
	return 0, false
}

func nftAddInputAccept(proto string, port int) error {
	cmd := exec.Command("nft", "add", "rule", "inet", "claude_proxy_fw",
		"input", proto, "dport", strconv.Itoa(port), "accept")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft add rule failed: %v: %s", err, out)
	}
	return nil
}

func (m *manager) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, publishResp{Error: "POST only"})
		return
	}
	var req publishReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, publishResp{Error: "bad json: " + err.Error()})
		return
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		writeJSON(w, 400, publishResp{Error: "protocol must be tcp or udp"})
		return
	}
	if req.ContainerPort < 1024 || req.ContainerPort > 65535 {
		writeJSON(w, 400, publishResp{Error: "container_port must be 1024-65535"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	hp := req.HostPort
	if hp == 0 {
		var ok bool
		hp, ok = m.nextFreePort()
		if !ok {
			writeJSON(w, 409, publishResp{Error: "no free port in range"})
			return
		}
	} else {
		if hp < m.rangeLo || hp > m.rangeHi {
			writeJSON(w, 400, publishResp{Error: "host_port outside session range"})
			return
		}
		key := fmt.Sprintf("%s/%d", req.Protocol, hp)
		if _, exists := m.published[key]; exists {
			writeJSON(w, 409, publishResp{Error: "host_port already published"})
			return
		}
	}

	// Add the firewall rule for the CONTAINER side (apps listen on that
	// port inside the netns; docker's portmap forwards host→container).
	if err := nftAddInputAccept(req.Protocol, req.ContainerPort); err != nil {
		writeJSON(w, 500, publishResp{Error: err.Error()})
		return
	}

	key := fmt.Sprintf("%s/%d", req.Protocol, hp)
	m.published[key] = listEntry{
		HostPort:      hp,
		ContainerPort: req.ContainerPort,
		Protocol:      req.Protocol,
		Label:         req.Label,
	}
	writeJSON(w, 200, publishResp{
		HostPort:      hp,
		ContainerPort: req.ContainerPort,
		Protocol:      req.Protocol,
		OK:            true,
	})
}

func nftDelInputAccept(proto string, port int) error {
	// nft delete by handle is the clean path; first list to find the
	// handle for our rule, then delete by it.
	out, err := exec.Command("nft", "-a", "list", "chain", "inet",
		"claude_proxy_fw", "input").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft list: %v: %s", err, out)
	}
	// Anchor the needle: nft prints "<proto> dport <port> accept # handle N".
	// Requiring " accept # handle " right after our rule body prevents
	// false-positive matches against rules like "accept counter" or rules
	// that just happen to mention the same port elsewhere.
	needle := fmt.Sprintf("%s dport %d accept # handle ", proto, port)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		i := strings.LastIndex(line, "handle ")
		if i < 0 {
			continue
		}
		h := strings.TrimSpace(line[i+len("handle "):])
		delCmd := exec.Command("nft", "delete", "rule", "inet",
			"claude_proxy_fw", "input", "handle", h)
		if err := delCmd.Run(); err != nil {
			return fmt.Errorf("nft delete handle %s: %w", h, err)
		}
		return nil
	}
	return fmt.Errorf("no matching nft rule for %s/%d", proto, port)
}

func (m *manager) handleUnpublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, publishResp{Error: "POST only"})
		return
	}
	var req unpublishReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, publishResp{Error: "bad json: " + err.Error()})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s/%d", req.Protocol, req.HostPort)
	entry, ok := m.published[key]
	if !ok {
		writeJSON(w, 404, publishResp{Error: "not published"})
		return
	}
	if err := nftDelInputAccept(req.Protocol, entry.ContainerPort); err != nil {
		writeJSON(w, 500, publishResp{Error: err.Error()})
		return
	}
	delete(m.published, key)
	writeJSON(w, 200, publishResp{OK: true})
}
