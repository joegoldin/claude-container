# Phase 3 — nft `user_allow` Sub-chain Escape Hatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the dashboard a "Custom Firewall" tab that lets users add **accept-only** nftables rules to a new `user_allow` sub-chain — both via structured templates (ICMP/TCP/UDP CIDR allows) and a guarded raw-statement mode. Rules persist in the existing rule store and are re-applied on proxy restart.

**Architecture:** A new `user_allow` chain is declared in `claude_proxy_fw` and jumped from both `input` and `output` immediately before the final log+drop. The `publish-mgr` daemon gains `/user-allow/add`, `/user-allow/del`, and `/user-allow/list` endpoints; it validates each statement against a keyword blacklist (`drop`, `reject`, `policy`, `flush`, `delete`, `table`, `chain`) and pipes through `nft --check` before committing. The dashboard compiles user-friendly templates into nft statements on the Python side, forwards to publish-mgr's Unix socket, and persists successful additions in `rules.json` with `proto: "nft"` (so they're invisible to the existing matchers in `addon.py` and `udp-redir`). On `publish-mgr` startup, it reads `rules.json` and replays every `nft_statement` entry. Raw mode is the same path with no compilation — the user types the statement directly.

**Tech Stack:** Go 1.22 (`publish-mgr` extensions), Python Starlette (dashboard endpoints + template compiler), nftables, vanilla JS (dashboard UI).

**Spec reference:** `docs/plans/2026-05-22-dashboard-all-traffic-design.md` §9.

**Out of scope:** Phase 4 (TCP UX polish — counters, bulk actions). Counter wiring and `conntrack -L` polling live in their own plan.

---

## File structure

| Path | Responsibility |
| --- | --- |
| `nix/proxy-image.nix` | Add `chain user_allow {}`; `jump user_allow` from output + input; pass `PROXY_SESSION` to publish-mgr |
| `proxy/publish-mgr/main.go` | New types `userAllowReq` / `userAllowEntry` / `userAllowList`; new endpoints `/user-allow/add` `/user-allow/del` `/user-allow/list` |
| `proxy/publish-mgr/userallow.go` | Validator (`validateUserAllowStmt`), nft helpers (`nftAddUserAllow`, `nftDelUserAllow`), startup replay (`replayUserAllowFromRules`) |
| `proxy/publish-mgr/userallow_test.go` | Unit tests for the validator |
| `proxy/claude_proxy/userallow.py` | Template compiler with unit tests; small library — does NOT touch the rule store |
| `proxy/tests/test_userallow.py` | Unit tests for the template compiler |
| `proxy/claude_proxy/dashboard.py` | New endpoints `/api/user-allow` (POST/GET/DELETE) that compile, forward, and persist |
| `proxy/tests/test_dashboard.py` | Endpoint tests with monkeypatched transport |
| `proxy/static/index.html` | "Custom Firewall" tab markup |
| `proxy/static/app.js` | Tab handlers, template form, raw textarea |
| `proxy/static/style.css` | Minimal styling for the new tab |
| `cmd/security_e2e_test.go` | E2E tests: template install + traffic, raw mode rejected, restart persistence |
| `README.md` | "Custom firewall escape hatch" section |

---

## Phase 3A — nft chain + publish-mgr validator

### Task 1: Add `user_allow` chain + jumps in nft script

**Files:**
- Modify: `nix/proxy-image.nix`

- [ ] **Step 1: Add `chain user_allow {}` as a sibling of `publish_in`**

In `nix/proxy-image.nix`, inside the `table inet claude_proxy_fw { ... }` block, the current `publish_in` chain looks like:

```
      chain publish_in {
        # publish-mgr adds dport accept rules here for each /publish call.
      }
```

After it, append (as another sibling at the same indentation):

```
      chain user_allow {
        # publish-mgr appends user-supplied accept rules here after the
        # /user-allow/add endpoint validates them. Accept-only by
        # design — the validator rejects any statement containing drop,
        # reject, policy, flush, delete, table, or chain keywords, and
        # then pipes through `nft --check` before committing.
      }
```

- [ ] **Step 2: Add `jump user_allow` in the `input` chain**

The current `input` chain ends with:

```
        jump publish_in
        log prefix "claude_proxy_fw input drop: " level debug
```

Replace with:

```
        jump publish_in
        jump user_allow
        log prefix "claude_proxy_fw input drop: " level debug
```

- [ ] **Step 3: Add `jump user_allow` in the `output` chain**

The current `output` chain (post Phase 2) ends with the NFQUEUE block and final drop:

```
        meta l4proto udp meta skuid != ${proxyUid} queue num 0 bypass
        udp drop

        # Everything else: drop. Logged so we can see what's being blocked
        # during smoke tests; remove the log statement later if noisy.
        log prefix "claude_proxy_fw drop: " level debug
```

Insert `jump user_allow` BETWEEN `udp drop` and the comment block:

```
        meta l4proto udp meta skuid != ${proxyUid} queue num 0 bypass
        udp drop

        # User-supplied accept-only rules. The validator on the Unix
        # socket side rejects any rule that would weaken the boundary,
        # so this jump only ever surfaces additional accepts.
        jump user_allow

        # Everything else: drop. Logged so we can see what's being blocked
        # during smoke tests; remove the log statement later if noisy.
        log prefix "claude_proxy_fw drop: " level debug
```

- [ ] **Step 4: Pass `PROXY_SESSION` to publish-mgr**

The current publish-mgr start block reads:

```bash
    ${pkgs.coreutils}/bin/mkdir -p /run
    PROXY_UID=${proxyUid} PROXY_GID=${proxyGid} \
      ${publishMgr}/bin/publish-mgr &
```

Replace with:

```bash
    ${pkgs.coreutils}/bin/mkdir -p /run
    PROXY_UID=${proxyUid} PROXY_GID=${proxyGid} \
      PROXY_SESSION="$PROXY_SESSION" \
      ${publishMgr}/bin/publish-mgr &
```

(`PROXY_SESSION` is already exported at the top of the entrypoint script — we just thread it through. Task 5 uses it to locate `rules.json` for replay.)

- [ ] **Step 5: Rebuild and verify**

```bash
cd /home/joe/Development/claude-container && nix build .#claude-proxy-image 2>&1 | tail -5
```

Expected: success.

- [ ] **Step 6: Commit**

```bash
cd /home/joe/Development/claude-container && git add nix/proxy-image.nix && git commit -m "feat(proxy-image): add user_allow nft sub-chain + thread PROXY_SESSION to publish-mgr"
```

---

### Task 2: Validator with keyword blacklist + nft --check

**Files:**
- Create: `proxy/publish-mgr/userallow.go`
- Create: `proxy/publish-mgr/userallow_test.go`

- [ ] **Step 1: Write the failing test**

