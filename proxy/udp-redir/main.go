// udp-redir listens on NFQUEUE 0, parses outbound UDP packets emitted
// from the Claude container's processes, consults the proxy rule store,
// and verdicts each packet ACCEPT / DROP / hold-for-resolve.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/florianl/go-nfqueue"
)

const (
	queueNum   = 0
	maxPktSize = 0xffff
	defaultTTL = 30 * time.Second
	defaultMax = 16
)

// state is the live verdict-engine state shared between the queue
// callback and the (later) Unix-socket API. Rules are reloaded on file
// mtime change; held holds packets pending a resolve decision.
type state struct {
	rulesPath  string
	rulesMu    sync.RWMutex
	rules      []Rule
	rulesMtime time.Time

	held *HoldBuf
	nf   *nfqueue.Nfqueue // for issuing deferred verdicts from outside the queue callback
}

func newState(rulesPath string) *state {
	return &state{
		rulesPath: rulesPath,
		held:      newHoldBuf(defaultMax, defaultTTL),
	}
}

// reloadIfChanged re-reads the rules file if its mtime has advanced.
// Called from the queue callback so verdicts always reflect the latest
// dashboard edits without an explicit signal.
func (s *state) reloadIfChanged() {
	st, err := os.Stat(s.rulesPath)
	if err != nil {
		return
	}
	s.rulesMu.RLock()
	current := s.rulesMtime
	s.rulesMu.RUnlock()
	if !st.ModTime().After(current) {
		return
	}
	rs, err := loadRules(s.rulesPath)
	if err != nil {
		log.Printf("udp-redir: reload rules: %v", err)
		return
	}
	s.rulesMu.Lock()
	s.rules = rs
	s.rulesMtime = st.ModTime()
	s.rulesMu.Unlock()
	log.Printf("udp-redir: reloaded %d rules from %s", len(rs), s.rulesPath)
}

// verdict computes the action for a parsed UDP datagram against the
// current rule set. Returns "allow", "deny", or "" if no rule matches.
func (s *state) verdict(d *UDPDatagram, dnsName string) string {
	s.rulesMu.RLock()
	rs := s.rules
	s.rulesMu.RUnlock()
	return matchUDP(rs, d.DstIP.String(), d.DstPort, dnsName)
}

func main() {
	rulesPath := os.Getenv("PROXY_RULES_PATH")
	if rulesPath == "" {
		session := os.Getenv("PROXY_SESSION")
		if session == "" {
			session = "default"
		}
		rulesPath = filepath.Join("/config", "proxy-state", session, "rules.json")
	}
	st := newState(rulesPath)
	st.reloadIfChanged()

	cfg := &nfqueue.Config{
		NfQueue:      queueNum,
		MaxPacketLen: maxPktSize,
		MaxQueueLen:  1024,
		Copymode:     nfqueue.NfQnlCopyPacket,
		WriteTimeout: 200 * time.Millisecond,
	}
	nf, err := nfqueue.Open(cfg)
	if err != nil {
		log.Fatalf("udp-redir: open nfqueue: %v", err)
	}
	defer nf.Close()
	st.nf = nf

	if err := startAPI(st); err != nil {
		log.Fatalf("udp-redir: start API: %v", err)
	}

	var pktCount, allowCount, denyCount atomic.Uint64

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fn := func(a nfqueue.Attribute) int {
		pktCount.Add(1)
		st.reloadIfChanged()

		id := *a.PacketID
		if a.Payload == nil {
			_ = nf.SetVerdict(id, nfqueue.NfDrop)
			return 0
		}
		d, err := parseUDP4(*a.Payload)
		if err != nil {
			// Not UDP/IPv4 or malformed — drop to be safe. Logging
			// every packet would be noisy, so only log occasionally.
			if pktCount.Load()%100 == 1 {
				log.Printf("udp-redir: parse error: %v", err)
			}
			_ = nf.SetVerdict(id, nfqueue.NfDrop)
			return 0
		}
		dnsName := ""
		if d.DstPort == 53 {
			name, _, _ := parseDNSQuestion(d.Payload)
			dnsName = name
		}
		switch st.verdict(d, dnsName) {
		case "allow":
			allowCount.Add(1)
			_ = nf.SetVerdict(id, nfqueue.NfAccept)
		case "deny":
			denyCount.Add(1)
			_ = nf.SetVerdict(id, nfqueue.NfDrop)
		default:
			// No rule — hold the packet. The kernel keeps it queued
			// until we issue a verdict (in /resolve or via TTL).
			key := flowKey{
				DstIP:   d.DstIP.String(),
				DstPort: d.DstPort,
				DNSName: dnsName,
			}
			if dropped := st.held.Add(key, id); dropped != 0 {
				_ = nf.SetVerdict(dropped, nfqueue.NfDrop)
			}
		}
		return 0
	}

	errFn := func(e error) int {
		log.Printf("udp-redir: nfqueue error: %v", e)
		return 0
	}

	if err := nf.RegisterWithErrorFunc(ctx, fn, errFn); err != nil {
		log.Fatalf("udp-redir: register: %v", err)
	}

	log.Printf("udp-redir: listening on NFQUEUE %d (rules=%s)", queueNum, rulesPath)

	// Periodically log counters so a stuck daemon is visible.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				log.Printf("udp-redir: pkts=%d allow=%d deny=%d",
					pktCount.Load(), allowCount.Load(), denyCount.Load())
			}
		}
	}()

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, exp := range st.held.EvictExpired() {
					_ = nf.SetVerdict(exp.ID, nfqueue.NfDrop)
				}
			}
		}
	}()

	// Block forever — Register spawned a background reader goroutine.
	select {}
}

// ensure helper symbol — fmt used in some build configurations.
var _ = fmt.Sprintf
