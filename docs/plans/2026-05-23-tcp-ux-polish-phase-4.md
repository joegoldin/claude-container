# Phase 4 — TCP UX Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dashboard's **Rules** tab usable as a full traffic-management surface: group rules by protocol family, surface live per-rule counters (bytes / packets) for the nft-managed rules from Phases 1 and 3, and add filter + sort. Also fix a latent regression from Phase 0 where the Rules tab reads `rule_type` / `pattern` which are no longer in the JSON response.

**Architecture:** Counters are wired in two places — `publish-mgr` rewrites its `nftAddInputAccept` (Phase 1) and `nftAddUserAllow` (Phase 3) helpers to emit `counter accept` instead of bare `accept`, then exposes `/counters` on its Unix socket. The dashboard's `/api/counters` proxies to that socket; the Rules tab polls every 5 seconds and shows packet/byte numbers per row for nft-managed rules. Non-nft rules (HTTP/TCP/UDP regex matchers handled by mitmproxy / udp-redir) get no counters in this phase — those would need separate addon-level instrumentation. The Rules tab markup is restructured to group rows by protocol family (`http`, `tcp`, `udp`, `icmp`, `nft`, `any`) with section headers, plus a text filter and a sort dropdown.

**Tech Stack:** Go (publish-mgr counter parsing + endpoint), Python Starlette (dashboard proxy), vanilla JS (Rules tab UI), nftables.

**Spec reference:** `docs/plans/2026-05-22-dashboard-all-traffic-design.md` §10.

**Out of scope (future work):**
- "Deny all hosts not seen in 7d" bulk action — needs persistent per-rule last-seen tracking we don't currently maintain.
- `conntrack -L` open-connection count per rule — requires correlating L4 5-tuples to rule patterns, brittle and noisy.
- Counters for HTTP/TCP/UDP regex rules — would need mitmproxy/udp-redir addon instrumentation. The current implementation only counts what nft handles directly (`publish_in` + `user_allow`).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `proxy/publish-mgr/userallow.go` | `nftAddUserAllow` adds `counter` before `accept` |
| `proxy/publish-mgr/main.go` | `nftAddInputAccept` adds `counter`; new `/counters` endpoint |
| `proxy/publish-mgr/counters.go` | New file: parse `nft -a list chain` output for counter values |
| `proxy/publish-mgr/counters_test.go` | Unit tests for the parser |
| `proxy/claude_proxy/dashboard.py` | New `/api/counters` endpoint that proxies to publish-mgr's socket |
| `proxy/tests/test_dashboard.py` | Test for the new endpoint |
| `proxy/static/app.js` | Migrate `renderRules` to new schema; group-by-proto, filter, sort; counter polling |
| `proxy/static/index.html` | Add filter input + sort dropdown above the rules table |
| `proxy/static/style.css` | Section header + counter cell styling |
| `cmd/security_e2e_test.go` | E2E: counter increments after traffic |
| `README.md` | Document the Rules tab features |

---

## Phase 4A — backend counter wiring

### Task 1: `publish-mgr` emits `counter` on every nft accept rule

**Files:**
- Modify: `proxy/publish-mgr/main.go` (`nftAddInputAccept`)
- Modify: `proxy/publish-mgr/userallow.go` (`nftAddUserAllow`)

The current helpers run `nft add rule ... <proto> dport <port> accept`. After this task they produce `nft add rule ... <proto> dport <port> counter accept`, so each rule gets its own packet+byte counter.

- [ ] **Step 1: Modify `nftAddInputAccept` in `proxy/publish-mgr/main.go`**

The current function in `main.go` reads:

```go
func nftAddInputAccept(proto string, port int) error {
	cmd := exec.Command("nft", "add", "rule", "inet", "claude_proxy_fw",
		"publish_in", proto, "dport", strconv.Itoa(port), "accept")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft add rule failed: %v: %s", err, out)
	}
	return nil
}
```

Replace with:

```go
func nftAddInputAccept(proto string, port int) error {
	cmd := exec.Command("nft", "add", "rule", "inet", "claude_proxy_fw",
		"publish_in", proto, "dport", strconv.Itoa(port),
		"counter", "accept")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft add rule failed: %v: %s", err, out)
	}
	return nil
}
```