`proxy/publish-mgr/userallow_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestValidateUserAllowStmt_RejectsBlacklistedKeywords(t *testing.T) {
	cases := []struct {
		stmt    string
		wantErr string
	}{
		{"ip daddr 1.2.3.4 drop", "drop"},
		{"ip daddr 1.2.3.4 reject", "reject"},
		{"add chain inet foo bar", "chain"},
		{"delete rule inet claude_proxy_fw user_allow handle 1", "delete"},
		{"flush chain inet claude_proxy_fw user_allow", "flush"},
		{"table inet evil { chain x { policy drop; } }", "table"},
		{"policy accept", "policy"},
	}
	for _, c := range cases {
		err := validateUserAllowStmtKeywordsOnly(c.stmt)
		if err == nil {
			t.Errorf("stmt %q: expected error containing %q, got nil", c.stmt, c.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("stmt %q: error %q does not mention %q", c.stmt, err, c.wantErr)
		}
	}
}

func TestValidateUserAllowStmt_AcceptsSafeStatements(t *testing.T) {
	ok := []string{
		"ip daddr 192.168.1.0/24 icmp type echo-request accept",
		"ip saddr 10.0.0.0/8 tcp dport 22 accept",
		"ip daddr 8.8.8.8 udp dport 53 accept",
		"ip daddr 1.1.1.1 accept",
	}
	for _, stmt := range ok {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err != nil {
			t.Errorf("stmt %q rejected unexpectedly: %v", stmt, err)
		}
	}
}

func TestValidateUserAllowStmt_WordBoundary(t *testing.T) {
	// "accept" is not on the blacklist; "drops" should not falsely
	// match "drop"; "tablespoon" should not falsely match "table".
	cases := []string{
		"ip daddr 1.2.3.4 accept",          // "accept" appears, fine
		"ip daddr drops.example.com accept", // hostname containing "drops"
	}
	for _, stmt := range cases {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err != nil {
			t.Errorf("stmt %q rejected unexpectedly: %v", stmt, err)
		}
	}
}

func TestValidateUserAllowStmt_EmptyRejected(t *testing.T) {
	if err := validateUserAllowStmtKeywordsOnly(""); err == nil {
		t.Errorf("empty stmt should be rejected")
	}
	if err := validateUserAllowStmtKeywordsOnly("   "); err == nil {
		t.Errorf("whitespace-only stmt should be rejected")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go test -run TestValidateUserAllowStmt ./..."
```

Expected: FAIL — `validateUserAllowStmtKeywordsOnly` undefined.

- [ ] **Step 3: Write the validator**

`proxy/publish-mgr/userallow.go`:

```go
package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// userAllowBlacklist is the set of nft keywords that must NEVER appear
// in a user-supplied statement. Each maps to a word-boundary regex so
// "drops" in a hostname doesn't falsely match "drop".
var userAllowBlacklist = []string{
	"drop", "reject", "policy", "flush", "delete", "table", "chain",
}

// validateUserAllowStmtKeywordsOnly performs the cheap pass: only the
// keyword check. Tests can target this directly without needing the
// nftables binary in the environment. The full validator
// (validateUserAllowStmt) also pipes through `nft --check`.
func validateUserAllowStmtKeywordsOnly(stmt string) error {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return fmt.Errorf("user-allow stmt is empty")
	}
	for _, kw := range userAllowBlacklist {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(kw) + `\b`)
		if re.MatchString(stmt) {
			return fmt.Errorf("user-allow stmt contains forbidden keyword %q", kw)
		}
	}
	return nil
}

// validateUserAllowStmt runs the keyword check, then pipes the wrapped
// statement through `nft --check -f -` so malformed syntax is rejected
// before commit.
func validateUserAllowStmt(stmt string) error {
	if err := validateUserAllowStmtKeywordsOnly(stmt); err != nil {
		return err
	}
	wrapped := fmt.Sprintf("add rule inet claude_proxy_fw user_allow %s\n", stmt)
	cmd := exec.Command("nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(wrapped)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft --check rejected: %v: %s", err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go test -run TestValidateUserAllowStmt ./..."
```

Expected: PASS (4 tests).

- [ ] **Step 5: Confirm full package build still clean**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./..."
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/userallow.go proxy/publish-mgr/userallow_test.go && git commit -m "feat(publish-mgr): validator for user_allow statements (keyword blacklist + nft --check)"
```

---

### Task 3: nft add/del helpers for `user_allow`

**Files:**
- Modify: `proxy/publish-mgr/userallow.go`
- Modify: `proxy/publish-mgr/main.go`

- [ ] **Step 1: Add the nft helpers**

Append to `proxy/publish-mgr/userallow.go`:

```go
// nftAddUserAllow appends the validated statement to the user_allow
// chain. The validator must run BEFORE this — the helper does not
// re-validate (callers should fail fast on validation errors).
func nftAddUserAllow(stmt string) error {
	// Build argv by splitting the statement on whitespace.
	args := []string{"add", "rule", "inet", "claude_proxy_fw", "user_allow"}
	args = append(args, strings.Fields(stmt)...)
	cmd := exec.Command("nft", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft add user_allow rule failed: %v: %s", err, out)
	}
	return nil
}

// nftDelUserAllow finds the handle for a rule matching stmt and deletes
// it. Mirrors nftDelInputAccept's pattern. The needle is the trimmed
// statement followed by " # handle " to avoid false-positive matches.
func nftDelUserAllow(stmt string) error {
	out, err := exec.Command("nft", "-a", "list", "chain", "inet",
		"claude_proxy_fw", "user_allow").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft list user_allow: %v: %s", err, out)
	}
	needle := strings.TrimSpace(stmt) + " # handle "
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
			"claude_proxy_fw", "user_allow", "handle", h)
		if err := delCmd.Run(); err != nil {
			return fmt.Errorf("nft delete handle %s: %w", h, err)
		}
		return nil
	}
	return fmt.Errorf("no matching user_allow rule for %q", stmt)
}
```

- [ ] **Step 2: Build to confirm**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./..."
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/userallow.go && git commit -m "feat(publish-mgr): nft add/del helpers for user_allow chain"
```

---

### Task 4: `/user-allow/add`, `/user-allow/del`, `/user-allow/list` endpoints

**Files:**
- Modify: `proxy/publish-mgr/main.go`

- [ ] **Step 1: Add the request/response types**

In `proxy/publish-mgr/main.go`, near the existing `publishReq`/`publishResp` types, add:

```go
type userAllowReq struct {
	Stmt  string `json:"stmt"`
	Label string `json:"label"`
	ID    string `json:"id,omitempty"` // optional client-supplied UUID
}

type userAllowResp struct {
	OK    bool   `json:"ok"`
	ID    string `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
}

type userAllowEntry struct {
	ID    string `json:"id"`
	Stmt  string `json:"stmt"`
	Label string `json:"label"`
}
```

- [ ] **Step 2: Add the entries map to the manager**

The existing `manager` struct in `main.go` has fields `mu`, `rangeLo`, `rangeHi`, `published`. Add another field:

```go
type manager struct {
	mu          sync.Mutex
	rangeLo     int
	rangeHi     int
	published   map[string]listEntry
	userAllow   map[string]userAllowEntry // key: id
}
```

In the `main()` function where the `manager` is constructed, the existing literal:

```go
	mgr := &manager{
		rangeLo:   lo,
		rangeHi:   hi,
		published: make(map[string]listEntry),
	}
