package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/florianl/go-nfqueue"
)

const apiSocketPath = "/run/udp-redir.sock"

// pendingEntry is the wire format returned by /pending. Matches the
// shape that proxy/claude_proxy/addon.py emits, plus an extra
// "dns_name" field that the dashboard can surface.
type pendingEntry struct {
	FlowID    string `json:"flow_id"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	DNSName   string `json:"dns_name,omitempty"`
	Remaining int    `json:"remaining"` // seconds — placeholder (full TTL since we don't track per-flow time here)
}

type resolveReq struct {
	FlowID  string `json:"flow_id"`
	Action  string `json:"action"`            // "allow" | "deny"
	Pattern string `json:"pattern,omitempty"` // ignored — daemon doesn't add rules
}

type resolveResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// flowID derives a stable id for a flowKey so the dashboard can roundtrip
// it through /resolve. SHA-256 of the canonical "ip:port:dns" string.
func flowID(k flowKey) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", k.DstIP, k.DstPort, k.DNSName)))
	return "udp-" + hex.EncodeToString(h[:6])
}

func (s *state) handlePending(w http.ResponseWriter, _ *http.Request) {
	keys := s.held.List()
	out := make([]pendingEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, pendingEntry{
			FlowID:    flowID(k),
			Kind:      "udp",
			URL:       fmt.Sprintf("udp://%s:%d", k.DstIP, k.DstPort),
			Host:      k.DstIP,
			Port:      int(k.DstPort),
			DNSName:   k.DNSName,
			Remaining: int(defaultTTL.Seconds()),
		})
	}
	writeJSON(w, 200, out)
}

func (s *state) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, resolveResp{Error: "POST only"})
		return
	}
	var req resolveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, resolveResp{Error: "bad json: " + err.Error()})
		return
	}
	if req.Action != "allow" && req.Action != "deny" {
		writeJSON(w, 400, resolveResp{Error: "action must be allow or deny"})
		return
	}
	// Find the matching key by id.
	var match flowKey
	found := false
	for _, k := range s.held.List() {
		if flowID(k) == req.FlowID {
			match = k
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, resolveResp{Error: "flow not held"})
		return
	}
	ids := s.held.Drain(match)
	verdict := nfqueue.NfDrop
	if req.Action == "allow" {
		verdict = nfqueue.NfAccept
	}
	for _, id := range ids {
		_ = s.nf.SetVerdict(id, verdict)
	}
	writeJSON(w, 200, resolveResp{OK: true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// startAPI listens on the Unix socket, chowns it to PROXY_UID/PROXY_GID,
// chmods to 0600, and serves /pending + /resolve.
func startAPI(s *state) error {
	_ = os.Remove(apiSocketPath)
	l, err := net.Listen("unix", apiSocketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", apiSocketPath, err)
	}
	if uid := os.Getenv("PROXY_UID"); uid != "" {
		u, err1 := strconv.Atoi(uid)
		gid := os.Getenv("PROXY_GID")
		g, err2 := strconv.Atoi(gid)
		if err1 == nil && err2 == nil {
			if err := os.Chown(apiSocketPath, u, g); err != nil {
				return fmt.Errorf("chown socket: %w", err)
			}
		}
	}
	if err := os.Chmod(apiSocketPath, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pending", s.handlePending)
	mux.HandleFunc("/resolve", s.handleResolve)
	go func() {
		log.Printf("udp-redir: API listening on %s", apiSocketPath)
		if err := http.Serve(l, mux); err != nil {
			log.Printf("udp-redir: API serve: %v", err)
		}
	}()
	return nil
}