(Note: also update the matching delete helper `nftDelInputAccept`'s needle to include `counter`. The current needle is `fmt.Sprintf("%s dport %d accept # handle ", proto, port)`. Change to `fmt.Sprintf("%s dport %d counter packets %%d bytes %%d accept # handle ", proto, port)` — but counter values vary, so use a more flexible match. Simplest fix: drop the strict needle and match on `<proto> dport <port>` + ` # handle ` only.)

The current `nftDelInputAccept`:

```go
needle := fmt.Sprintf("%s dport %d accept # handle ", proto, port)
```

Replace with:

```go
// Match by proto/port + handle suffix. The middle (counter and accept)
// can vary depending on whether the rule was added with or without a
// counter statement.
needle := fmt.Sprintf("%s dport %d ", proto, port)
handleMarker := " # handle "
```

And update the loop body so the line must contain both substrings:

```go
for _, line := range strings.Split(string(out), "\n") {
    if !strings.Contains(line, needle) || !strings.Contains(line, handleMarker) {
        continue
    }
    i := strings.LastIndex(line, "handle ")
    // ... rest unchanged
```

- [ ] **Step 2: Modify `nftAddUserAllow` in `proxy/publish-mgr/userallow.go`**

The current function reads:

```go
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
```

Replace with:

```go
func nftAddUserAllow(stmt string) error {
	// Insert "counter" before the final "accept" verdict so every
	// user_allow rule accumulates its own packet+byte counter. If the
	// statement doesn't end in `accept` (defensive — the validator
	// should have rejected it), fall through and let nft reject it.
	stmt = strings.TrimSpace(stmt)
	if strings.HasSuffix(stmt, " accept") {
		stmt = strings.TrimSuffix(stmt, " accept") + " counter accept"
	}
	args := []string{"add", "rule", "inet", "claude_proxy_fw", "user_allow"}
	args = append(args, strings.Fields(stmt)...)
	cmd := exec.Command("nft", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft add user_allow rule failed: %v: %s", err, out)
	}
	return nil
}
```

- [ ] **Step 3: Update `nftDelUserAllow` needle to be counter-tolerant**

The current function uses:

```go
needle := strings.TrimSpace(stmt) + " # handle "
```

The problem: after Step 2, the actual rule line in `nft list` contains `counter packets X bytes Y accept` instead of `accept`. The needle won't match. Replace with:

```go
// Strip the trailing "accept" so the needle tolerates "counter
// packets N bytes M accept" between the rule body and "# handle".
stmtNoAccept := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stmt), " accept"))
handleMarker := " # handle "
```

And update the loop to require both:

```go
for _, line := range strings.Split(string(out), "\n") {
    if !strings.Contains(line, stmtNoAccept) || !strings.Contains(line, handleMarker) {
        continue
    }
    // ... rest unchanged
```

- [ ] **Step 4: Verify all tests still pass**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./... && go test ./..."
```

Expected: clean build, 4 validator tests still pass. (No new tests in this task; counter parsing is Task 2.)

- [ ] **Step 5: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/main.go proxy/publish-mgr/userallow.go && git commit -m "feat(publish-mgr): emit counter on every publish_in and user_allow rule"
```

---

### Task 2: `nft list` counter parser

**Files:**
- Create: `proxy/publish-mgr/counters.go`
- Create: `proxy/publish-mgr/counters_test.go`

- [ ] **Step 1: Write the failing test**

`proxy/publish-mgr/counters_test.go`:

```go
package main

import "testing"

// Sample output from `nft -a list chain inet claude_proxy_fw user_allow`,
// trimmed to the chain body.
const sampleUserAllowChain = `table inet claude_proxy_fw {
	chain user_allow { # handle 9
		ip daddr 8.8.8.8 udp dport 53 counter packets 7 bytes 462 accept # handle 12
		ip daddr 1.1.1.1 tcp dport 443 counter packets 0 bytes 0 accept # handle 13
		ip daddr 10.0.0.0/8 icmp type echo-request counter packets 3 bytes 252 accept # handle 14
	}
}`

func TestParseCounters_ExtractsAllRules(t *testing.T) {
	got := parseCounters(sampleUserAllowChain)
	if len(got) != 3 {
		t.Fatalf("got %d counters, want 3: %+v", len(got), got)
	}
	if got[0].Handle != "12" {
		t.Errorf("got[0].Handle=%q, want 12", got[0].Handle)
	}
	if got[0].Packets != 7 || got[0].Bytes != 462 {
		t.Errorf("got[0]=%+v, want packets=7 bytes=462", got[0])
	}
	if got[1].Handle != "13" {
		t.Errorf("got[1].Handle=%q, want 13", got[1].Handle)
	}
	if got[1].Packets != 0 || got[1].Bytes != 0 {
		t.Errorf("got[1] packets/bytes not zero: %+v", got[1])
	}
}

func TestParseCounters_ExtractsStatement(t *testing.T) {
	got := parseCounters(sampleUserAllowChain)
	if got[0].Stmt != "ip daddr 8.8.8.8 udp dport 53 accept" {
		t.Errorf("got[0].Stmt=%q, want canonical form without counter", got[0].Stmt)
	}
}

func TestParseCounters_IgnoresLinesWithoutCounter(t *testing.T) {
	chain := `table inet claude_proxy_fw {
	chain other { # handle 1
		ip daddr 1.2.3.4 accept # handle 2
	}
}`
	if got := parseCounters(chain); len(got) != 0 {
		t.Errorf("expected 0 (no counter on these lines), got %+v", got)
	}
}

func TestParseCounters_EmptyInput(t *testing.T) {
	if got := parseCounters(""); len(got) != 0 {
		t.Errorf("expected 0, got %+v", got)
	}
}
```

- [ ] **Step 2: Verify it fails**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go test -run TestParseCounters ./..."
```

Expected: FAIL — `parseCounters` undefined.

- [ ] **Step 3: Write the parser**

`proxy/publish-mgr/counters.go`:

```go
package main

import (
	"regexp"
	"strconv"
	"strings"
)

// CounterEntry is one rule's counter data, returned by /counters.
// `Stmt` is the rule body with the "counter packets N bytes M"
// fragment elided, so the dashboard can match it against the
// nft_statement field in the rule store.
type CounterEntry struct {
	Handle  string `json:"handle"`
	Stmt    string `json:"stmt"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// counterLineRE matches a single rule line like:
//   "        ip daddr 8.8.8.8 udp dport 53 counter packets 7 bytes 462 accept # handle 12"
// and captures the body before counter, the packets/bytes values, the
// verdict after, and the handle.
var counterLineRE = regexp.MustCompile(
	`^\s+(.+?)\s+counter packets (\d+) bytes (\d+)\s+(.+?)\s+#\s+handle\s+(\d+)\s*$`,
)

// parseCounters reads the text output of `nft -a list chain ...` and
// returns one CounterEntry per rule that has a `counter` statement.
// Lines without `counter` are ignored.
func parseCounters(out string) []CounterEntry {
	var entries []CounterEntry
	for _, line := range strings.Split(out, "\n") {
		m := counterLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		body := strings.TrimSpace(m[1])
		verdict := strings.TrimSpace(m[4])
		pkts, _ := strconv.ParseUint(m[2], 10, 64)
		byts, _ := strconv.ParseUint(m[3], 10, 64)
		entries = append(entries, CounterEntry{
			Handle:  strings.TrimSpace(m[5]),
			Stmt:    body + " " + verdict,
			Packets: pkts,
			Bytes:   byts,
		})
	}
	return entries
}
```

- [ ] **Step 4: Verify it passes**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go test -run TestParseCounters ./..."
```

Expected: PASS (4 tests).

- [ ] **Step 5: Confirm full package still builds + tests pass**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./... && go test ./..."
```

Expected: clean build, 4 validator + 4 counter = 8 tests pass.

- [ ] **Step 6: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/counters.go proxy/publish-mgr/counters_test.go && git commit -m "feat(publish-mgr): parse nft list output for per-rule counters"
```

---

### Task 3: `/counters` endpoint

**Files:**
- Modify: `proxy/publish-mgr/main.go`

- [ ] **Step 1: Add the handler**

Append to `proxy/publish-mgr/main.go` (after the existing user_allow handlers):

```go
type countersResp struct {
	PublishIn []CounterEntry `json:"publish_in"`
	UserAllow []CounterEntry `json:"user_allow"`
}

func (m *manager) handleCounters(w http.ResponseWriter, _ *http.Request) {
	out := countersResp{
		PublishIn: listChainCounters("publish_in"),
		UserAllow: listChainCounters("user_allow"),
	}
	writeJSON(w, 200, out)
}

func listChainCounters(chain string) []CounterEntry {
	raw, err := exec.Command("nft", "-a", "list", "chain", "inet",
		"claude_proxy_fw", chain).CombinedOutput()
	if err != nil {
		// On error return empty; the dashboard will surface zero
		// counters instead of failing the whole request.
		return nil
	}
	return parseCounters(string(raw))
}
```

- [ ] **Step 2: Register the route**

In `main()`, the existing user-allow route registrations look like:

```go
mux.HandleFunc("/user-allow/add", mgr.handleUserAllowAdd)
mux.HandleFunc("/user-allow/del", mgr.handleUserAllowDel)
mux.HandleFunc("/user-allow/list", mgr.handleUserAllowList)
```

Add after them:

```go
mux.HandleFunc("/counters", mgr.handleCounters)
```

- [ ] **Step 3: Build + tests**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy/publish-mgr && go build ./... && go test ./..."
```

Expected: clean, 8 tests still pass.

- [ ] **Step 4: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/publish-mgr/main.go && git commit -m "feat(publish-mgr): /counters endpoint exposing publish_in + user_allow counts"
```

---

## Phase 4B — dashboard + UI

### Task 4: Dashboard `/api/counters` proxy

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py`
- Modify: `proxy/tests/test_dashboard.py`

- [ ] **Step 1: Write the failing test**

Append to `proxy/tests/test_dashboard.py`:

```python
def test_counters_endpoint_proxies_to_publish_mgr(client, monkeypatch):
    """GET /api/counters returns publish-mgr's counter snapshot verbatim."""
    import httpx
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            assert request.url.path == "/counters"
            return httpx.Response(200, json={
                "publish_in": [
                    {"handle": "12", "stmt": "tcp dport 3000 accept",
                     "packets": 5, "bytes": 1024},
                ],
                "user_allow": [
                    {"handle": "20", "stmt": "ip daddr 8.8.8.8 udp dport 53 accept",
                     "packets": 3, "bytes": 252},
                ],
            })

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport", FakeTransport())

    resp = client.get("/api/counters")
    assert resp.status_code == 200
    data = resp.json()
    assert data["publish_in"][0]["packets"] == 5
    assert data["user_allow"][0]["bytes"] == 252


def test_counters_endpoint_returns_empty_when_publish_mgr_down(client, monkeypatch):
    """If the socket is unreachable, the endpoint returns empty arrays (not a 5xx)."""
    import httpx
    class BrokenTransport(httpx.BaseTransport):
        def handle_request(self, request):
            raise httpx.ConnectError("socket missing")

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport", BrokenTransport())

    resp = client.get("/api/counters")
    assert resp.status_code == 200
    assert resp.json() == {"publish_in": [], "user_allow": []}
```

- [ ] **Step 2: Verify it fails**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py::test_counters_endpoint_proxies_to_publish_mgr -v"
```

Expected: FAIL — endpoint doesn't exist.

- [ ] **Step 3: Add the handler + route**

In `proxy/claude_proxy/dashboard.py`, add a handler near `user_allow_list`:

```python
async def counters(request: Request) -> JSONResponse:
    """Return per-rule counters from publish-mgr.

    Failure soft: if publish-mgr is unreachable, return empty arrays
    instead of a 5xx so the dashboard UI keeps rendering rules.
    """
    try:
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.get("http://publish-mgr/counters", timeout=2)
        return JSONResponse(r.json(), status_code=r.status_code)
    except Exception as exc:
        logger.debug("publish-mgr /counters unreachable: %s", exc)
        return JSONResponse({"publish_in": [], "user_allow": []})
```

In the `routes = [...]` list, add:

```python
    Route("/api/counters", counters, methods=["GET"]),
```

- [ ] **Step 4: Run tests**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/test_dashboard.py -q"
```

Expected: 28/28 pass (26 existing + 2 new).

- [ ] **Step 5: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/claude_proxy/dashboard.py proxy/tests/test_dashboard.py && git commit -m "feat(proxy/dashboard): /api/counters proxies to publish-mgr"
```

---

### Task 5: Fix the Rules tab — migrate to new rule schema

**Files:**
- Modify: `proxy/static/app.js`

The Rules tab currently reads `rule.rule_type` and `rule.pattern` — fields that Phase 0 removed from `to_dict`. The result: the Rules tab renders empty Type / Pattern columns. This task fixes that.

- [ ] **Step 1: Replace `renderRules` body**

The current function in `proxy/static/app.js` (around line 480) reads:

```javascript
  function renderRules() {
    if (rules.length === 0) {
      rulesBody.innerHTML =
        '<tr class="empty-row"><td colspan="6">No rules configured.</td></tr>';
      return;
    }

    rulesBody.innerHTML = "";
    rules.forEach((rule) => {
      const tr = document.createElement("tr");
      const expiresStr = rule.expires_at
        ? new Date(rule.expires_at * 1000).toLocaleString()
        : "Never";

      tr.innerHTML = `
        <td><span class="rule-type ${rule.rule_type}">${htmlEscape(rule.rule_type)}</span></td>
        <td class="rule-pattern" title="${htmlAttrEscape(rule.pattern)}">${htmlEscape(rule.pattern)}</td>
        <td>${htmlEscape(rule.label || "")}</td>
        <td>${expiresStr}</td>
        <td>${htmlEscape(rule.source || "interactive")}</td>
        <td><button class="btn-delete" data-rule-id="${rule.id}">Delete</button></td>
      `;

      tr.querySelector(".btn-delete").addEventListener("click", async () => {
        try {
          const resp = await fetch("/api/rules/" + rule.id, {
            method: "DELETE",
          });
          if (resp.ok) {
            rules = rules.filter((r) => r.id !== rule.id);
            renderRules();
          }
        } catch (err) {
          console.error("Failed to delete rule:", err);
        }
      });

      rulesBody.appendChild(tr);
    });
  }
```

Replace ENTIRELY with:

```javascript
  // Render a single rule row. Used by both renderRules and the grouped
  // renderer in Task 6. Returns a <tr> element.
  function makeRuleRow(rule) {
    const tr = document.createElement("tr");
    tr.setAttribute("data-rule-id", rule.id);
    const expiresStr = rule.expires_at
      ? new Date(rule.expires_at * 1000).toLocaleString()
      : "Never";
    const action = rule.action || "allow";
    const proto = rule.proto || "any";
    const summary = ruleSummary(rule);
    tr.innerHTML =
      '<td><span class="rule-type ' + htmlAttrEscape(action) + '">' +
        htmlEscape(action) + '</span></td>' +
      '<td><span class="rule-proto">' + htmlEscape(proto) + '</span></td>' +
      '<td class="rule-pattern" title="' + htmlAttrEscape(summary) + '">' +
        htmlEscape(summary) + '</td>' +
      '<td class="rule-counter" data-rule-counter="' +
        htmlAttrEscape(rule.id) + '">—</td>' +
      '<td>' + htmlEscape(rule.label || "") + '</td>' +
      '<td>' + htmlEscape(expiresStr) + '</td>' +
      '<td>' + htmlEscape(rule.source || "interactive") + '</td>' +
      '<td><button class="btn-delete" data-rule-id="' +
        htmlAttrEscape(rule.id) + '">Delete</button></td>';
    tr.querySelector(".btn-delete").addEventListener("click", async () => {
      try {
        const resp = await fetch("/api/rules/" + rule.id, { method: "DELETE" });
        if (resp.ok) {
          rules = rules.filter((r) => r.id !== rule.id);
          renderRules();
        }
      } catch (err) {
        console.error("Failed to delete rule:", err);
      }
    });
    return tr;
  }

  // ruleSummary returns a human-readable one-liner describing what the
  // rule matches. Inspects rule.match for the canonical fields the
  // Python rule store emits.
  function ruleSummary(rule) {
    const m = rule.match || {};
    if (m.nft_statement) return m.nft_statement;
    if (m.host) return m.host;
    if (m.host_regex) return m.host_regex;
    if (m.port) return "port " + m.port;
    if (m.dns_name) return "dns " + m.dns_name;
    return "(any)";
  }

  function renderRules() {
    if (rules.length === 0) {
      rulesBody.innerHTML =
        '<tr class="empty-row"><td colspan="8">No rules configured.</td></tr>';
      return;
    }
    rulesBody.innerHTML = "";
    rules.forEach((rule) => rulesBody.appendChild(makeRuleRow(rule)));
  }
```

- [ ] **Step 2: Add 2 more table-header columns in `index.html`**

The current `<thead>` block reads:

```html
                    <thead>
                        <tr>
                            <th>Type</th>
                            <th>Pattern</th>
                            <th>Label</th>
                            <th>Expires</th>
                            <th>Source</th>
                            <th></th>
                        </tr>
                    </thead>
```

Replace with:

```html
                    <thead>
                        <tr>
                            <th>Action</th>
                            <th>Proto</th>
                            <th>Match</th>
                            <th>Pkts/Bytes</th>
                            <th>Label</th>
                            <th>Expires</th>
                            <th>Source</th>
                            <th></th>
                        </tr>
                    </thead>
```

And update the empty-state row:

```html
                    <tbody id="rules-body">
                        <tr class="empty-row">
                            <td colspan="8">No rules configured.</td>
                        </tr>
                    </tbody>
```

- [ ] **Step 3: Verify Python tests still pass**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
```

Expected: 89/89 (87 pre-existing post Phase 3 + 2 new from Task 4).

- [ ] **Step 4: Verify IIFE still balanced**

```bash
grep -c "})();" /home/joe/Development/claude-container/proxy/static/app.js
```

Expected: `1`.

- [ ] **Step 5: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/static/app.js proxy/static/index.html && git commit -m "fix(proxy/dashboard-ui): Rules tab reads new schema (action/proto/match)"
```

---

### Task 6: Group rules by protocol family

**Files:**
- Modify: `proxy/static/app.js`

- [ ] **Step 1: Replace `renderRules` (again) with the grouping variant**

In `proxy/static/app.js`, replace the `renderRules` body (the one written in Task 5) with:

```javascript
  // Protocol family order — defines section ordering in the rules table.
  const PROTO_ORDER = ["http", "tcp", "udp", "icmp", "nft", "any"];

  function renderRules() {
    if (rules.length === 0) {
      rulesBody.innerHTML =
        '<tr class="empty-row"><td colspan="8">No rules configured.</td></tr>';
      return;
    }
    rulesBody.innerHTML = "";

    // Apply current filter + sort.
    const filtered = filterAndSortRules(rules);
    if (filtered.length === 0) {
      rulesBody.innerHTML =
        '<tr class="empty-row"><td colspan="8">No rules match the current filter.</td></tr>';
      return;
    }

    // Group by proto. Unknown protocols collapse into "any".
    const groups = {};
    for (const r of filtered) {
      const p = PROTO_ORDER.indexOf(r.proto || "any") >= 0 ? (r.proto || "any") : "any";
      (groups[p] = groups[p] || []).push(r);
    }
    for (const proto of PROTO_ORDER) {
      const items = groups[proto];
      if (!items || items.length === 0) continue;
      const header = document.createElement("tr");
      header.className = "rules-group-header";
      header.innerHTML =
        '<td colspan="8">' + htmlEscape(proto.toUpperCase()) +
        ' <span class="rules-group-count">(' + items.length + ')</span></td>';
      rulesBody.appendChild(header);
      for (const rule of items) rulesBody.appendChild(makeRuleRow(rule));
    }
  }

  function filterAndSortRules(rs) {
    const filter = (document.getElementById("rules-filter") || {}).value || "";
    const sort = (document.getElementById("rules-sort") || {}).value || "proto";
    const needle = filter.toLowerCase();
    let out = rs.slice();
    if (needle) {
      out = out.filter((r) => {
        const hay = ((r.label || "") + " " + ruleSummary(r) + " " +
                     (r.action || "") + " " + (r.proto || "")).toLowerCase();
        return hay.indexOf(needle) >= 0;
      });
    }
    if (sort === "label") {
      out.sort((a, b) => (a.label || "").localeCompare(b.label || ""));
    } else if (sort === "created") {
      out.sort((a, b) => (b.created_at || 0) - (a.created_at || 0));
    } else {
      // default: proto, then label
      out.sort((a, b) => {
        const ap = PROTO_ORDER.indexOf(a.proto || "any");
        const bp = PROTO_ORDER.indexOf(b.proto || "any");
        if (ap !== bp) return ap - bp;
        return (a.label || "").localeCompare(b.label || "");
      });
    }
    return out;
  }
```

- [ ] **Step 2: Add the filter + sort controls in `index.html`**

In the Rules tab `<section id="rules-view" class="view">`, just BEFORE the `<div id="rules-table-wrap">` line, insert:

```html
            <div class="rules-controls">
                <input type="text" id="rules-filter" placeholder="Filter rules...">
                <select id="rules-sort">
                    <option value="proto">Sort: Protocol</option>
                    <option value="label">Sort: Label</option>
                    <option value="created">Sort: Newest</option>
                </select>
            </div>
```

- [ ] **Step 3: Wire the controls to re-render**

In `proxy/static/app.js`, just after the renderRules + filterAndSortRules definitions added in Step 1, add:

```javascript
  const rulesFilterEl = document.getElementById("rules-filter");
  if (rulesFilterEl) rulesFilterEl.addEventListener("input", renderRules);
  const rulesSortEl = document.getElementById("rules-sort");
  if (rulesSortEl) rulesSortEl.addEventListener("change", renderRules);
```

- [ ] **Step 4: Verify Python tests still pass**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
```

Expected: 89/89 still pass.

- [ ] **Step 5: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/static/app.js proxy/static/index.html && git commit -m "feat(proxy/dashboard-ui): group rules by proto family + filter + sort"
```

---

### Task 7: Counter polling

**Files:**
- Modify: `proxy/static/app.js`

- [ ] **Step 1: Append the polling block**

Inside the existing IIFE in `proxy/static/app.js`, before the closing `})();`, append:

```javascript
  // --- Rule counters polling ---
  // Poll publish-mgr's counter snapshot every 5 seconds and update the
  // "Pkts/Bytes" cells of nft-managed rules in the Rules tab. Non-nft
  // rules (HTTP/TCP/UDP regex) have no live counter in this phase.
  async function refreshRuleCounters() {
    let snap;
    try {
      const r = await fetch("/api/counters");
      snap = await r.json();
    } catch (err) {
      return; // soft-fail
    }
    const allEntries = (snap.user_allow || []).concat(snap.publish_in || []);
    // Build a map: stmt → "packets/bytes". Stmt comparison is exact
    // because the publish-mgr parser elides the counter fragment for us.
    const byStmt = {};
    for (const e of allEntries) {
      byStmt[e.stmt] = formatPktBytes(e.packets, e.bytes);
    }
    for (const rule of rules) {
      const cell = document.querySelector(
        '[data-rule-counter="' + cssEscape(rule.id) + '"]');
      if (!cell) continue;
      const m = rule.match || {};
      const stmt = m.nft_statement;
      if (stmt) {
        cell.textContent = byStmt[stmt] || "0 / 0";
      } else {
        cell.textContent = "—";
      }
    }
  }

  function formatPktBytes(pkts, bytes) {
    return (pkts || 0) + " / " + formatBytes(bytes || 0);
  }

  function formatBytes(b) {
    if (b < 1024) return b + " B";
    if (b < 1024 * 1024) return (b / 1024).toFixed(1) + " KiB";
    return (b / 1024 / 1024).toFixed(1) + " MiB";
  }

  // CSS.escape may not be available in very old browsers; provide a
  // minimal fallback that escapes the few characters that appear in
  // our rule ids (UUIDs use [0-9a-f-]).
  function cssEscape(s) {
    if (typeof CSS !== "undefined" && CSS.escape) return CSS.escape(s);
    return String(s).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  }

  setInterval(refreshRuleCounters, 5000);
```

- [ ] **Step 2: Run an initial poll when the Rules tab opens**

The existing tab-switch handler (post-Phase 3) currently reads:

```javascript
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      tabs.forEach((t) => t.classList.remove("active"));
      views.forEach((v) => v.classList.remove("active"));
      tab.classList.add("active");
      const target = tab.getAttribute("data-tab");
      document.getElementById(target + "-view").classList.add("active");
      if (target === "published") refreshPublished();
      if (target === "userallow") {
        renderUserAllowFields();
        refreshUserAllow();
      }
    });
  });
```

Add one more conditional:

```javascript
      if (target === "rules") refreshRuleCounters();
```

So the block becomes:

```javascript
      if (target === "published") refreshPublished();
      if (target === "userallow") {
        renderUserAllowFields();
        refreshUserAllow();
      }
      if (target === "rules") refreshRuleCounters();
```

- [ ] **Step 3: IIFE balance check**

```bash
grep -c "})();" /home/joe/Development/claude-container/proxy/static/app.js
```

Expected: `1`.

- [ ] **Step 4: Python tests sanity**

```bash
cd /home/joe/Development/claude-container && devenv shell -- bash -c "cd proxy && .venv-tmp/bin/python -m pytest tests/ -q"
```

Expected: 89/89 still pass.

- [ ] **Step 5: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/static/app.js && git commit -m "feat(proxy/dashboard-ui): live counter polling for nft-managed rules"
```

---

### Task 8: Rules tab CSS

**Files:**
- Modify: `proxy/static/style.css`

- [ ] **Step 1: Append styles**

Append to `proxy/static/style.css`:

```css
/* --- Rules tab polish (Phase 4) --- */
.rules-controls {
    display: flex;
    gap: 0.5em;
    margin-bottom: 0.5em;
}

.rules-controls input[type="text"] {
    flex: 1;
    padding: 4px 8px;
    border: 1px solid #ccc;
    border-radius: 3px;
    font-size: 0.9em;
}

.rules-controls select {
    padding: 4px 8px;
    border: 1px solid #ccc;
    border-radius: 3px;
    font-size: 0.9em;
}

.rules-group-header td {
    background: #f5f5f7;
    font-weight: 600;
    font-size: 0.85em;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: #555;
    padding: 8px 12px;
    border-bottom: 1px solid #ddd;
    border-top: 1px solid #eaeaea;
}

.rules-group-count {
    color: #999;
    font-weight: 400;
    font-size: 0.9em;
}

.rule-proto {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    background: #e8e8ea;
    padding: 1px 6px;
    border-radius: 3px;
    color: #444;
}

.rule-counter {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    color: #666;
    white-space: nowrap;
}
```

- [ ] **Step 2: Commit**

```bash
cd /home/joe/Development/claude-container && git add proxy/static/style.css && git commit -m "feat(proxy/dashboard-ui): styles for grouped rules + counters"
```

---

## Phase 4C — E2E test + docs

### Task 9: E2E — counter increments after traffic

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Append the test**

```go
// TestSecurity_Counters_TickOnUserAllowTraffic verifies that a
// user_allow rule's counter increments when matching traffic flows
// through the chain.
func TestSecurity_Counters_TickOnUserAllowTraffic(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-counters"
	startSecurityContainer(t, name, "--yolo")

	api := newProxyAPI(t, configDir, name)

	// Install an outbound ICMP rule for 127.0.0.1.
	body, _ := json.Marshal(map[string]any{
		"template": "outbound_icmp_echo",
		"params":   map[string]any{"addr": "127.0.0.1"},
		"label":    "loopback ping",
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

	// Trigger an ICMP echo from the container.
	boundedDockerExec(t, 5*time.Second, name, "sh", "-c",
		"ping -c 2 -W 1 127.0.0.1 >/dev/null 2>&1 || true")

	time.Sleep(500 * time.Millisecond)

	// Read counters.
	cresp, err := http.Get(api.baseURL + "/api/counters")
	if err != nil {
		t.Fatalf("GET /api/counters: %v", err)
	}
	defer cresp.Body.Close()
	var snap struct {
		PublishIn []map[string]any `json:"publish_in"`
		UserAllow []map[string]any `json:"user_allow"`
	}
	json.NewDecoder(cresp.Body).Decode(&snap)

	found := false
	for _, e := range snap.UserAllow {
		stmt, _ := e["stmt"].(string)
		pkts, _ := e["packets"].(float64)
		if strings.Contains(stmt, "127.0.0.1") && pkts > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected non-zero counter for loopback ICMP rule, got: %+v",
			snap.UserAllow)
	}
}
```

- [ ] **Step 2: Build + vet**

```bash
cd /home/joe/Development/claude-container && devenv shell -- go build ./... && devenv shell -- go vet ./...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd /home/joe/Development/claude-container && git add cmd/security_e2e_test.go && git commit -m "test(security): E2E counters increment on user_allow traffic"
```

---

### Task 10: README — document the polished Rules tab

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Insert a section AFTER the Phase 3 "Custom firewall escape hatch" and BEFORE "Default allowed domains by profile"**

Find this block in `README.md`:

```markdown
This is the only place a process inside the Claude container can
influence its own firewall, and only via the dashboard (which the
container has no privileged access to).

### Default allowed domains by profile
```

Insert between them:

```markdown
### Rules tab (Phase 4)

The dashboard's **Rules** tab groups rules by protocol family
(HTTP / TCP / UDP / ICMP / nft / any), with a text filter and a sort
selector at the top. Each row shows action, protocol, the match
summary, label, source, and — for rules that nft enforces directly
(inbound publish + custom firewall) — live packet and byte counters
polled every 5 seconds.

HTTP, TCP, and UDP regex rules (the ones mitmproxy and udp-redir
match against URLs) don't have per-rule counters in this release —
they would need addon-level instrumentation. Their rows show `—` in
the counter column. The Custom Firewall and Published Ports rules
both get real numbers.

### Default allowed domains by profile
```

- [ ] **Step 2: Commit**

```bash
cd /home/joe/Development/claude-container && git add README.md && git commit -m "docs(README): document Rules tab grouping, filter, sort, and counters"
```

---

## Phase 4 boundary check

After Task 10:

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

Expected: every command exits 0. The Task 9 E2E test is slow and needs a rebuilt + loaded proxy image; reserve for the pre-release pass.

---

## Self-review

**Spec coverage (against §10 of the design):**

| Spec item | Task | Notes |
| --- | --- | --- |
| Group rules by protocol family | Task 6 | HTTP/TCP/UDP/ICMP/nft/any |
| Live counters per row | Tasks 1, 2, 3, 7 | publish_in + user_allow only |
| Bulk action: "Export current ruleset as preset" | already in Phase 0 | unchanged |
| Bulk action: "Deny all hosts not seen in 7d" | DROPPED | needs last-seen tracking we don't have; documented as future work |
| Sort and filter | Task 6 | by protocol, label, created_at; filter on label/match/proto |
| No new addons, no new daemons | ✅ | publish-mgr gets one new endpoint; nothing else |

**Placeholder scan:** every step has full code. The single intentional "soft fail" path in `refreshRuleCounters` is documented inline (return-on-error).

**Type consistency:**
- `CounterEntry` defined in Task 2; consumed by Task 3 (`countersResp`) and Task 7 (browser JSON parsing — `e.packets`, `e.bytes`, `e.stmt`)
- `nft_statement` field in `rule.match` is the join key between `/api/counters` entries and the rule store (matched by exact string in Task 7's polling code)
- `makeRuleRow` helper defined in Task 5; consumed by Task 6's grouped renderer
- `ruleSummary` helper defined in Task 5; reused in Task 6's filter

**Latent regression fixed:** Phase 0's `to_dict` shape change removed `rule_type` and `pattern`; the existing Rules-tab JS still read them. Task 5 migrates the UI to read `rule.action` / `rule.match.*`. Without this, the existing Rules tab would show empty Action / Pattern columns indefinitely.

**Counter parser caveat:** the parser's regex requires exactly one space between tokens. nft's text output is consistent on this, but if a future nft version changes the format the parser will silently emit no entries. Unit tests cover the current format; integration tests will catch a real regression.

**E2E test caveat (Task 9):** the test uses `ping -c 2 127.0.0.1` from inside the container. The `127.0.0.1` user_allow rule allows the outbound ICMP, but ICMP also needs an inbound rule for the echo-reply. In practice `ct state established,related accept` in the input chain handles the reply. If the rule installation succeeds but ping fails (e.g. iputils missing in the image), the test would falsely fail. The implementer should swap to a `--privileged`-tolerant probe if ping doesn't exist; check `which ping` inside the container first.