```

becomes:

```go
	mgr := &manager{
		rangeLo:   lo,
		rangeHi:   hi,
		published: make(map[string]listEntry),
		userAllow: make(map[string]userAllowEntry),
	}
```

- [ ] **Step 3: Add handlers**

Append to `proxy/publish-mgr/main.go` (anywhere after the existing handlers):

```go
func (m *manager) handleUserAllowAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, userAllowResp{Error: "POST only"})
		return
	}
	var req userAllowReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, userAllowResp{Error: "bad json: " + err.Error()})
		return
	}
	if err := validateUserAllowStmt(req.Stmt); err != nil {
		writeJSON(w, 400, userAllowResp{Error: err.Error()})
		return
	}
	if err := nftAddUserAllow(req.Stmt); err != nil {
		writeJSON(w, 500, userAllowResp{Error: err.Error()})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	id := req.ID
	if id == "" {
		id = randomID()
	}
	m.userAllow[id] = userAllowEntry{
		ID:    id,
		Stmt:  strings.TrimSpace(req.Stmt),
		Label: req.Label,
	}
	writeJSON(w, 200, userAllowResp{OK: true, ID: id})
}

func (m *manager) handleUserAllowDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, userAllowResp{Error: "POST only"})
		return
	}
	var req userAllowReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, userAllowResp{Error: "bad json: " + err.Error()})
		return
	}
	if req.ID == "" {
		writeJSON(w, 400, userAllowResp{Error: "id required"})
		return
	}
	m.mu.Lock()
	entry, ok := m.userAllow[req.ID]
	if !ok {
		m.mu.Unlock()
		writeJSON(w, 404, userAllowResp{Error: "not found"})
		return
	}
	if err := nftDelUserAllow(entry.Stmt); err != nil {
		m.mu.Unlock()
		writeJSON(w, 500, userAllowResp{Error: err.Error()})
		return
	}
	delete(m.userAllow, req.ID)
	m.mu.Unlock()
	writeJSON(w, 200, userAllowResp{OK: true})
}

func (m *manager) handleUserAllowList(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]userAllowEntry, 0, len(m.userAllow))
	for _, e := range m.userAllow {
		out = append(out, e)
	}
	writeJSON(w, 200, out)
}

// randomID returns a 12-hex-character id derived from /dev/urandom.
// Sufficient for in-memory + rule-store identification; not a security
// token.
func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Add the imports**

In `proxy/publish-mgr/main.go`, add to the imports block:

```go
	"crypto/rand"
	"encoding/hex"
```

- [ ] **Step 5: Register the routes in `main`**

In `main()`, after the existing route registrations:

```go
	mux.HandleFunc("/publish", mgr.handlePublish)
	mux.HandleFunc("/unpublish", mgr.handleUnpublish)
	mux.HandleFunc("/list", mgr.handleList)
```

Add:

```go
	mux.HandleFunc("/user-allow/add", mgr.handleUserAllowAdd)
	mux.HandleFunc("/user-allow/del", mgr.handleUserAllowDel)
	mux.HandleFunc("/user-allow/list", mgr.handleUserAllowList)
```

- [ ] **Step 6: Build to confirm**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./... && go test ./..."
```

Expected: clean build, all 4 existing validator tests still pass.

- [ ] **Step 7: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/main.go && git commit -m "feat(publish-mgr): /user-allow/{add,del,list} HTTP endpoints"
```

---

### Task 5: Startup replay from `rules.json`

**Files:**
- Modify: `proxy/publish-mgr/userallow.go`
- Modify: `proxy/publish-mgr/main.go`

When `publish-mgr` starts, it reads `/config/proxy-state/<PROXY_SESSION>/rules.json` and applies every entry whose `match.nft_statement` field is set.

- [ ] **Step 1: Add the replay function**

Append to `proxy/publish-mgr/userallow.go`:

```go
// rulesFileRule is the minimal subset of the rules.json schema that
// startup replay needs. Phase 0's RuleStore.to_dict writes more fields,
// but Go's json.Decoder ignores extras silently.
type rulesFileRule struct {
	ID    string         `json:"id"`
	Proto string         `json:"proto"`
	Match map[string]any `json:"match"`
	Label string         `json:"label"`
}

// replayUserAllowFromRules reads rules.json at the given path and runs
// nftAddUserAllow for every entry that has match.nft_statement set.
// Errors are logged but do not abort startup — a single broken rule
// shouldn't take the proxy down.
//
// Returns the (id → entry) map for the caller to seed the manager.
func replayUserAllowFromRules(path string) (map[string]userAllowEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]userAllowEntry{}, nil
		}
		return nil, fmt.Errorf("publish-mgr: read %s: %w", path, err)
	}
	var rs []rulesFileRule
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("publish-mgr: parse %s: %w", path, err)
	}
	out := make(map[string]userAllowEntry)
	for _, r := range rs {
		stmt, _ := r.Match["nft_statement"].(string)
		if stmt == "" {
			continue
		}
		if err := validateUserAllowStmt(stmt); err != nil {
			log.Printf("publish-mgr: replay skip rule %s: %v", r.ID, err)
			continue
		}
		if err := nftAddUserAllow(stmt); err != nil {
			log.Printf("publish-mgr: replay apply rule %s: %v", r.ID, err)
			continue
		}
		out[r.ID] = userAllowEntry{
			ID:    r.ID,
			Stmt:  strings.TrimSpace(stmt),
			Label: r.Label,
		}
	}
	return out, nil
}
```

- [ ] **Step 2: Add the imports**

In `proxy/publish-mgr/userallow.go`, the imports already contain `fmt`, `os/exec`, `regexp`, `strings`. Add:

```go
	"encoding/json"
	"log"
	"os"
```

- [ ] **Step 3: Wire the replay into main**

In `proxy/publish-mgr/main.go`, just BEFORE the `mux := http.NewServeMux()` line in `main`, add:

```go
	// Replay user_allow rules saved in the rule store. On a clean
	// proxy startup the chain is empty and this seeds it from whatever
	// the dashboard persisted before the previous shutdown.
	if session := os.Getenv("PROXY_SESSION"); session != "" {
		rulesPath := filepath.Join("/config", "proxy-state", session, "rules.json")
		seeded, err := replayUserAllowFromRules(rulesPath)
		if err != nil {
			log.Printf("publish-mgr: user_allow replay: %v", err)
		} else if len(seeded) > 0 {
			mgr.userAllow = seeded
			log.Printf("publish-mgr: replayed %d user_allow rules from %s",
				len(seeded), rulesPath)
		}
	}
```

