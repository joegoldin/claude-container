# Phase 2 — UDP Outbound via NFQUEUE Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Intercept outbound UDP datagrams from the Claude container with a new `udp-redir` userspace daemon (NFQUEUE-driven), apply the unified rule store's verdicts (allow/deny/hold), expose DNS-aware UX in the dashboard, and replay held packets when the user approves.

**Architecture:** A small Go daemon listens on NFQUEUE 0. nftables enqueues every UDP packet from non-mitmproxy uids (skuid != 1500). The daemon parses the IP + UDP headers, parses the DNS question for queries to UDP/53, consults the proxy's `rules.json` (read from the same per-session state dir that mitmproxy uses, refreshed on mtime change), and issues a verdict per kernel packet-id. Hold semantics use the kernel queue's deferred-verdict capability — the packet sits in the queue (up to 16 per tuple, 30-second TTL) until the user resolves it via the dashboard, at which point the deferred verdict is issued. The dashboard's existing `/api/pending` and `/api/resolve` endpoints proxy to a new Unix socket exposed by the daemon.

**Tech Stack:** Go 1.22 (`github.com/florianl/go-nfqueue` + transitive netlink deps, vendored), Python Starlette/Uvicorn (dashboard, addon — already in place from Phase 0), nftables, nix dockerTools.

**Spec reference:** `docs/plans/2026-05-22-dashboard-all-traffic-design.md` §8.