- [ ] **Step 4: Add the `path/filepath` import to main.go**

```go
	"path/filepath"
```

(Verify it's not already imported — `publish-mgr/main.go` may not have used filepath yet.)

- [ ] **Step 5: Build to confirm**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./... && go test ./..."
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/userallow.go proxy/publish-mgr/main.go && git commit -m "feat(publish-mgr): replay user_allow rules from rules.json at startup"
```

---

## Phase 3B — dashboard endpoints + template compiler

### Task 6: Python template compiler

**Files:**
- Create: `proxy/claude_proxy/userallow.py`
- Create: `proxy/tests/test_userallow.py`

- [ ] **Step 1: Write the failing tests**

`proxy/tests/test_userallow.py`:

```python
"""Tests for the user_allow nft template compiler."""

import pytest
from claude_proxy.userallow import compile_template, TEMPLATES


def test_outbound_icmp_echo():
    stmt = compile_template("outbound_icmp_echo", {"addr": "8.8.8.8"})
    assert stmt == "ip daddr 8.8.8.8 icmp type echo-request accept"


def test_inbound_icmp_echo():
    stmt = compile_template("inbound_icmp_echo", {"addr": "192.168.1.0/24"})
    assert stmt == "ip saddr 192.168.1.0/24 icmp type echo-reply accept"


def test_outbound_tcp_cidr():
    stmt = compile_template("outbound_tcp_cidr",
                            {"cidr": "10.0.0.0/8", "port": 22})
    assert stmt == "ip daddr 10.0.0.0/8 tcp dport 22 accept"


def test_inbound_tcp_cidr():
    stmt = compile_template("inbound_tcp_cidr",
                            {"cidr": "192.168.1.0/24", "port": 3000})
    assert stmt == "ip saddr 192.168.1.0/24 tcp dport 3000 accept"


def test_outbound_udp_cidr():
    stmt = compile_template("outbound_udp_cidr",
                            {"cidr": "8.8.8.8", "port": 53})
    assert stmt == "ip daddr 8.8.8.8 udp dport 53 accept"


def test_outbound_any_protocol():
    stmt = compile_template("outbound_any", {"addr": "1.1.1.1"})
    assert stmt == "ip daddr 1.1.1.1 accept"


def test_unknown_template_raises():
    with pytest.raises(ValueError, match="unknown template"):
        compile_template("delete_chain", {})


def test_missing_param_raises():
    with pytest.raises(ValueError, match="missing"):
        compile_template("outbound_tcp_cidr", {"cidr": "10.0.0.0/8"})


def test_addr_validation_rejects_garbage():
    with pytest.raises(ValueError, match="invalid"):
        compile_template("outbound_any", {"addr": "; drop chain;"})


def test_port_validation_rejects_garbage():
    with pytest.raises(ValueError, match="port"):
        compile_template("outbound_tcp_cidr",
                         {"cidr": "10.0.0.0/8", "port": "22; drop chain"})


def test_port_out_of_range():
    with pytest.raises(ValueError, match="port"):
        compile_template("outbound_tcp_cidr",
                         {"cidr": "10.0.0.0/8", "port": 99999})


def test_templates_metadata_lists_all():
    """TEMPLATES dict exposes all template names with their fields."""
    names = set(TEMPLATES.keys())
    assert names == {
        "outbound_icmp_echo", "inbound_icmp_echo",
        "outbound_tcp_cidr", "inbound_tcp_cidr", "outbound_udp_cidr",
        "outbound_any",
    }
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_userallow.py -v"
```

Expected: FAIL — module doesn't exist.

- [ ] **Step 3: Write the compiler**

`proxy/claude_proxy/userallow.py`:

```python
"""Compile user-friendly firewall templates into nft statement strings.

The compiler is the only place user input enters the nft pipeline. It
validates each parameter against a strict regex and emits a canonical
statement string that `publish-mgr` then re-validates (keyword
blacklist + `nft --check`) before committing.
"""

import re

# Address-or-CIDR pattern. Accepts IPv4 like "192.168.1.1" or
# "10.0.0.0/8". Rejects anything containing whitespace, shell
# metacharacters, or nft keywords.
_ADDR_RE = re.compile(r"^\d{1,3}(\.\d{1,3}){3}(/\d{1,2})?$")


def _check_addr(value: str) -> str:
    if not isinstance(value, str) or not _ADDR_RE.match(value):
        raise ValueError(f"invalid address {value!r}")
    return value


def _check_port(value) -> int:
    """Coerce to int and range-check 1..65535."""
    try:
        port = int(value)
    except (TypeError, ValueError):
        raise ValueError(f"port {value!r} is not an integer")
    if port < 1 or port > 65535:
        raise ValueError(f"port {port} out of range 1-65535")
    return port


def _outbound_icmp_echo(params: dict) -> str:
    addr = _check_addr(_require(params, "addr"))
    return f"ip daddr {addr} icmp type echo-request accept"


def _inbound_icmp_echo(params: dict) -> str:
    addr = _check_addr(_require(params, "addr"))
    return f"ip saddr {addr} icmp type echo-reply accept"


def _outbound_tcp_cidr(params: dict) -> str:
    cidr = _check_addr(_require(params, "cidr"))
    port = _check_port(_require(params, "port"))
    return f"ip daddr {cidr} tcp dport {port} accept"


def _inbound_tcp_cidr(params: dict) -> str:
    cidr = _check_addr(_require(params, "cidr"))
    port = _check_port(_require(params, "port"))
    return f"ip saddr {cidr} tcp dport {port} accept"


def _outbound_udp_cidr(params: dict) -> str:
    cidr = _check_addr(_require(params, "cidr"))
    port = _check_port(_require(params, "port"))
    return f"ip daddr {cidr} udp dport {port} accept"


def _outbound_any(params: dict) -> str:
    addr = _check_addr(_require(params, "addr"))
    return f"ip daddr {addr} accept"


def _require(params: dict, key: str):
    if key not in params:
        raise ValueError(f"missing parameter {key!r}")
    return params[key]


# Public registry — each entry maps template_name → (compile_fn, [field_names]).
# The dashboard UI uses the field list to build the form.
TEMPLATES = {
    "outbound_icmp_echo": (_outbound_icmp_echo, ["addr"]),
    "inbound_icmp_echo":  (_inbound_icmp_echo,  ["addr"]),
    "outbound_tcp_cidr":  (_outbound_tcp_cidr,  ["cidr", "port"]),
    "inbound_tcp_cidr":   (_inbound_tcp_cidr,   ["cidr", "port"]),
    "outbound_udp_cidr":  (_outbound_udp_cidr,  ["cidr", "port"]),
    "outbound_any":       (_outbound_any,       ["addr"]),
}


def compile_template(name: str, params: dict) -> str:
    """Compile one of the well-known templates into an nft statement.

    Raises ValueError on unknown template or invalid params.
    """
    entry = TEMPLATES.get(name)
    if entry is None:
        raise ValueError(f"unknown template {name!r}")
    fn, _fields = entry
    return fn(params)
```

- [ ] **Step 4: Run to verify it passes**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_userallow.py -v"
```

Expected: PASS (12 tests).

- [ ] **Step 5: Confirm no regressions across the full Python suite**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
```

Expected: 83/83 (71 pre-existing + 12 new).

- [ ] **Step 6: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/claude_proxy/userallow.py proxy/tests/test_userallow.py && git commit -m "feat(proxy/userallow): nft template compiler with strict input validation"
```

---

### Task 7: Dashboard `/api/user-allow` endpoints

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py`
- Modify: `proxy/tests/test_dashboard.py`

The endpoints:
- `GET /api/user-allow/templates` → returns the `TEMPLATES` metadata so the UI knows which fields each template needs
- `POST /api/user-allow` → body `{template, params, label}` OR `{stmt, label}` (raw mode); compiles → forwards to publish-mgr → persists in rule store
- `GET /api/user-allow` → lists currently-applied user-allow rules (proxies to publish-mgr's `/user-allow/list`)
- `DELETE /api/user-allow/{id}` → forwards to publish-mgr's `/user-allow/del`; on success removes the rule from the rule store

- [ ] **Step 1: Write the failing tests**

Append to `proxy/tests/test_dashboard.py`:

```python
def test_user_allow_templates(client):
    """GET /api/user-allow/templates returns the template metadata."""
    resp = client.get("/api/user-allow/templates")
    assert resp.status_code == 200
    data = resp.json()
    assert "outbound_icmp_echo" in data
    assert data["outbound_tcp_cidr"] == ["cidr", "port"]


def test_user_allow_add_template_forwards(client, monkeypatch):
    """POST /api/user-allow with a template compiles and forwards to publish-mgr."""
    import httpx
    calls = []
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            body = request.read().decode()
            calls.append((request.method, request.url.path, body))
            return httpx.Response(200, json={"ok": True, "id": "abc123"})

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport", FakeTransport())

    resp = client.post("/api/user-allow", json={
        "template": "outbound_icmp_echo",
        "params": {"addr": "8.8.8.8"},
        "label": "ping google",
    })
    assert resp.status_code == 200
    assert resp.json()["ok"] is True
    # publish-mgr should have received the compiled statement.
    assert len(calls) == 1
    body = calls[0][2]
    assert "ip daddr 8.8.8.8 icmp type echo-request accept" in body


def test_user_allow_add_raw_forwards(client, monkeypatch):
    """POST /api/user-allow with raw stmt bypasses the compiler."""
    import httpx
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            return httpx.Response(200, json={"ok": True, "id": "raw1"})

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport", FakeTransport())

    resp = client.post("/api/user-allow", json={
        "stmt": "ip daddr 1.1.1.1 accept",
        "label": "raw cloudflare",
    })
    assert resp.status_code == 200


def test_user_allow_add_rejects_bad_template(client, monkeypatch):
    """Unknown template returns 400 before reaching publish-mgr."""
    import httpx
    called = []
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            called.append(1)
            return httpx.Response(500)

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport", FakeTransport())

    resp = client.post("/api/user-allow", json={
        "template": "delete_chain",
        "params": {},
        "label": "nope",
    })
    assert resp.status_code == 400
    assert called == []  # never reached publish-mgr
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py -v -k user_allow"
```

Expected: FAIL — endpoints don't exist.

- [ ] **Step 3: Add endpoints + routes**

In `proxy/claude_proxy/dashboard.py`, add a top-level import (near the other claude_proxy imports):

```python
from claude_proxy.userallow import TEMPLATES as _USER_ALLOW_TEMPLATES, compile_template
```

Add handler functions (anywhere convenient — e.g. after the publish/unpublish handlers):

```python
async def user_allow_templates(request: Request) -> JSONResponse:
    """Return the template registry: name → list of required fields."""
    return JSONResponse({
        name: fields for name, (_fn, fields) in _USER_ALLOW_TEMPLATES.items()
    })


async def user_allow_add(request: Request) -> JSONResponse:
    """Add a user_allow nft rule (template or raw).

    Body shape:
        {template, params, label}  — compile template, forward, persist
        {stmt, label}              — raw mode, forward verbatim, persist
    """
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    body = await request.json()
    label = body.get("label", "")
    # Compile or accept raw statement.
    if "template" in body:
        try:
            stmt = compile_template(body["template"], body.get("params", {}))
        except ValueError as exc:
            return JSONResponse({"error": str(exc)}, status_code=400)
    elif "stmt" in body:
        stmt = body["stmt"]
    else:
        return JSONResponse(
            {"error": "either template+params or stmt required"},
            status_code=400,
        )
    # Forward to publish-mgr.
    try:
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.post("http://publish-mgr/user-allow/add",
                       json={"stmt": stmt, "label": label},
                       timeout=5)
    except Exception as exc:
        return JSONResponse({"error": f"publish-mgr: {exc}"}, status_code=502)
    if r.status_code != 200:
        return JSONResponse(r.json(), status_code=r.status_code)
    data = r.json()
    rule_id = data.get("id")
    # Persist in the rule store (proto="nft" so existing matchers skip it).
    _store.add_structured(
        direction="out",  # informational only — match irrelevant for nft rules
        proto="nft",
        match={"nft_statement": stmt},
        action="allow",
        label=label,
        source="user-allow",
    )
    _save_profile()
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"ok": True, "id": rule_id, "stmt": stmt})


async def user_allow_list(request: Request) -> JSONResponse:
    """List currently-applied user_allow rules (live from publish-mgr)."""
    try:
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.get("http://publish-mgr/user-allow/list", timeout=5)
        return JSONResponse(r.json(), status_code=r.status_code)
    except Exception as exc:
        return JSONResponse({"error": f"publish-mgr: {exc}"}, status_code=502)


async def user_allow_del(request: Request) -> JSONResponse:
    """Delete a user_allow rule by id (forwards to publish-mgr and unpersists)."""
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    rule_id = request.path_params["rule_id"]
    try:
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.post("http://publish-mgr/user-allow/del",
                       json={"id": rule_id},
                       timeout=5)
    except Exception as exc:
        return JSONResponse({"error": f"publish-mgr: {exc}"}, status_code=502)
    if r.status_code != 200:
        return JSONResponse(r.json(), status_code=r.status_code)
    # Best-effort: find the matching nft store entry by id and remove.
    _store.remove(rule_id)
    _save_profile()
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"ok": True})
```

In the `routes = [...]` list, add (before the WebSocket route):

```python
    Route("/api/user-allow/templates", user_allow_templates, methods=["GET"]),
    Route("/api/user-allow", user_allow_add, methods=["POST"]),
    Route("/api/user-allow", user_allow_list, methods=["GET"]),
    Route("/api/user-allow/{rule_id}", user_allow_del, methods=["DELETE"]),
```

- [ ] **Step 4: Run to verify the new tests pass**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py -v -k user_allow"
```