**Out of scope (future work):** Phase 3 (nft user_allow sub-chain), Phase 4 (TCP UX polish), inbound UDP (already covered by Phase 1's publish-mgr).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `proxy/udp-redir/go.mod` | New Go module (separate from main repo + `publish-mgr`) |
| `proxy/udp-redir/main.go` | Daemon entrypoint: socket, queue loop wiring |
| `proxy/udp-redir/ipparse.go` | IPv4 + UDP header parser (pure functions) |
| `proxy/udp-redir/dnsparse.go` | RFC 1035 DNS question parser (pure functions) |
| `proxy/udp-redir/rules.go` | Rule file loader + matcher mirroring `RuleStore.match_request` |
| `proxy/udp-redir/holdbuf.go` | Per-tuple deferred-verdict buffer with TTL |
| `proxy/udp-redir/api.go` | Unix socket HTTP API (/pending, /resolve) |
| `proxy/udp-redir/*_test.go` | Unit tests for the pure-function packages |
| `proxy/udp-redir/go.sum` | Dependency hashes (nix verifies against `vendorHash`) |
| `nix/proxy-image.nix` | Build the binary, install it in the image, start it from the entrypoint, add the NFQUEUE nft rules |
| `proxy/claude_proxy/dashboard.py` | Merge udp-redir's pending list + forward UDP resolves |
| `cmd/security_e2e_test.go` | New E2E coverage for the daemon |
| `README.md` | UDP-outbound user-facing docs |

---

## Phase 2A — udp-redir daemon

### Task 1: Initialize Go module with `go-nfqueue` dependency

**Files:**
- Create: `proxy/udp-redir/go.mod`
- Create: `proxy/udp-redir/go.sum`
- Create: `proxy/udp-redir/main.go`

The module ships its `go.sum` but NOT a `vendor/` directory — nix's `buildGoModule` will fetch + verify the deps against `vendorHash` declared in `proxy-image.nix` (Task 9 sets that up).

- [ ] **Step 1: Create the module and add the dep**

```bash
mkdir -p proxy/udp-redir
cd proxy/udp-redir
devenv shell -- go mod init github.com/joegoldin/claude-container/proxy/udp-redir
devenv shell -- go get github.com/florianl/go-nfqueue
```

- [ ] **Step 2: Write a placeholder main.go so the module has a real package**

`proxy/udp-redir/main.go`:

```go
// udp-redir listens on NFQUEUE 0, parses outbound UDP packets emitted
// from the Claude container's processes, consults the proxy rule store,
// and verdicts each packet ACCEPT / DROP / hold-for-resolve.
package main

import (
	"log"

	_ "github.com/florianl/go-nfqueue"
)

func main() {
	log.Println("udp-redir: starting")
}
```

(The blank-import keeps `go mod tidy` from removing the dep before later tasks use it.)

- [ ] **Step 3: Set the go directive to 1.22 and tidy**

Edit `proxy/udp-redir/go.mod` so the `go` directive is `go 1.22` (matches `publish-mgr` and the nix `buildGoModule` toolchain).

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go mod tidy"
```

- [ ] **Step 4: Build to confirm**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go build ./..."
```

Expected: no output, no errors.

- [ ] **Step 5: Add the build output to .gitignore (uncommitted — leave for user to commit later)**

The same pattern as `publish-mgr`: append a line to `.gitignore` so a stray local build doesn't get committed.

```
proxy/udp-redir/udp-redir
```

Do NOT commit `.gitignore` in this task per the project convention; leave it as a working-tree change.

- [ ] **Step 6: Commit**

```bash
git add proxy/udp-redir/go.mod proxy/udp-redir/go.sum proxy/udp-redir/main.go
git commit -m "feat(udp-redir): bootstrap Go module with go-nfqueue dependency"
```

---

### Task 2: IPv4 + UDP header parser

**Files:**
- Create: `proxy/udp-redir/ipparse.go`
- Create: `proxy/udp-redir/ipparse_test.go`

- [ ] **Step 1: Write the failing test**

`proxy/udp-redir/ipparse_test.go`:

```go
package main

import (
	"net"
	"testing"
)

// Hand-built IPv4 packet: 20-byte IPv4 header + 8-byte UDP header + 4 bytes payload.
//
// IPv4: version=4, IHL=5, total length 32, proto=17 (UDP),
//       src 10.0.0.5, dst 8.8.8.8
// UDP : sport 5555, dport 53, length 12 (8 header + 4 payload), no csum
// Payload: "ping"
func sampleDNSish() []byte {
	pkt := []byte{
		0x45, 0x00, 0x00, 0x20, // ver/ihl, dscp, total length 32
		0x00, 0x00, 0x00, 0x00, // id, flags/frag
		0x40, 0x11, 0x00, 0x00, // ttl=64, proto=17 (UDP), header csum (ignored)
		10, 0, 0, 5, // src
		8, 8, 8, 8, // dst
		// UDP
		0x15, 0xb3, // sport 5555
		0x00, 0x35, // dport 53
		0x00, 0x0c, // length 12
		0x00, 0x00, // csum (ignored)
		'p', 'i', 'n', 'g',
	}
	return pkt
}

func TestParseUDP4_ExtractsTuple(t *testing.T) {
	got, err := parseUDP4(sampleDNSish())
	if err != nil {
		t.Fatalf("parseUDP4: %v", err)
	}
	if !got.SrcIP.Equal(net.IPv4(10, 0, 0, 5)) {
		t.Errorf("SrcIP=%v, want 10.0.0.5", got.SrcIP)
	}
	if !got.DstIP.Equal(net.IPv4(8, 8, 8, 8)) {
		t.Errorf("DstIP=%v, want 8.8.8.8", got.DstIP)
	}
	if got.SrcPort != 5555 {
		t.Errorf("SrcPort=%d, want 5555", got.SrcPort)
	}
	if got.DstPort != 53 {
		t.Errorf("DstPort=%d, want 53", got.DstPort)
	}
	if string(got.Payload) != "ping" {
		t.Errorf("Payload=%q, want ping", got.Payload)
	}
}

func TestParseUDP4_RejectsNonUDP(t *testing.T) {
	pkt := sampleDNSish()
	pkt[9] = 6 // proto = TCP
	if _, err := parseUDP4(pkt); err == nil {
		t.Errorf("expected error for non-UDP proto, got nil")
	}
}

func TestParseUDP4_RejectsShortPacket(t *testing.T) {
	if _, err := parseUDP4([]byte{0x45, 0, 0, 0}); err == nil {
		t.Errorf("expected error for short packet, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestParseUDP4 ./..."
```

Expected: FAIL — `parseUDP4` undefined.

- [ ] **Step 3: Write the parser**

`proxy/udp-redir/ipparse.go`:

```go
package main

import (
	"encoding/binary"
	"fmt"
	"net"
)

// UDPDatagram describes the 5-tuple and payload of a parsed IPv4 UDP packet.
type UDPDatagram struct {
	SrcIP   net.IP
	DstIP   net.IP
	SrcPort uint16
	DstPort uint16
	Payload []byte
}

// parseUDP4 parses an IPv4 packet carrying UDP, returning the 5-tuple
// and the UDP payload. Returns an error for non-UDP, IPv6, or truncated
// input. Header checksums are not verified (the kernel already did).
func parseUDP4(pkt []byte) (*UDPDatagram, error) {
	if len(pkt) < 20 {
		return nil, fmt.Errorf("packet too short for IPv4 header: %d bytes", len(pkt))
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || ihl > len(pkt) {
		return nil, fmt.Errorf("bad IHL %d in %d-byte packet", ihl, len(pkt))
	}
	if pkt[9] != 17 {
		return nil, fmt.Errorf("not UDP (proto=%d)", pkt[9])
	}
	if len(pkt) < ihl+8 {
		return nil, fmt.Errorf("packet too short for UDP header")
	}
	src := net.IPv4(pkt[12], pkt[13], pkt[14], pkt[15])
	dst := net.IPv4(pkt[16], pkt[17], pkt[18], pkt[19])
	udp := pkt[ihl:]
	sport := binary.BigEndian.Uint16(udp[0:2])
	dport := binary.BigEndian.Uint16(udp[2:4])
	udpLen := int(binary.BigEndian.Uint16(udp[4:6]))
	if udpLen < 8 || ihl+udpLen > len(pkt) {
		return nil, fmt.Errorf("bad UDP length %d", udpLen)
	}
	payload := udp[8:udpLen]
	return &UDPDatagram{
		SrcIP:   src,
		DstIP:   dst,
		SrcPort: sport,
		DstPort: dport,
		Payload: payload,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestParseUDP4 ./..."
```

Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add proxy/udp-redir/ipparse.go proxy/udp-redir/ipparse_test.go
git commit -m "feat(udp-redir): IPv4 + UDP header parser"
```

---

### Task 3: DNS RFC 1035 question parser

**Files:**
- Create: `proxy/udp-redir/dnsparse.go`
- Create: `proxy/udp-redir/dnsparse_test.go`

- [ ] **Step 1: Write the failing test**

`proxy/udp-redir/dnsparse_test.go`:

```go
package main

import "testing"

// A real DNS query for "example.com", QTYPE=A, QCLASS=IN.
// Captured from `dig example.com @1.1.1.1`.
func sampleDNSQuery() []byte {
	return []byte{
		0x12, 0x34, // ID
		0x01, 0x00, // flags: standard query, RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
		// QNAME: 7 "example" 3 "com" 0
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm',
		0x00,
		// QTYPE A, QCLASS IN
		0x00, 0x01,
		0x00, 0x01,
	}
}

func TestParseDNSQuestion_ReadsName(t *testing.T) {
	name, qtype, err := parseDNSQuestion(sampleDNSQuery())
	if err != nil {
		t.Fatalf("parseDNSQuestion: %v", err)
	}
	if name != "example.com" {
		t.Errorf("name=%q, want example.com", name)
	}
	if qtype != 1 {
		t.Errorf("qtype=%d, want 1 (A)", qtype)
	}
}

func TestParseDNSQuestion_RejectsShortPayload(t *testing.T) {
	if _, _, err := parseDNSQuestion([]byte{0x12, 0x34}); err == nil {
		t.Errorf("expected error for short payload, got nil")
	}
}

func TestParseDNSQuestion_RejectsZeroQuestions(t *testing.T) {
	pkt := sampleDNSQuery()
	pkt[4] = 0 // QDCOUNT high byte
	pkt[5] = 0 // QDCOUNT low byte → 0 questions
	if _, _, err := parseDNSQuestion(pkt); err == nil {
		t.Errorf("expected error when QDCOUNT=0, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestParseDNSQuestion ./..."
```

Expected: FAIL — `parseDNSQuestion` undefined.

- [ ] **Step 3: Write the parser**

`proxy/udp-redir/dnsparse.go`:

```go
package main

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// parseDNSQuestion reads the QNAME and QTYPE of the first question in a
// DNS query payload. Only RFC 1035 wire format (no compression in the
// question section — which the RFC actually forbids anyway). Returns
// the dotted name (e.g. "example.com") and the QTYPE.
func parseDNSQuestion(payload []byte) (string, uint16, error) {
	if len(payload) < 12 {
		return "", 0, fmt.Errorf("payload too short for DNS header: %d", len(payload))
	}
	qdcount := binary.BigEndian.Uint16(payload[4:6])
	if qdcount == 0 {
		return "", 0, fmt.Errorf("no questions in DNS payload")
	}
	// Skip the 12-byte header, then walk labels.
	i := 12
	var labels []string
	for i < len(payload) {
		n := int(payload[i])
		if n == 0 {
			i++
			break
		}
		// RFC 1035 §4.1.4: compression pointers MAY appear in answers/
		// authority/additional but MUST NOT appear in questions. We
		// reject pointer bytes here to keep the parser tight.
		if n&0xc0 != 0 {
			return "", 0, fmt.Errorf("unexpected compression pointer in question at offset %d", i)
		}
		if i+1+n > len(payload) {
			return "", 0, fmt.Errorf("label overrun at offset %d", i)
		}
		labels = append(labels, string(payload[i+1:i+1+n]))
		i += 1 + n
	}
	if i+4 > len(payload) {
		return "", 0, fmt.Errorf("truncated qtype/qclass at offset %d", i)
	}
	qtype := binary.BigEndian.Uint16(payload[i : i+2])
	return strings.Join(labels, "."), qtype, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestParseDNSQuestion ./..."
```

Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add proxy/udp-redir/dnsparse.go proxy/udp-redir/dnsparse_test.go
git commit -m "feat(udp-redir): RFC 1035 DNS question parser"
```

---

### Task 4: Rule file loader

**Files:**
- Create: `proxy/udp-redir/rules.go`
- Create: `proxy/udp-redir/rules_test.go`

The daemon reads the same `rules.json` file that the Python `RuleStore` writes. Phase 0 made the on-disk shape `{id, direction, proto, match, action, label, ...}`. The Go matcher mirrors `RuleStore.match_request`.

- [ ] **Step 1: Write the failing test**

`proxy/udp-redir/rules_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRules(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	return p
}

func TestLoadRules_ParsesNewShape(t *testing.T) {
	dir := t.TempDir()
	p := writeRules(t, dir, `[
		{"id":"1","direction":"out","proto":"udp","match":{"host":"1.1.1.1"},"action":"allow"}
	]`)
	rs, err := loadRules(p)
	if err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("len=%d, want 1", len(rs))
	}
	if rs[0].Action != "allow" || rs[0].Proto != "udp" || rs[0].Match["host"] != "1.1.1.1" {
		t.Errorf("unexpected rule: %+v", rs[0])
	}
}

func TestMatchUDP_ProtoExact(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
	}
	got := matchUDP(rs, "1.1.1.1", 53, "")
	if got != "allow" {
		t.Errorf("got %q, want allow", got)
	}
	// TCP-only rule must NOT match UDP.
	rs[0].Proto = "tcp"
	if got := matchUDP(rs, "1.1.1.1", 53, ""); got != "" {
		t.Errorf("tcp-only rule matched UDP: %q", got)
	}
}

func TestMatchUDP_AnyProto(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "any", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
	}
	if got := matchUDP(rs, "1.1.1.1", 53, ""); got != "allow" {
		t.Errorf("got %q, want allow", got)
	}
}

func TestMatchUDP_DenyBeatsAllow(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "deny"},
	}
	if got := matchUDP(rs, "1.1.1.1", 53, ""); got != "deny" {
		t.Errorf("got %q, want deny", got)
	}
}

func TestMatchUDP_DNSName(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"dns_name": "example.com"}, Action: "allow"},
	}
	if got := matchUDP(rs, "1.1.1.1", 53, "example.com"); got != "allow" {
		t.Errorf("got %q, want allow", got)
	}
	if got := matchUDP(rs, "1.1.1.1", 53, "evil.com"); got != "" {
		t.Errorf("got %q, want empty (no match)", got)
	}
}

func TestMatchUDP_NoMatchReturnsEmpty(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
	}
	if got := matchUDP(rs, "8.8.8.8", 53, ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestLoadRules ./..."
```

Expected: FAIL — `loadRules`, `Rule`, `matchUDP` undefined.

- [ ] **Step 3: Write the loader + matcher**

`proxy/udp-redir/rules.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Rule mirrors the Phase 0 canonical schema written by RuleStore.to_dict
// in proxy/claude_proxy/rules.py. We only care about the fields used by
// the UDP matcher; extra fields are tolerated by the json decoder.
type Rule struct {
	ID        string         `json:"id"`
	Direction string         `json:"direction"`
	Proto     string         `json:"proto"`
	Match     map[string]any `json:"match"`
	Action    string         `json:"action"`
	Label     string         `json:"label"`
}

// loadRules reads a rules.json file and returns the parsed list.
func loadRules(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("udp-redir: read %s: %w", path, err)
	}
	var rs []Rule
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("udp-redir: parse %s: %w", path, err)
	}
	return rs, nil
}

// matchUDP evaluates the rule list against a UDP request descriptor.
// Mirrors RuleStore.match_request: deny rules first, then allow rules,
// "any" proto wildcards, direction must be "out". Returns "allow",
// "deny", or "" if no rule matches.
func matchUDP(rs []Rule, dstHost string, dstPort uint16, dnsName string) string {
	check := func(action string) string {
		for _, r := range rs {
			if r.Direction != "out" {
				continue
			}
			if r.Proto != "any" && r.Proto != "udp" {
				continue
			}
			if r.Action != action {
				continue
			}
			if !udpMatchFields(r.Match, dstHost, dstPort, dnsName) {
				continue
			}
			return action
		}
		return ""
	}
	if v := check("deny"); v != "" {
		return v
	}
	if v := check("allow"); v != "" {
		return v
	}
	return ""
}

// udpMatchFields evaluates the match object for a UDP request. Returns
// true only if every constraint present in m is satisfied. An empty m
// matches everything.
func udpMatchFields(m map[string]any, dstHost string, dstPort uint16, dnsName string) bool {
	if h, ok := m["host"].(string); ok && h != "" {
		if !strings.EqualFold(h, dstHost) {
			return false
		}
	}
	if hr, ok := m["host_regex"].(string); ok && hr != "" {
		re, err := regexp.Compile(hr)
		if err != nil {
			return false
		}
		if !re.MatchString(dstHost) {
			return false
		}
	}
	if p, ok := m["port"].(float64); ok && p != 0 {
		if uint16(p) != dstPort {
			return false
		}
	}
	if d, ok := m["dns_name"].(string); ok && d != "" {
		if !strings.EqualFold(d, dnsName) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestLoadRules ./... && go test -run TestMatchUDP ./..."
```

Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add proxy/udp-redir/rules.go proxy/udp-redir/rules_test.go
git commit -m "feat(udp-redir): rules.json loader + UDP matcher"
```

---

### Task 5: Hold buffer with TTL

**Files:**
- Create: `proxy/udp-redir/holdbuf.go`
- Create: `proxy/udp-redir/holdbuf_test.go`

- [ ] **Step 1: Write the failing test**

`proxy/udp-redir/holdbuf_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestHoldBuf_StoresAndDrainsTuple(t *testing.T) {
	b := newHoldBuf(16, 30*time.Second)
	key := flowKey{DstIP: "1.1.1.1", DstPort: 53, DNSName: "example.com"}
	b.Add(key, 42)
	b.Add(key, 43)
	ids := b.Drain(key)
	if len(ids) != 2 || ids[0] != 42 || ids[1] != 43 {
		t.Errorf("Drain returned %v, want [42 43]", ids)
	}
	// After draining the tuple should be empty.
	if rest := b.Drain(key); len(rest) != 0 {
		t.Errorf("second Drain returned %v, want empty", rest)
	}
}

func TestHoldBuf_CapsAtMaxPerTuple(t *testing.T) {
	b := newHoldBuf(2, 30*time.Second)
	key := flowKey{DstIP: "1.1.1.1", DstPort: 53}
	dropped := b.Add(key, 1)
	if dropped != 0 {
		t.Errorf("first Add dropped %d, want 0", dropped)
	}
	b.Add(key, 2)
	dropped = b.Add(key, 3)
	if dropped != 1 {
		t.Errorf("third Add dropped %d, want 1 (FIFO eviction id=1)", dropped)
	}
	ids := b.Drain(key)
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Errorf("after eviction got %v, want [2 3]", ids)
	}
}

func TestHoldBuf_EvictExpired(t *testing.T) {
	b := newHoldBuf(16, 50*time.Millisecond)
	key := flowKey{DstIP: "1.1.1.1", DstPort: 53}
	b.Add(key, 7)
	time.Sleep(80 * time.Millisecond)
	expired := b.EvictExpired()
	if len(expired) != 1 || expired[0].ID != 7 {
		t.Errorf("EvictExpired returned %v, want [{ID:7}]", expired)
	}
	if rest := b.Drain(key); len(rest) != 0 {
		t.Errorf("after expiration, Drain returned %v", rest)
	}
}

func TestHoldBuf_ListSnapshot(t *testing.T) {
	b := newHoldBuf(16, 30*time.Second)
	b.Add(flowKey{DstIP: "1.1.1.1", DstPort: 53, DNSName: "example.com"}, 1)
	b.Add(flowKey{DstIP: "8.8.8.8", DstPort: 53}, 2)
	entries := b.List()
	if len(entries) != 2 {
		t.Fatalf("len=%d, want 2", len(entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestHoldBuf ./..."
```

Expected: FAIL — `newHoldBuf`, `flowKey` undefined.

- [ ] **Step 3: Write the buffer**

`proxy/udp-redir/holdbuf.go`:

```go
package main

import (
	"sync"
	"time"
)

// flowKey identifies the (dst, dns_name) tuple a packet is held under.
// For non-DNS flows DNSName is empty; equal-by-value tuples are equal as
// map keys.
type flowKey struct {
	DstIP   string
	DstPort uint16
	DNSName string
}

// HeldPacket is one queued kernel packet awaiting verdict, plus its
// expiration deadline.
type HeldPacket struct {
	ID        uint32
	ExpiresAt time.Time
}

// HoldBuf stores deferred-verdict packet IDs per flowKey with a per-tuple
// max size (FIFO eviction) and a wall-clock TTL.
type HoldBuf struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	pending map[flowKey][]HeldPacket
}

func newHoldBuf(max int, ttl time.Duration) *HoldBuf {
	return &HoldBuf{
		max:     max,
		ttl:     ttl,
		pending: make(map[flowKey][]HeldPacket),
	}
}

// Add stores a packet under key. Returns the ID of an evicted packet
// (FIFO) if the per-tuple max was already reached, otherwise 0.
func (b *HoldBuf) Add(key flowKey, id uint32) uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue := b.pending[key]
	var dropped uint32
	if len(queue) >= b.max {
		dropped = queue[0].ID
		queue = queue[1:]
	}
	queue = append(queue, HeldPacket{ID: id, ExpiresAt: time.Now().Add(b.ttl)})
	b.pending[key] = queue
	return dropped
}

// Drain removes and returns every held packet ID for the given key.
func (b *HoldBuf) Drain(key flowKey) []uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue := b.pending[key]
	delete(b.pending, key)
	ids := make([]uint32, len(queue))
	for i, p := range queue {
		ids[i] = p.ID
	}
	return ids
}

// EvictExpired removes every packet whose ExpiresAt has passed and
// returns the evicted entries. Caller is responsible for issuing a DROP
// verdict on each.
func (b *HoldBuf) EvictExpired() []HeldPacket {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	var expired []HeldPacket
	for key, queue := range b.pending {
		keep := queue[:0]
		for _, p := range queue {
			if now.After(p.ExpiresAt) {
				expired = append(expired, p)
			} else {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			delete(b.pending, key)
		} else {
			b.pending[key] = keep
		}
	}
	return expired
}

// List returns a snapshot of all held flowKeys (one entry per non-empty
// tuple, regardless of how many packets it holds). Used by /pending.
func (b *HoldBuf) List() []flowKey {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]flowKey, 0, len(b.pending))
	for key := range b.pending {
		out = append(out, key)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test -run TestHoldBuf ./..."
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add proxy/udp-redir/holdbuf.go proxy/udp-redir/holdbuf_test.go
git commit -m "feat(udp-redir): per-tuple deferred-verdict hold buffer with TTL"
```

---

### Task 6: NFQUEUE loop with allow/deny verdicts

**Files:**
- Modify: `proxy/udp-redir/main.go`

This task wires the queue. No HOLD yet — packets that don't match an allow/deny rule are dropped (consistent with the default-deny posture). Task 7 layers HOLD on top.

- [ ] **Step 1: Replace `main.go` with the queue loop**

`proxy/udp-redir/main.go`:

```go
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
	queueNum    = 0
	maxPktSize  = 0xffff
	defaultTTL  = 30 * time.Second
	defaultMax  = 16
)

// state is the live verdict-engine state shared between the queue
// callback and the (later) Unix-socket API. Rules are reloaded on file
// mtime change; the hold buffer is added in Task 7.
type state struct {
	rulesPath  string
	rulesMu    sync.RWMutex
	rules      []Rule
	rulesMtime time.Time
}

func newState(rulesPath string) *state {
	return &state{rulesPath: rulesPath}
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
			// No rule matched. Task 7 will hold; for now we drop —
			// the firewall is default-deny anyway.
			denyCount.Add(1)
			_ = nf.SetVerdict(id, nfqueue.NfDrop)
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

	// Block forever — Register spawned a background reader goroutine.
	select {}
}

// ensure helper symbol — fmt used in some build configurations.
var _ = fmt.Sprintf
```

- [ ] **Step 2: Build to confirm**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go build ./..."
```

Expected: clean build.

- [ ] **Step 3: Run existing unit tests**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test ./..."
```

Expected: all parser + matcher + holdbuf tests still pass.

- [ ] **Step 4: Commit**

```bash
git add proxy/udp-redir/main.go
git commit -m "feat(udp-redir): NFQUEUE loop with allow/deny verdicts"
```

---

### Task 7: HOLD verdict + TTL eviction

**Files:**
- Modify: `proxy/udp-redir/main.go`

Hold semantics: when no rule matches, defer the verdict (don't `SetVerdict` immediately), buffer the kernel packet-id in the hold buffer keyed by flow tuple, and let a background ticker DROP-expire it after 30 seconds. Tasks 8 + 9 add the API to resolve held flows.

- [ ] **Step 1: Add hold buffer + state.held field**

In `proxy/udp-redir/main.go`, add a field to the `state` struct:

```go
type state struct {
	rulesPath  string
	rulesMu    sync.RWMutex
	rules      []Rule
	rulesMtime time.Time

	held *HoldBuf
	nf   *nfqueue.Nfqueue // for issuing deferred verdicts from outside the queue callback
}
```

Initialize the buffer in `newState`:

```go
func newState(rulesPath string) *state {
	return &state{
		rulesPath: rulesPath,
		held:      newHoldBuf(defaultMax, defaultTTL),
	}
}
```

And after `nf, err := nfqueue.Open(cfg)` in `main`, set the reference:

```go
	st.nf = nf
```

- [ ] **Step 2: Update the queue callback to HOLD on no-match**

Replace the `default:` arm of the `switch st.verdict(...)`:

```go
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
```

(No `SetVerdict` for the new packet — it stays in the kernel queue.)

- [ ] **Step 3: Add a TTL eviction goroutine in main**

After the counter goroutine, add:

```go
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
```

- [ ] **Step 4: Build + run unit tests**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go build ./... && go test ./..."
```

Expected: clean build, all tests still pass (held semantics are tested via Task 5's holdbuf tests).

- [ ] **Step 5: Commit**

```bash
git add proxy/udp-redir/main.go
git commit -m "feat(udp-redir): hold packets when no rule matches; TTL DROP after 30s"
```

---

### Task 8: Unix-socket /pending and /resolve API

**Files:**
- Create: `proxy/udp-redir/api.go`
- Modify: `proxy/udp-redir/main.go`

Endpoint shape mirrors the existing dashboard `/api/pending` and `/api/resolve` so the dashboard's merging code can stay simple. Auth: none — the socket is `0600` chowned to the proxy uid, same as publish-mgr.

- [ ] **Step 1: Write the API file**

`proxy/udp-redir/api.go`:

```go
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
	FlowID   string `json:"flow_id"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DNSName  string `json:"dns_name,omitempty"`
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
```

- [ ] **Step 2: Start the API in main**

In `proxy/udp-redir/main.go`, after `st.nf = nf`, add:

```go
	if err := startAPI(st); err != nil {
		log.Fatalf("udp-redir: start API: %v", err)
	}
```

- [ ] **Step 3: Build to confirm**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go build ./..."
```

Expected: clean build.

- [ ] **Step 4: Re-tidy after new imports**

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go mod tidy"
```

Re-run unit tests:

```bash
devenv shell -- bash -c "cd proxy/udp-redir && go test ./..."
```

Expected: all tests pass (no new tests; API is exercised by E2E).

- [ ] **Step 5: Commit**

```bash
git add proxy/udp-redir/
git commit -m "feat(udp-redir): Unix socket API for /pending and /resolve"
```

---

## Phase 2B — proxy image + nftables

### Task 9: Bake udp-redir into the proxy image

**Files:**
- Modify: `nix/proxy-image.nix`

- [ ] **Step 1: Add the Go derivation alongside publishMgr (placeholder hash)**

In `nix/proxy-image.nix`, after the existing `publishMgr = pkgs.buildGoModule { ... }`:

```nix
  udpRedir = pkgs.buildGoModule {
    pname = "udp-redir";
    version = "0.1.0";
    src = ../proxy/udp-redir;
    vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
    subPackages = [ "." ];
  };
```

This `sha256-AAAA...` placeholder is intentional — nix will fail the first build and print the *actual* hash it computed. Copy that real hash in.

- [ ] **Step 1b: Get the real vendorHash**

```bash
nix build .#claude-proxy-image 2>&1 | grep -E "got:|specified:"
```

Expected output (the `got:` line is the real hash):

```
specified: sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
got:       sha256-<real-hash-here>
```

Replace `sha256-AAAA...` in `nix/proxy-image.nix` with the `got:` value.

- [ ] **Step 2: Add to contents**

In the `contents = [ ... ]` block, after `publishMgr`:

```nix
    udpRedir
```

- [ ] **Step 3: Build to confirm**

```bash
nix build .#claude-proxy-image
```

Expected: build succeeds. The output is `result -> /nix/store/.../claude-proxy.tar.gz`.

- [ ] **Step 4: Commit**

```bash
git add nix/proxy-image.nix
git commit -m "build(proxy-image): bake udp-redir binary into the image"
```

---

### Task 10: nft NFQUEUE rule + entrypoint wiring

**Files:**
- Modify: `nix/proxy-image.nix`

- [ ] **Step 1: Replace the UDP block in the nft heredoc**

In `nix/proxy-image.nix`, find this section in the `output` chain:

```
        # Kill QUIC so HTTPS clients downgrade to TCP and hit the redirect.
        udp dport 443 drop

        # Kill external UDP/53 explicitly so the drop is visible in the log
        # prefix below (not just a silent "default policy drop").
        udp dport 53 log prefix "claude_proxy_fw dns-udp drop: " level debug drop
```

Replace with:

```
        # Kill QUIC so HTTPS clients downgrade to TCP and hit the redirect.
        udp dport 443 drop

        # ----- UDP outbound via NFQUEUE -----
        # Queue every other outbound UDP packet (from non-mitmproxy uids)
        # to userspace. udp-redir reads queue 0 and verdicts each packet
        # against the rule store. The "bypass" flag means: if the daemon
        # is not listening, packets fall through to the final drop below
        # — UDP fails closed.
        meta l4proto udp meta skuid != ${proxyUid} queue num 0 bypass

        # Fallback: if NFQUEUE bypassed (daemon down), drop any UDP that
        # made it this far. This rule will NOT fire when udp-redir is up
        # because the queue verdicts (ACCEPT/DROP) are applied before
        # output continues evaluating.
        udp drop
```

(Remove the old explicit `udp dport 53 log ... drop` line — udp-redir now sees and verdicts those packets directly, with DNS-aware UX.)

- [ ] **Step 2: Update the entrypoint to start udp-redir**

In the same file, the publish-mgr start block currently looks like:

```bash
    ${pkgs.coreutils}/bin/mkdir -p /run
    PROXY_UID=${proxyUid} PROXY_GID=${proxyGid} \
      ${publishMgr}/bin/publish-mgr &
```

Add the udp-redir start right after it (still BEFORE `exec ... mitmproxy`):

```bash
    # udp-redir owns NFQUEUE 0 and gates outbound UDP via the rule store.
    # Same root + chown-socket pattern as publish-mgr: needs CAP_NET_ADMIN
    # to bind to the queue, but the dashboard reaches it through a
    # uid-1500-owned Unix socket.
    PROXY_UID=${proxyUid} PROXY_GID=${proxyGid} \
      PROXY_SESSION="$PROXY_SESSION" \
      ${udpRedir}/bin/udp-redir &
```

- [ ] **Step 3: Rebuild the image and verify**

```bash
nix build .#claude-proxy-image
```

Expected: success.

- [ ] **Step 4: Commit**

```bash
git add nix/proxy-image.nix
git commit -m "feat(proxy-image): nft NFQUEUE rule + start udp-redir at boot"
```

---

## Phase 2C — dashboard integration

### Task 11: Dashboard /api/pending merges UDP holds

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py`
- Modify: `proxy/tests/test_dashboard.py`

- [ ] **Step 1: Write the failing test**

Append to `proxy/tests/test_dashboard.py`:

```python
def test_pending_endpoint_merges_udp_holds(client, monkeypatch):
    """GET /api/pending merges udp-redir's held flows into the response."""
    import httpx
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            return httpx.Response(200, json=[{
                "flow_id": "udp-abcdef",
                "kind": "udp",
                "url": "udp://1.1.1.1:53",
                "host": "1.1.1.1",
                "port": 53,
                "dns_name": "example.com",
                "remaining": 30,
            }])

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_udp_redir_transport", FakeTransport())

    resp = client.get("/api/pending")
    assert resp.status_code == 200
    items = resp.json()
    # The fixture's addon has no pending, but the merge should surface
    # the UDP entry.
    udp = [i for i in items if i.get("kind") == "udp"]
    assert len(udp) == 1
    assert udp[0]["dns_name"] == "example.com"
```

- [ ] **Step 2: Run test to verify it fails**

```bash
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py::test_pending_endpoint_merges_udp_holds -v"
```

Expected: FAIL — `_udp_redir_transport` doesn't exist; merge logic doesn't exist.

- [ ] **Step 3: Add the transport + update get_pending**

In `proxy/claude_proxy/dashboard.py`, near the existing `_publish_mgr_transport` declaration, add:

```python
_udp_redir_transport = httpx.HTTPTransport(uds="/run/udp-redir.sock")
```

Replace the existing `get_pending` function with:

```python
async def get_pending(request: Request) -> JSONResponse:
    """Return list of pending requests (HTTP/TCP from addon + UDP from udp-redir)."""
    out: list[dict] = []
    if _addon is not None:
        out.extend(_addon.get_pending())
    # Merge UDP holds; fail soft so HTTP pendings still surface if the
    # daemon is down.
    try:
        with httpx.Client(transport=_udp_redir_transport) as c:
            r = c.get("http://udp-redir/pending", timeout=2)
        if r.status_code == 200:
            out.extend(r.json())
    except Exception as exc:
        logger.debug("udp-redir /pending unreachable: %s", exc)
    return JSONResponse(out)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py::test_pending_endpoint_merges_udp_holds -v"
```

Expected: PASS.

- [ ] **Step 5: Run full dashboard suite**

```bash
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py -q"
```

Expected: 21/21 (20 existing + 1 new) pass.

- [ ] **Step 6: Commit**

```bash
git add proxy/claude_proxy/dashboard.py proxy/tests/test_dashboard.py
git commit -m "feat(proxy/dashboard): merge udp-redir holds into /api/pending"
```

---

### Task 12: Dashboard /api/resolve forwards UDP flows

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py`
- Modify: `proxy/tests/test_dashboard.py`

- [ ] **Step 1: Write the failing test**

Append to `proxy/tests/test_dashboard.py`:

```python
def test_resolve_endpoint_forwards_udp_flow(client, monkeypatch):
    """POST /api/resolve with a udp- flow_id forwards to udp-redir socket."""
    import httpx
    calls = []
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            calls.append((request.method, request.url.path,
                          request.read().decode()))
            return httpx.Response(200, json={"ok": True})

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_udp_redir_transport", FakeTransport())

    resp = client.post("/api/resolve", json={
        "flow_id": "udp-abcdef",
        "action": "allow",
        "pattern": "1.1.1.1",
    })
    assert resp.status_code == 200
    assert resp.json() == {"ok": True}
    assert len(calls) == 1
    assert calls[0][1].endswith("/resolve")
```

- [ ] **Step 2: Run test to verify it fails**

```bash
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py::test_resolve_endpoint_forwards_udp_flow -v"
```

Expected: FAIL — `/api/resolve` currently always calls `_addon.resolve` and doesn't recognize `udp-` flow_ids.

- [ ] **Step 3: Update resolve_pending to dispatch UDP flows**

In `proxy/claude_proxy/dashboard.py`, replace the body of `resolve_pending` with:

```python
async def resolve_pending(request: Request) -> JSONResponse:
    """Resolve a pending flow. Body: {flow_id, action, pattern, label?, expires_at?}.

    flow_ids prefixed with "udp-" are forwarded to the udp-redir daemon;
    everything else is handled by the addon as before.
    """
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    body = await request.json()
    flow_id = body.get("flow_id")
    action = body.get("action")
    pattern = body.get("pattern")
    if not flow_id or not action or not pattern:
        return JSONResponse(
            {"error": "flow_id, action, and pattern are required"}, status_code=400
        )

    # UDP flows live in udp-redir's hold buffer.
    if flow_id.startswith("udp-"):
        try:
            with httpx.Client(transport=_udp_redir_transport) as c:
                r = c.post("http://udp-redir/resolve",
                           json={"flow_id": flow_id, "action": action,
                                 "pattern": pattern},
                           timeout=5)
            await broadcast({"type": "resolved",
                             "data": {"flow_id": flow_id, "action": action,
                                      "pattern": pattern}})
            return JSONResponse(r.json(), status_code=r.status_code)
        except Exception as exc:
            return JSONResponse({"error": f"udp-redir: {exc}"}, status_code=502)

    if _addon is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    label = body.get("label", "")
    expires_at = body.get("expires_at")
    found = _addon.resolve(flow_id, action, pattern, label, expires_at=expires_at)
    if not found:
        return JSONResponse({"error": "flow not found"}, status_code=404)
    _save_profile()
    await broadcast({
        "type": "resolved",
        "data": {
            "flow_id": flow_id,
            "action": action,
            "pattern": pattern,
        },
    })
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"ok": True})
```

- [ ] **Step 4: Run test to verify it passes**

```bash
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py::test_resolve_endpoint_forwards_udp_flow -v"
```

Expected: PASS.

- [ ] **Step 5: Run full dashboard suite**

```bash
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py -q"
```

Expected: 22/22 pass.

- [ ] **Step 6: Commit**

```bash
git add proxy/claude_proxy/dashboard.py proxy/tests/test_dashboard.py
git commit -m "feat(proxy/dashboard): forward udp- flow_ids to udp-redir /resolve"
```

---

### Task 13: Dashboard UI surfaces UDP/DNS pending flows

**Files:**
- Modify: `proxy/static/app.js`

The existing pending UI reads `kind` and `url`. It already renders `udp://` URLs literally (Phase 1 added that for raw-TCP holds via mitmproxy). We just need to surface the `dns_name` field for DNS queries so the user knows what they're approving.

- [ ] **Step 1: Find the pending-row renderer**

```bash
grep -n "renderPending\|pending-row\|kind\|dns_name" proxy/static/app.js | head -20
```

Identify the function that renders one pending entry into a card.

- [ ] **Step 2: Add a dns_name caption when present**

In the pending-row template, after the URL is rendered, conditionally inject:

```javascript
const dnsLabel = entry.dns_name
  ? `<div class="pending-dns">DNS query for <strong>${entry.dns_name}</strong></div>`
  : "";
```

And insert `${dnsLabel}` after the URL line in the card HTML template.

- [ ] **Step 3: Add CSS in `proxy/static/style.css`**

Append:

```css
.pending-dns {
  font-size: 0.9em;
  color: #666;
  margin-top: 2px;
}
```

- [ ] **Step 4: Manual verification**

There is no automated UI test. Confirm visually that with a UDP/53 entry in `/api/pending`, the card displays the DNS name. Skip this step for now if the proxy image is not running; the E2E tests in the next phase exercise the full path.

- [ ] **Step 5: Commit**

```bash
git add proxy/static/app.js proxy/static/style.css
git commit -m "feat(proxy/dashboard-ui): show DNS query name on UDP pending cards"
```

---

## Phase 2D — E2E tests + docs

### Task 14: E2E — outbound UDP to allowed host succeeds

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Add the test**

Append to `cmd/security_e2e_test.go`:

```go
// TestSecurity_UDPOutbound_AllowedHostPasses verifies that with a
// pre-allow rule for a destination, outbound UDP from inside the
// container reaches a host-side UDP server.
func TestSecurity_UDPOutbound_AllowedHostPasses(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-udp-allow"
	startSecurityContainer(t, name, "--yolo")

	// Host listens on a random port, container will UDP-send to it.
	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()
	hostAddr := srv.LocalAddr().(*net.UDPAddr)

	// Pre-allow the host's IP via the proxy rule store.
	api := newProxyAPI(t, configDir, name)
	api.addRule(t, "allow", "127.0.0.1")
	// Phase 0 stores legacy "allow" as proto=any, so UDP is covered.

	// Container-side: send a single UDP datagram to the host port.
	go boundedDockerExec(t, 10*time.Second, name, "sh", "-c",
		fmt.Sprintf(`python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.sendto(b'HELLO UDP', ('host.docker.internal' if False else '127.0.0.1', %d))
"`, hostAddr.Port))

	srv.SetReadDeadline(time.Now().Add(8 * time.Second))
	buf := make([]byte, 256)
	n, _, err := srv.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom timed out (UDP didn't pass through): %v", err)
	}
	if string(buf[:n]) != "HELLO UDP" {
		t.Errorf("got %q, want HELLO UDP", buf[:n])
	}
}
```

- [ ] **Step 2: Build verify (don't run live)**

```bash
devenv shell -- go build ./... && devenv shell -- go vet ./...
```

Expected: pass.

- [ ] **Step 3: Commit**

```bash
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E UDP outbound passes when destination is allowed"
```

---

### Task 15: E2E — outbound UDP to denied host is dropped

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Add the test**

Append:

```go
// TestSecurity_UDPOutbound_DeniedByDefault verifies that with the
// default rules (no UDP allowlist), an outbound UDP datagram is dropped
// — the host listener never sees it.
func TestSecurity_UDPOutbound_DeniedByDefault(t *testing.T) {
	requireDockerAndAuth(t)
	setupIsolatedConfigDir(t)

	name := "sec-udp-deny"
	startSecurityContainer(t, name, "--yolo")

	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()
	hostAddr := srv.LocalAddr().(*net.UDPAddr)

	go boundedDockerExec(t, 6*time.Second, name, "sh", "-c",
		fmt.Sprintf(`python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.sendto(b'SHOULD NOT ARRIVE', ('127.0.0.1', %d))
"`, hostAddr.Port))

	srv.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	if n, _, err := srv.ReadFrom(buf); err == nil {
		t.Errorf("UDP arrived despite default deny: %q", buf[:n])
	}
}
```

- [ ] **Step 2: Build verify**

```bash
devenv shell -- go build ./... && devenv shell -- go vet ./...
```

- [ ] **Step 3: Commit**

```bash
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E UDP outbound dropped by default"
```

---

### Task 16: E2E — DNS query held + approve replays the packet

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Add the test**

Append:

```go
// TestSecurity_UDPOutbound_DNSHoldAndApprove verifies the full DNS UX:
// container queries an external resolver, udp-redir holds the packet
// and surfaces it on /api/pending with dns_name, test approves via
// /api/resolve, the held packet is re-issued and the container's
// resolver gets a response.
func TestSecurity_UDPOutbound_DNSHoldAndApprove(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-udp-dns"
	startSecurityContainer(t, name, "--yolo")

	api := newProxyAPI(t, configDir, name)

	// Send a query for example.com via 1.1.1.1 from inside the container.
	// This will hit NFQUEUE; the daemon parses the question and HOLDs.
	go boundedDockerExec(t, 15*time.Second, name, "sh", "-c",
		`python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(10)
# RFC 1035 query for example.com, qtype A.
q = bytes.fromhex('1234010000010000000000000765')+ \
    b'example' + bytes.fromhex('03') + b'com' + bytes.fromhex('0000010001')
s.sendto(q, ('1.1.1.1', 53))
try:
    data, _ = s.recvfrom(512)
    open('/tmp/dns-ok','w').write('ok')
except Exception as e:
    open('/tmp/dns-err','w').write(str(e))
"`)

	// Wait for the hold to appear on /api/pending.
	deadline := time.Now().Add(8 * time.Second)
	var flowID string
	for time.Now().Before(deadline) {
		for _, p := range api.getPending(t) {
			if k, _ := p["kind"].(string); k == "udp" {
				if name, _ := p["dns_name"].(string); name == "example.com" {
					flowID, _ = p["flow_id"].(string)
					break
				}
			}
		}
		if flowID != "" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if flowID == "" {
		t.Fatalf("never saw a UDP/DNS pending flow for example.com")
	}

	// Approve via /api/resolve.
	api.resolve(t, flowID, "allow", "1.1.1.1")

	// Give the container time to receive the response and write /tmp/dns-ok.
	time.Sleep(3 * time.Second)
	out, err := boundedDockerExec(t, 3*time.Second, name, "cat", "/tmp/dns-ok")
	if err != nil || !strings.Contains(out, "ok") {
		t.Errorf("container did not receive DNS response after approve: out=%q err=%v", out, err)
	}
}
```

- [ ] **Step 2: Build verify**

```bash
devenv shell -- go build ./... && devenv shell -- go vet ./...
```

- [ ] **Step 3: Commit**

```bash
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E DNS query held by udp-redir + approve replays"
```

---

### Task 17: README — document UDP outbound behavior

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a paragraph after the existing "How it works" section**

In the `## NETWORK PROXY` section, after the `### How it works` block ends and before `### Default allowed domains by profile`, insert:

```markdown
### Outbound UDP (Phase 2)

UDP packets from inside the container are also gated by the rule store —
the path uses NFQUEUE instead of mitmproxy because UDP has no
`SO_ORIGINAL_DST`. A small Go daemon (`udp-redir`) inside the proxy
container reads every outbound UDP datagram from netfilter, parses the
IP/UDP headers (and the DNS question for queries to UDP/53), and either:

- **ACCEPT** if the destination matches a UDP or `proto=any` allow rule
- **DROP** if a deny rule matches
- **HOLD** the packet in the kernel queue (up to 16 per `(dst, port)`
  tuple, 30-second TTL) and surface it on the dashboard's pending list
  alongside HTTP/TCP flows. When you approve, the held packet is
  released to the wire.

DNS queries display the queried hostname in the pending UI so you can
make an informed decision. DNS via the docker embedded resolver
(`127.0.0.11`) bypasses NFQUEUE entirely (it's loopback), so libc-based
`getaddrinfo` keeps working without prompting.

UDP/443 (QUIC) is dropped at the firewall before NFQUEUE — clients fall
back to TCP, which mitmproxy already gates.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(README): document outbound UDP via NFQUEUE + udp-redir"
```

---

## Phase 2 boundary check

After Task 17, run the full Go + Python test suite to catch cross-package regressions:

```bash
devenv shell -- go build ./...
devenv shell -- go vet ./...
devenv shell -- go test ./internal/... ./cmd/
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
devenv shell -- bash -c "cd proxy/udp-redir && go test ./..."
nix build .#claude-proxy-image
```

Expected: every command exits 0. E2E tests in Tasks 14-16 are slow and require running the rebuilt proxy image; reserve them for the pre-release pass.

---

## Self-review

**Spec coverage (against §8 of the design):**

| Spec item | Task |
| --- | --- |
| §8.1 NFQUEUE over TPROXY | Tasks 1, 6 (queue 0 setup) |
| §8.2 daemon as uid 1500 | Task 10 — runs as root because CAP_NET_ADMIN; socket chowned to 1500 (consistent with publish-mgr's revised model from Phase 1 Task 18) |
| §8.2 packet parsing 5-tuple + DNS | Tasks 2, 3 |
| §8.2 RuleStore.match() consult | Task 4 |
| §8.2 allow → NF_ACCEPT | Task 6 |
| §8.2 deny → NF_DROP | Task 6 |
| §8.2 hold → buffer + dashboard pending | Tasks 5, 7, 8 |
| §8.3 nft rule additions + bypass+drop fallback | Task 10 |
| §8.4 DNS-aware UX with parsed name | Tasks 3, 8, 11, 13 |
| §8.5 inbound UDP via publish-mgr | Already shipped in Phase 1 — no new work |

The design's "daemon runs as uid 1500" deviated to "daemon runs as root, socket chowned to 1500" during Phase 1's Task 18 (publish-mgr needed CAP_NET_ADMIN for nft). That deviation is documented and propagates to udp-redir for the same reason (NFQUEUE bind also needs CAP_NET_ADMIN). The threat model is unchanged: the only way in is via the uid-1500-owned Unix socket, which the dashboard mediates with the same auth-token gate.

**Placeholder scan:** no TBD/TODO/"add validation" — each step has full code.

**Type consistency:** `flowKey` (DstIP/DstPort/DNSName) is declared in Task 5 and used in Tasks 6-8; `state` struct grows fields across Tasks 6-7 with each addition shown verbatim; `pendingEntry` shape (Task 8) matches what `dashboard.py:get_pending` already returns (kind/url/host/port/remaining), plus a new `dns_name` field that the UI in Task 13 surfaces.

**Known limitations carried forward:**
- DNS over TCP / DoH still goes through mitmproxy's TCP REDIRECT — the rule store's `dns_name` field only fires for UDP/53. Not a regression; documented behavior.
- The Python `_udp_redir_transport` test fixture (Tasks 11, 12) uses `httpx.HTTPTransport(uds=...)` — that path exists in `.venv-tmp` because httpx was added to dependencies in Phase 1 Task 19.
- udp-redir reloads rules on file mtime change inside the queue callback. If the dashboard writes a new rule and a packet arrives within a few milliseconds, the daemon will see the new rule because `os.WriteFile` advances mtime synchronously. No filesystem watcher needed.