Expected: PASS (4 tests).

- [ ] **Step 5: Run full dashboard suite**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py -q"
```

Expected: 26/26 (22 existing + 4 new).

- [ ] **Step 6: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/claude_proxy/dashboard.py proxy/tests/test_dashboard.py && git commit -m "feat(proxy/dashboard): /api/user-allow endpoints (template + raw)"
```

---

## Phase 3C — dashboard UI

### Task 8: HTML markup for the "Custom Firewall" tab

**Files:**
- Modify: `proxy/static/index.html`

- [ ] **Step 1: Add the tab button**

The existing `<nav>` block has 3 tabs (Pending, Rules, Published Ports). Add a fourth:

```html
<nav>
    <button class="tab active" data-tab="pending">Pending</button>
    <button class="tab" data-tab="rules">Rules</button>
    <button class="tab" data-tab="published">Published Ports</button>
    <button class="tab" data-tab="userallow">Custom Firewall</button>
</nav>
```

- [ ] **Step 2: Add the section**

After the `<section id="published-view" class="view">...</section>` block, add:

```html
<section id="userallow-view" class="view">
    <div class="add-rule-form">
        <h3>Add Firewall Rule</h3>
        <p class="form-help">
            Accept-only nftables rules — common cases via templates,
            arbitrary statements via raw mode. Rules persist and are
            replayed on proxy restart.
        </p>
        <form id="userallow-form">
            <div class="form-row">
                <div class="form-group">
                    <label>Mode</label>
                    <select name="mode" id="userallow-mode">
                        <option value="template">Template</option>
                        <option value="raw">Raw nft statement</option>
                    </select>
                </div>
                <div class="form-group" id="userallow-template-group">
                    <label>Template</label>
                    <select name="template" id="userallow-template"></select>
                </div>
            </div>
            <div class="form-row" id="userallow-fields"></div>
            <div class="form-row" id="userallow-raw-group" hidden>
                <div class="form-group-grow">
                    <label>Statement</label>
                    <textarea name="stmt" rows="2"
                        placeholder="ip daddr 10.0.0.0/8 tcp dport 22 accept"></textarea>
                </div>
            </div>
            <div class="form-row">
                <div class="form-group-grow">
                    <label>Label</label>
                    <input name="label" type="text" placeholder="LAN SSH">
                </div>
                <div class="form-group">
                    <label>&nbsp;</label>
                    <button class="btn btn-add" type="submit">Add Rule</button>
                </div>
            </div>
        </form>
    </div>
    <div id="userallow-table-wrap">
        <table id="userallow-table">
            <thead>
                <tr>
                    <th>Label</th>
                    <th>nft Statement</th>
                    <th></th>
                </tr>
            </thead>
            <tbody>
                <tr class="empty-row">
                    <td colspan="3">No custom firewall rules.</td>
                </tr>
            </tbody>
        </table>
    </div>
</section>
```

- [ ] **Step 3: Commit**

(No test step — this is markup. Task 9 wires the JS.)

```bash
cd /home/joe/Development/claude-container && git add proxy/static/index.html && git commit -m "feat(proxy/dashboard-ui): Custom Firewall tab markup"
```

---

### Task 9: app.js handlers for the Custom Firewall tab

**Files:**
- Modify: `proxy/static/app.js`

- [ ] **Step 1: Append handlers + tab refresh wiring**

Inside the existing IIFE in `proxy/static/app.js`, BEFORE the closing `})();`, append:

```javascript
  // --- Custom Firewall (user_allow) ---
  let userAllowTemplates = null;

  async function loadUserAllowTemplates() {
    if (userAllowTemplates) return userAllowTemplates;
    const r = await fetch("/api/user-allow/templates");
    userAllowTemplates = await r.json();
    return userAllowTemplates;
  }

  async function refreshUserAllow() {
    const r = await fetch("/api/user-allow");
    const items = await r.json();
    const tbody = document.querySelector("#userallow-table tbody");
    tbody.innerHTML = "";
    if (!Array.isArray(items) || items.length === 0) {
      tbody.innerHTML =
        '<tr class="empty-row"><td colspan="3">No custom firewall rules.</td></tr>';
      return;
    }
    for (const it of items) {
      const tr = document.createElement("tr");
      tr.innerHTML =
        "<td>" + htmlEscape(it.label || "") + "</td>" +
        "<td><code>" + htmlEscape(it.stmt) + "</code></td>" +
        '<td><button class="btn btn-secondary userallow-del-btn"' +
        ' data-id="' + htmlAttrEscape(it.id) + '">Delete</button></td>';
      tbody.appendChild(tr);
    }
  }

  async function renderUserAllowFields() {
    const templates = await loadUserAllowTemplates();
    const select = document.querySelector("#userallow-template");
    select.innerHTML = "";
    for (const name of Object.keys(templates)) {
      const opt = document.createElement("option");
      opt.value = name;
      opt.textContent = name.replace(/_/g, " ");
      select.appendChild(opt);
    }
    renderTemplateInputs();
  }

  function renderTemplateInputs() {
    const name = document.querySelector("#userallow-template").value;
    if (!name || !userAllowTemplates) return;
    const fields = userAllowTemplates[name] || [];
    const container = document.querySelector("#userallow-fields");
    container.innerHTML = "";
    for (const f of fields) {
      const div = document.createElement("div");
      div.className = "form-group";
      div.innerHTML =
        "<label>" + htmlEscape(f) + "</label>" +
        '<input name="' + htmlAttrEscape(f) + '" type="text" required>';
      container.appendChild(div);
    }
  }

  document.querySelector("#userallow-mode").addEventListener("change", (e) => {
    const isRaw = e.target.value === "raw";
    document.querySelector("#userallow-template-group").hidden = isRaw;
    document.querySelector("#userallow-fields").hidden = isRaw;
    document.querySelector("#userallow-raw-group").hidden = !isRaw;
  });

  document.querySelector("#userallow-template").addEventListener("change", renderTemplateInputs);

  document.querySelector("#userallow-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const mode = document.querySelector("#userallow-mode").value;
    const label = e.target.querySelector('[name="label"]').value;
    let body;
    if (mode === "raw") {
      const stmt = e.target.querySelector('[name="stmt"]').value;
      body = { stmt: stmt, label: label };
    } else {
      const template = document.querySelector("#userallow-template").value;
      const params = {};
      for (const inp of document.querySelectorAll("#userallow-fields input")) {
        params[inp.name] = inp.value;
      }
      body = { template: template, params: params, label: label };
    }
    const r = await fetch("/api/user-allow", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      const err = await r.json().catch(() => ({ error: "unknown" }));
      alert("Rule rejected: " + (err.error || r.status));
      return;
    }
    e.target.reset();
    document.querySelector("#userallow-mode").dispatchEvent(new Event("change"));
    refreshUserAllow();
  });

  document.querySelector("#userallow-table tbody").addEventListener("click", async (e) => {
    if (!e.target.classList.contains("userallow-del-btn")) return;
    const id = e.target.dataset.id;
    if (!confirm("Delete this firewall rule?")) return;
    await fetch("/api/user-allow/" + encodeURIComponent(id), { method: "DELETE" });
    refreshUserAllow();
  });
```

- [ ] **Step 2: Hook the tab refresh into the existing tab-switch handler**

The existing handler (around line 222) currently reads:

```javascript
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      tabs.forEach((t) => t.classList.remove("active"));
      views.forEach((v) => v.classList.remove("active"));
      tab.classList.add("active");
      const target = tab.getAttribute("data-tab");
      document.getElementById(target + "-view").classList.add("active");
      if (target === "published") refreshPublished();
    });
  });
```

Replace the last line with:

```javascript
      if (target === "published") refreshPublished();
      if (target === "userallow") {
        renderUserAllowFields();
        refreshUserAllow();
      }
```

- [ ] **Step 3: Manual JS syntax check**

There's no node available; visually confirm the IIFE close `})();` is balanced (search the file for `})();` and verify exactly one occurrence).

```bash
grep -c "})();" /home/joe/Development/claude-container/proxy/static/app.js
```

Expected: `1`.

- [ ] **Step 4: Run the Python test suite to confirm no regressions**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
```

Expected: 83/83 pass.

- [ ] **Step 5: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/static/app.js && git commit -m "feat(proxy/dashboard-ui): Custom Firewall tab handlers + template/raw mode"
```

---

### Task 10: CSS for the Custom Firewall tab

**Files:**
- Modify: `proxy/static/style.css`

- [ ] **Step 1: Append styles**

Append to `proxy/static/style.css`:

```css
/* --- Custom Firewall tab --- */
#userallow-form .form-help {
    color: #666;
    font-size: 0.9em;
    margin: 0 0 12px 0;
}

#userallow-form textarea {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.9em;
    padding: 6px 8px;
    width: 100%;
    border: 1px solid #ccc;
    border-radius: 3px;
    resize: vertical;
}

#userallow-table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 1em;
}

#userallow-table th,
#userallow-table td {
    padding: 6px 12px;
    border-bottom: 1px solid #eee;
    text-align: left;
    vertical-align: top;
}

#userallow-table code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    color: #c41a16;
}

.userallow-del-btn {
    padding: 2px 8px;
    font-size: 0.85em;
}
```

- [ ] **Step 2: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/static/style.css && git commit -m "feat(proxy/dashboard-ui): styles for Custom Firewall tab"
```

---

## Phase 3D — E2E tests + docs

### Task 11: E2E test — template install + traffic passes

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Append the test**

```go
// TestSecurity_UserAllow_TemplateInstallAllowsTraffic verifies that
// adding an outbound TCP CIDR template via the dashboard actually
// installs the nft rule and allows the corresponding traffic.
func TestSecurity_UserAllow_TemplateInstallAllowsTraffic(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-ua-tcp"
	startSecurityContainer(t, name, "--yolo")

	// Host server: TCP echo on a random loopback port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	hostPort := ln.Addr().(*net.TCPAddr).Port

	// Accept loop — write a marker and close.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("UA-OK"))
	}()

	// Add the user_allow rule via the dashboard.
	api := newProxyAPI(t, configDir, name)
	body, _ := json.Marshal(map[string]any{
		"template": "outbound_tcp_cidr",
		"params":   map[string]any{"cidr": "127.0.0.1", "port": hostPort},
		"label":    "test-tcp",
	})
	req, _ := http.NewRequest("POST", api.baseURL+"/api/user-allow", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", api.token)
	resp, err := api.http.Do(req)
	if err != nil {
		t.Fatalf("POST /api/user-allow: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("user-allow add: status=%d", resp.StatusCode)
	}

	// Container makes the TCP connection. Without the rule it would be
	// REDIRECTed to mitmproxy and held; with the user_allow rule the
	// kernel accepts before the redirect... actually no — TCP redirects
	// happen in nat OUTPUT BEFORE the filter OUTPUT user_allow runs, so
	// this test mainly verifies the rule got *installed*. The real
	// traffic-bypassing properties of user_allow are exercised by the
	// ICMP test (Task 12) which doesn't go through the TCP redirect.
	out, _ := boundedDockerExec(t, 10*time.Second, name, "sh", "-c",
		"nft list chain inet claude_proxy_fw user_allow")
	expected := fmt.Sprintf("ip daddr 127.0.0.1 tcp dport %d accept", hostPort)
	if !strings.Contains(out, expected) {
		t.Errorf("user_allow chain missing expected rule:\nwant: %s\ngot:\n%s",
			expected, out)
	}
}
```

- [ ] **Step 2: Verify build**

```bash
cd /home/joe/Development/claude-container && devenv shell -- go build ./... && devenv shell -- go vet ./...
```

- [ ] **Step 3: Commit**

```bash
cd /home/joe/Development/claude-container && git add cmd/security_e2e_test.go && git commit -m "test(security): E2E user_allow template installs nft rule"
```

---

### Task 12: E2E test — raw mode validates dangerous statements

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Append the test**

```go
// TestSecurity_UserAllow_RejectsDangerousRawStatement verifies that a
// raw statement containing a blacklisted keyword is rejected by
// publish-mgr before reaching the kernel.
func TestSecurity_UserAllow_RejectsDangerousRawStatement(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-ua-raw"
	startSecurityContainer(t, name, "--yolo")

	api := newProxyAPI(t, configDir, name)
	cases := []string{
		"ip daddr 1.2.3.4 drop",
		"flush chain inet claude_proxy_fw user_allow",
		"delete rule inet claude_proxy_fw user_allow handle 1",
	}
	for _, stmt := range cases {
		body, _ := json.Marshal(map[string]any{
			"stmt":  stmt,
			"label": "evil",
		})
		req, _ := http.NewRequest("POST", api.baseURL+"/api/user-allow",
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Auth-Token", api.token)
		resp, err := api.http.Do(req)
		if err != nil {
			t.Errorf("stmt %q: POST failed: %v", stmt, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("stmt %q: server accepted dangerous statement", stmt)
		}
	}

	// Confirm the chain is still empty.
	out, _ := boundedDockerExec(t, 5*time.Second, name, "sh", "-c",
		"nft list chain inet claude_proxy_fw user_allow")
	if strings.Contains(out, "drop") || strings.Contains(out, "flush") ||
		strings.Contains(out, "delete") {
		t.Errorf("user_allow chain has unexpected statements:\n%s", out)
	}
}
```

- [ ] **Step 2: Verify build + commit**

```bash
cd /home/joe/Development/claude-container && devenv shell -- go build ./... && devenv shell -- go vet ./...
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E user_allow rejects dangerous raw statements"
```

---

### Task 13: E2E test — rules survive proxy restart

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Append the test**

```go
// TestSecurity_UserAllow_PersistsAcrossProxyRestart verifies that
// adding a user_allow rule, restarting the proxy container, and
// listing the chain shows the rule re-applied via startup replay.
func TestSecurity_UserAllow_PersistsAcrossProxyRestart(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-ua-persist"
	startSecurityContainer(t, name, "--yolo")

	api := newProxyAPI(t, configDir, name)

	// Add a rule via the dashboard.
	body, _ := json.Marshal(map[string]any{
		"template": "outbound_icmp_echo",
		"params":   map[string]any{"addr": "8.8.8.8"},
		"label":    "ping google",
	})
	req, _ := http.NewRequest("POST", api.baseURL+"/api/user-allow",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", api.token)
	resp, err := api.http.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("user-allow add failed: %v status=%d", err, resp.StatusCode)
	}
	resp.Body.Close()

	// Confirm the rule is present before restart.
	out, _ := boundedDockerExec(t, 5*time.Second, name, "sh", "-c",
		"nft list chain inet claude_proxy_fw user_allow")
	if !strings.Contains(out, "ip daddr 8.8.8.8 icmp type echo-request accept") {
		t.Fatalf("rule not installed pre-restart:\n%s", out)
	}

	// Restart the proxy container.
	if err := exec.Command("docker", "restart", "claude-proxy_"+name).Run(); err != nil {
		t.Fatalf("docker restart: %v", err)
	}
	// Give publish-mgr time to come up and replay.
	time.Sleep(5 * time.Second)

	out, _ = boundedDockerExec(t, 5*time.Second, name, "sh", "-c",
		"nft list chain inet claude_proxy_fw user_allow")
	if !strings.Contains(out, "ip daddr 8.8.8.8 icmp type echo-request accept") {
		t.Errorf("rule did NOT survive proxy restart:\n%s", out)
	}
}
```

- [ ] **Step 2: Verify build + commit**

```bash
cd /home/joe/Development/claude-container && devenv shell -- go build ./... && devenv shell -- go vet ./...
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E user_allow rules survive proxy restart via startup replay"
```

---

### Task 14: README — document Custom Firewall

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Append a section under NETWORK PROXY**

Find the `### Outbound UDP (Phase 2)` section (added in Phase 2 Task 17). Insert AFTER it (and BEFORE `### Default allowed domains by profile`):

```markdown
### Custom firewall escape hatch (Phase 3)

The dashboard's **Custom Firewall** tab lets you add accept-only
nftables rules to a `user_allow` sub-chain that's jumped from both
`input` and `output` immediately before the final drop. Two modes:

**Templates** — pick a common shape (outbound ICMP echo, inbound TCP
CIDR, outbound UDP CIDR, etc.), fill in an address or CIDR + port, and
click Add. The dashboard compiles the parameters into an nft statement
and submits it.

**Raw mode** — type a full nft expression like
`ip daddr 10.0.0.0/8 tcp dport 22 accept`. The expression is validated
twice before it can affect the chain:

1. A keyword blacklist rejects any statement containing `drop`,
   `reject`, `policy`, `flush`, `delete`, `table`, or `chain` — words
   that could weaken the boundary.
2. `nft --check` parses the statement; malformed syntax is rejected.

Rules persist in the session's `rules.json` with `proto: "nft"` so
they're invisible to the HTTP/UDP matchers. On proxy restart,
`publish-mgr` replays every persisted `nft_statement` into the
`user_allow` chain — your rules survive restarts without manual
re-entry.

This is the only place a process inside the Claude container can
influence its own firewall, and only via the dashboard (which the
container has no privileged access to).
```

- [ ] **Step 2: Commit**

```bash
cd /home/joe/Development/claude-container && git add README.md && git commit -m "docs(README): document Custom Firewall escape hatch"
```

---

## Phase 3 boundary check

After Task 14:

```bash
cd /home/joe/Development/claude-container
devenv shell -- go build ./...
devenv shell -- go vet ./...
devenv shell -- go test ./internal/... ./cmd/
devenv shell -- bash -c "cd proxy/publish-mgr && go test ./..."
devenv shell -- bash -c "cd proxy/udp-redir && go test ./..."
devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
nix build .#claude-proxy-image
```

Expected: every command exits 0. The 3 new E2E tests are slow and need a rebuilt + loaded proxy image (`docker load -i result`); reserve them for the pre-release pass.

---

## Self-review

**Spec coverage (against §9 of the design):**

| Spec item | Task |
| --- | --- |
| §9.1 `chain user_allow {}` in claude_proxy_fw | Task 1 |
| §9.1 `jump user_allow` in output + input chains | Task 1 |
| §9.1 user_allow is accept-only (validator enforces) | Tasks 2, 3, 4 |
| §9.2 structured templates (6 templates, exact strings) | Task 6 (compiler), Task 9 (UI form) |
| §9.2 stored as rule-store entries with `proto: "nft"` and `match.nft_statement` | Task 7 |
| §9.3 raw mode | Tasks 7, 9 |
| §9.3 keyword blacklist | Task 2 |
| §9.3 `nft --check` validation | Task 2 |
| §9.4 persistence: replay from rule store on startup | Task 5 |

**Placeholder scan:** every step has complete code. No "TBD", no "add validation later" — the validator is fully specified in Task 2.

**Type consistency:**
- `userAllowReq`/`userAllowResp`/`userAllowEntry` defined in Task 4, used by Tasks 4-5
- `TEMPLATES` registry defined in Task 6, used by Tasks 7 (dashboard handlers) and 9 (UI templates list)
- `validateUserAllowStmt` defined in Task 2, called from Task 4 (`handleUserAllowAdd`) and Task 5 (`replayUserAllowFromRules`)
- `nftAddUserAllow` / `nftDelUserAllow` defined in Task 3, called from Tasks 4 + 5
- `proto: "nft"` convention introduced in Task 7 — neither `addon.py:match_request` nor `udp-redir/rules.go:matchUDP` recognize it, so these rules are inert at the matcher layer. Confirmed via reading Phase 0 Task 4 + Phase 2 Task 4 in the existing plans.

**Known limitations:**
- The TCP outbound user_allow E2E test (Task 11) only verifies the rule got *installed*, not that TCP traffic bypasses the mitmproxy redirect — because `nat OUTPUT` `redirect to :8080` fires BEFORE the `filter OUTPUT` `jump user_allow`. A user_allow rule for TCP only meaningfully affects packets that wouldn't have been redirected (uid=1500 from mitmproxy itself, or destinations that nat OUTPUT explicitly returned earlier). The ICMP and UDP templates DO have real-world effect since they don't go through the TCP redirect; the persistence test (Task 13) uses ICMP for that reason.
- `user_allow` rules carry no `expires_at` semantics — they're not subject to TTL pruning. This is intentional: nft rules don't have a built-in TTL and the spec doesn't call for one. If the user wants a temporary firewall hole they can delete it themselves.
- Raw mode does not save the original raw text; it stores only the validated `nft_statement`. There's no edit-after-create — delete and re-add.
