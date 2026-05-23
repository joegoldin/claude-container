# Dashboard All-Traffic Control — Phase 0 + Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the unified rule schema (Phase 0) and dynamic dashboard-managed inbound port publishing for TCP + UDP (Phase 1). After this plan: the proxy's dashboard becomes the live source of truth for outbound *and* inbound traffic at the protocol level, with per-session port-range allocation so multiple concurrent sessions don't collide.

**Architecture:** A unified Rule schema (`direction` + `proto` + structured `match` object + `action`) replaces the flat regex-pattern model, with backward-compatible normalization at load. A host-side `PortAllocator` claims a 10-port range per session before the proxy starts; the proxy container publishes that range on both TCP and UDP, but firewall-default-denies unassigned ports. A new `publish-mgr` daemon inside the proxy container owns the dynamic nft INPUT rules and exposes a Unix-socket API; the dashboard proxies to it via new `/api/publish` endpoints.

**Tech Stack:** Python 3.13 + Starlette/Uvicorn (dashboard, rule store), Go 1.23 (Allocator + publish-mgr), nix dockerTools (proxy image), nftables, vanilla JS (dashboard UI).

**Phases NOT covered (future plans):** Phase 2 (UDP outbound via NFQUEUE), Phase 3 (nft user sub-chain), Phase 4 (TCP UX polish + counters).

**Spec reference:** `docs/plans/2026-05-22-dashboard-all-traffic-design.md`

---

## Phase 0 — Unified rule schema

### Task 1: Extend `Rule` dataclass with new fields, defaults preserve old behavior

**Files:**
- Modify: `proxy/claude_proxy/rules.py`
- Test: `proxy/tests/test_rules.py`

- [ ] **Step 1: Write the failing test** — append to `proxy/tests/test_rules.py`:

```python
def test_rule_has_new_schema_fields():
    from claude_proxy.rules import Rule
    r = Rule(action="allow", direction="out", proto="http",
             match={"host": "github.com"})
    assert r.action == "allow"
    assert r.direction == "out"
    assert r.proto == "http"
    assert r.match == {"host": "github.com"}
    # Defaults preserve old behavior: direction defaults to "out",
    # proto defaults to "any".
    r2 = Rule(action="allow", match={"host_regex": "github"})
    assert r2.direction == "out"
    assert r2.proto == "any"
```

- [ ] **Step 2: Run test to verify it fails**

```
devenv shell -- pytest proxy/tests/test_rules.py::test_rule_has_new_schema_fields -v
```

Expected: FAIL — `Rule.__init__() got an unexpected keyword argument 'action'`.

- [ ] **Step 3: Add new fields to `Rule`**

In `proxy/claude_proxy/rules.py`, replace the `Rule` dataclass with:

```python
@dataclass
class Rule:
    """A single traffic rule.

    New canonical schema (direction + proto + match object + action).
    Old (rule_type + pattern) is accepted and normalized in from_dict.
    """

    id: str = field(default_factory=lambda: str(uuid.uuid4()))
    direction: str = "out"          # "out" | "in"
    proto: str = "any"              # "http"|"tcp"|"udp"|"icmp"|"any"
    match: dict = field(default_factory=dict)
    action: str = "allow"           # "allow"|"deny"|"hold"
    label: str = ""
    created_at: float = field(default_factory=time.time)
    expires_at: Optional[float] = None
    source: str = "interactive"

    # Backward-compat fields preserved for code that still reads them.
    # to_dict emits the NEW shape; from_dict accepts both.
    @property
    def rule_type(self) -> str:
        return self.action

    @property
    def pattern(self) -> str:
        # If the rule was created with the old shape, the host_regex
        # field carries the original regex pattern.
        return self.match.get("host_regex", "") or self.match.get("host", "")

    def is_expired(self) -> bool:
        if self.expires_at is None:
            return False
        return time.time() > self.expires_at

    def compiled(self) -> re.Pattern:
        return _compile_pattern(self.pattern)
```

- [ ] **Step 4: Run test to verify it passes**

```
devenv shell -- pytest proxy/tests/test_rules.py::test_rule_has_new_schema_fields -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add proxy/claude_proxy/rules.py proxy/tests/test_rules.py
git commit -m "feat(proxy/rules): add direction/proto/match/action fields to Rule"
```

---

### Task 2: `Rule.from_dict` normalizes old-shape rules into new shape

**Files:**
- Modify: `proxy/claude_proxy/rules.py`
- Test: `proxy/tests/test_rules.py`

- [ ] **Step 1: Write the failing test**

```python
def test_from_dict_normalizes_old_shape():
    from claude_proxy.rules import Rule
    old = {
        "id": "abc",
        "rule_type": "allow",
        "pattern": "github.com",
        "label": "github",
        "created_at": 1000.0,
        "expires_at": None,
        "source": "preset",
    }
    r = Rule.from_dict(old)
    assert r.action == "allow"
    assert r.direction == "out"
    assert r.proto == "any"
    assert r.match == {"host_regex": "github.com"}
    assert r.label == "github"
    assert r.source == "preset"

def test_from_dict_accepts_new_shape():
    from claude_proxy.rules import Rule
    new = {
        "id": "def",
        "direction": "in",
        "proto": "tcp",
        "match": {"port": 3000},
        "action": "allow",
        "label": "vite",
        "created_at": 2000.0,
        "expires_at": None,
        "source": "interactive",
    }
    r = Rule.from_dict(new)
    assert r.direction == "in"
    assert r.proto == "tcp"
    assert r.match == {"port": 3000}
```

- [ ] **Step 2: Run both tests to verify they fail**

```
devenv shell -- pytest proxy/tests/test_rules.py -v -k from_dict
```

Expected: FAIL on both — `from_dict` still expects the old fields by name.

- [ ] **Step 3: Replace `Rule.from_dict`**

```python
    @classmethod
    def from_dict(cls, data: dict) -> "Rule":
        """Deserialize a rule from a dict. Accepts both old and new shapes.

        Old shape:
            {id, rule_type, pattern, label, created_at, expires_at, source}
        New shape:
            {id, direction, proto, match, action, label, created_at,
             expires_at, source}
        """
        # New shape if any of these keys are present:
        if "action" in data or "direction" in data or "proto" in data:
            return cls(
                id=data["id"],
                direction=data.get("direction", "out"),
                proto=data.get("proto", "any"),
                match=data.get("match", {}),
                action=data.get("action", "allow"),
                label=data.get("label", ""),
                created_at=data.get("created_at", time.time()),
                expires_at=data.get("expires_at"),
                source=data.get("source", "interactive"),
            )
        # Otherwise normalize old shape.
        return cls(
            id=data["id"],
            direction="out",
            proto="any",
            match={"host_regex": data.get("pattern", "")},
            action=data.get("rule_type", "allow"),
            label=data.get("label", ""),
            created_at=data.get("created_at", time.time()),
            expires_at=data.get("expires_at"),
            source=data.get("source", "interactive"),
        )
```

- [ ] **Step 4: Run tests to verify they pass**

```
devenv shell -- pytest proxy/tests/test_rules.py -v -k from_dict
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add proxy/claude_proxy/rules.py proxy/tests/test_rules.py
git commit -m "feat(proxy/rules): normalize old-shape rules in from_dict"
```

---

### Task 3: `Rule.to_dict` emits the new canonical shape

**Files:**
- Modify: `proxy/claude_proxy/rules.py`
- Test: `proxy/tests/test_rules.py`

- [ ] **Step 1: Write the failing test**

```python
def test_to_dict_emits_new_shape():
    from claude_proxy.rules import Rule
    r = Rule(direction="in", proto="tcp",
             match={"port": 5173}, action="allow",
             label="vite", source="interactive")
    d = r.to_dict()
    assert d["direction"] == "in"
    assert d["proto"] == "tcp"
    assert d["match"] == {"port": 5173}
    assert d["action"] == "allow"
    assert "rule_type" not in d   # old field gone from canonical output
    assert "pattern" not in d
```

- [ ] **Step 2: Run test to verify it fails**

```
devenv shell -- pytest proxy/tests/test_rules.py::test_to_dict_emits_new_shape -v
```

Expected: FAIL — `to_dict` still emits old shape.

- [ ] **Step 3: Replace `Rule.to_dict`**

```python
    def to_dict(self) -> dict:
        """Serialize the rule to a JSON-compatible dict (new shape)."""
        return {
            "id": self.id,
            "direction": self.direction,
            "proto": self.proto,
            "match": self.match,
            "action": self.action,
            "label": self.label,
            "created_at": self.created_at,
            "expires_at": self.expires_at,
            "source": self.source,
        }
```

- [ ] **Step 4: Run test to verify it passes**

```
devenv shell -- pytest proxy/tests/test_rules.py::test_to_dict_emits_new_shape -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add proxy/claude_proxy/rules.py proxy/tests/test_rules.py
git commit -m "refactor(proxy/rules): to_dict emits new canonical shape"
```

---

### Task 4: `RuleStore.match()` filters by direction/proto + uses `match` object

**Files:**
- Modify: `proxy/claude_proxy/rules.py`
- Test: `proxy/tests/test_rules.py`

The existing `match()` matches a URL string against rule `pattern` regexes. With the new schema we need it to take a *request descriptor* (url + direction + proto + dns_name) and filter rules accordingly.

- [ ] **Step 1: Write the failing tests**

```python
def test_match_filters_by_proto():
    from claude_proxy.rules import RuleStore, Rule
    store = RuleStore()
    store.add_rule(Rule(direction="out", proto="http",
                        match={"host": "github.com"}, action="allow"))
    # http request to github.com matches
    assert store.match_request(direction="out", proto="http",
                               url="https://github.com/x") == "allow"
    # tcp request to same host does NOT match the http rule
    assert store.match_request(direction="out", proto="tcp",
                               url="tcp://github.com:22") is None

def test_match_uses_host_field():
    from claude_proxy.rules import RuleStore, Rule
    store = RuleStore()
    store.add_rule(Rule(direction="out", proto="http",
                        match={"host": "github.com"}, action="allow"))
    assert store.match_request(direction="out", proto="http",
                               url="https://github.com/foo") == "allow"
    assert store.match_request(direction="out", proto="http",
                               url="https://evil.com/foo") is None

def test_match_uses_host_regex_field():
    from claude_proxy.rules import RuleStore, Rule
    store = RuleStore()
    store.add_rule(Rule(direction="out", proto="any",
                        match={"host_regex": ".*\\.github\\.com"},
                        action="allow"))
    assert store.match_request(direction="out", proto="http",
                               url="https://api.github.com/x") == "allow"

def test_match_proto_any_matches_all():
    from claude_proxy.rules import RuleStore, Rule
    store = RuleStore()
    store.add_rule(Rule(direction="out", proto="any",
                        match={"host_regex": "github\\.com"},
                        action="allow"))
    assert store.match_request(direction="out", proto="http",
                               url="https://github.com/") == "allow"
    assert store.match_request(direction="out", proto="tcp",
                               url="tcp://github.com:22") == "allow"
    assert store.match_request(direction="out", proto="udp",
                               url="udp://github.com:53") == "allow"
```

- [ ] **Step 2: Run tests to verify they fail**

```
devenv shell -- pytest proxy/tests/test_rules.py -v -k "match_filters or match_uses or match_proto"
```

Expected: FAIL — `RuleStore` has no `match_request` method.

- [ ] **Step 3: Add `match_request` to `RuleStore`** (keep the old `match` for back-compat)

```python
    def match_request(self, *, direction: str, proto: str, url: str,
                      dns_name: Optional[str] = None) -> Optional[str]:
        """Evaluate the rule store against a request descriptor.

        Returns "allow", "deny", or None (no rule matched — caller
        decides whether to hold or default-deny).

        Deny rules are evaluated first, then allow.
        """
        from urllib.parse import urlparse
        parsed = urlparse(url)
        host = parsed.hostname or ""
        port = parsed.port

        def matches(rule: Rule) -> bool:
            # direction must match exactly
            if rule.direction != direction:
                return False
            # proto: "any" wildcards; otherwise must equal
            if rule.proto != "any" and rule.proto != proto:
                return False
            m = rule.match or {}
            # host (exact, case-insensitive)
            if "host" in m and m["host"]:
                if (m["host"] or "").lower() != host.lower():
                    return False
            # host_regex
            if "host_regex" in m and m["host_regex"]:
                if not re.search(m["host_regex"], host):
                    # also check against the full URL for back-compat
                    # with old rules that matched the whole URL.
                    if not re.search(m["host_regex"], url):
                        return False
            # port
            if "port" in m and m["port"] is not None:
                if port != m["port"]:
                    return False
            # dns_name (only relevant for UDP/53 lookups)
            if "dns_name" in m and m["dns_name"]:
                if (dns_name or "").lower() != m["dns_name"].lower():
                    return False
            return True

        with self._lock:
            # Deny first
            for r in self._rules:
                if r.is_expired():
                    continue
                if r.action == "deny" and matches(r):
                    return "deny"
            # Then allow
            for r in self._rules:
                if r.is_expired():
                    continue
                if r.action == "allow" and matches(r):
                    return "allow"
        return None
```

- [ ] **Step 4: Run tests to verify they pass**

```
devenv shell -- pytest proxy/tests/test_rules.py -v -k "match_filters or match_uses or match_proto"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add proxy/claude_proxy/rules.py proxy/tests/test_rules.py
git commit -m "feat(proxy/rules): structured match_request with direction+proto filtering"
```

---

### Task 5: `addon.py` switches to `match_request`

**Files:**
- Modify: `proxy/claude_proxy/addon.py`

Two call sites — the HTTP `request()` handler and `tcp_start`. Each becomes a structured call.

- [ ] **Step 1: Read the current call sites**

In `proxy/claude_proxy/addon.py`, find `self.store.match(url)` in `request()` (~line 45) and `tcp_start()` (~line 101). Both will be replaced.

- [ ] **Step 2: Replace the HTTP request call**

In `request()`:

```python
        url = flow.request.pretty_url
        action = self.store.match_request(
            direction="out", proto="http", url=url,
        )
```

- [ ] **Step 3: Replace the TCP call**

In `tcp_start()`:

```python
            synthetic = f"tcp://{host}:{port}"
            action = self.store.match_request(
                direction="out", proto="tcp", url=synthetic,
            )
```

- [ ] **Step 4: Run all addon + integration tests**

```
devenv shell -- pytest proxy/tests/test_addon.py proxy/tests/test_integration.py -v
```

Expected: PASS — `match_request` returns the same `"allow"|"deny"|None` shape that the call sites already handle.

- [ ] **Step 5: Commit**

```
git add proxy/claude_proxy/addon.py
git commit -m "refactor(proxy/addon): use RuleStore.match_request with proto filtering"
```

---

### Task 6: Dashboard `POST /api/rules` accepts both old and new shape

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py`
- Test: `proxy/tests/test_dashboard.py`

- [ ] **Step 1: Write the failing tests**

```python
def test_post_rule_accepts_new_shape(client):
    resp = client.post("/api/rules", json={
        "direction": "out",
        "proto": "http",
        "match": {"host": "example.com"},
        "action": "allow",
        "label": "ex",
    }, headers={"X-Auth-Token": TEST_TOKEN})
    assert resp.status_code == 201
    rid = resp.json()["id"]

    rules = client.get("/api/rules").json()
    found = next(r for r in rules if r["id"] == rid)
    assert found["direction"] == "out"
    assert found["proto"] == "http"
    assert found["match"]["host"] == "example.com"
    assert found["action"] == "allow"

def test_post_rule_accepts_old_shape(client):
    """Back-compat: legacy POSTs with type/pattern still work."""
    resp = client.post("/api/rules", json={
        "type": "http_allow",
        "pattern": "github.com",
        "label": "gh",
    }, headers={"X-Auth-Token": TEST_TOKEN})
    assert resp.status_code == 201
    rules = client.get("/api/rules").json()
    found = next(r for r in rules if r["label"] == "gh")
    assert found["proto"] in ("http", "any")
    assert found["action"] == "allow"
```

Replace `TEST_TOKEN` with whatever the existing test fixture uses; check the top of `proxy/tests/test_dashboard.py` for the existing pattern.

- [ ] **Step 2: Run tests to verify they fail**

```
devenv shell -- pytest proxy/tests/test_dashboard.py -v -k "new_shape or old_shape"
```

Expected: FAIL — endpoint rejects new-shape body OR doesn't normalize old shape correctly.

- [ ] **Step 3: Update `add_rule` in `proxy/claude_proxy/dashboard.py`**

```python
async def add_rule(request: Request) -> JSONResponse:
    """Add a new rule. Accepts both new shape and legacy old shape."""
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    body = await request.json()

    # New shape: has "action" key
    if "action" in body:
        direction = body.get("direction", "out")
        proto = body.get("proto", "any")
        match = body.get("match") or {}
        action = body["action"]
        label = body.get("label", "")
        expires_at = body.get("expires_at")
        source = body.get("source", "interactive")
        rule_id = _store.add_structured(
            direction=direction, proto=proto, match=match,
            action=action, label=label,
            expires_at=expires_at, source=source,
        )
    else:
        # Legacy old shape with type + pattern
        rule_type = body.get("type")
        pattern = body.get("pattern")
        if not rule_type or not pattern:
            return JSONResponse(
                {"error": "type+pattern (old) or action+match (new) are required"},
                status_code=400,
            )
        # Map old "<proto>_<action>" type to new fields
        # e.g. "http_allow" -> proto=http, action=allow
        parts = rule_type.split("_", 1)
        if len(parts) == 2 and parts[1] in ("allow", "deny"):
            proto_part, action_part = parts
        else:
            proto_part, action_part = "any", rule_type
        rule_id = _store.add_structured(
            direction="out",
            proto=proto_part if proto_part in ("http", "tcp", "udp", "any") else "any",
            match={"host_regex": pattern},
            action=action_part,
            label=body.get("label", ""),
            expires_at=body.get("expires_at"),
            source=body.get("source", "interactive"),
        )

    _save_profile()
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"id": rule_id}, status_code=201)
```

- [ ] **Step 4: Add `RuleStore.add_structured` if it doesn't exist**

In `proxy/claude_proxy/rules.py`:

```python
    def add_structured(
        self,
        *,
        direction: str,
        proto: str,
        match: dict,
        action: str,
        label: str = "",
        expires_at: Optional[float] = None,
        source: str = "interactive",
    ) -> str:
        """Add a rule using the new canonical schema. Returns the rule id."""
        with self._lock:
            rule = Rule(
                direction=direction,
                proto=proto,
                match=match,
                action=action,
                label=label,
                expires_at=expires_at,
                source=source,
            )
            self._rules.append(rule)
            return rule.id
```

- [ ] **Step 5: Run tests to verify they pass**

```
devenv shell -- pytest proxy/tests/test_dashboard.py -v -k "new_shape or old_shape"
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add proxy/claude_proxy/dashboard.py proxy/claude_proxy/rules.py proxy/tests/test_dashboard.py
git commit -m "feat(proxy/dashboard): POST /api/rules accepts new schema with back-compat"
```

---

### Task 7: Verify existing presets still load

**Files:**
- Test: `proxy/tests/test_rules.py`

- [ ] **Step 1: Write a regression test**

```python
def test_loads_legacy_preset_format(tmp_path):
    """Old preset files on disk continue to work without migration."""
    import json
    from claude_proxy.rules import RuleStore
    preset = tmp_path / "legacy.json"
    preset.write_text(json.dumps([
        {"id": "1", "rule_type": "allow",  "pattern": "github\\.com",
         "label": "gh", "created_at": 0.0, "expires_at": None,
         "source": "preset"},
        {"id": "2", "rule_type": "deny",   "pattern": "evil\\.com",
         "label": "no",  "created_at": 0.0, "expires_at": None,
         "source": "preset"},
    ]))
    store = RuleStore()
    store.load(str(preset))
    assert store.match_request(direction="out", proto="http",
                               url="https://github.com/x") == "allow"
    assert store.match_request(direction="out", proto="http",
                               url="https://evil.com/x") == "deny"
```

- [ ] **Step 2: Run the test**

```
devenv shell -- pytest proxy/tests/test_rules.py::test_loads_legacy_preset_format -v
```

Expected: PASS — Task 2's `from_dict` already normalizes.

- [ ] **Step 3: Commit**

```
git add proxy/tests/test_rules.py
git commit -m "test(proxy/rules): regression — legacy preset files load via normalizer"
```

---

## Phase 1 — Inbound publish (TCP + UDP, dynamic)

### Task 8: `PortAllocator` skeleton — type and constructor

**Files:**
- Create: `internal/httpproxy/portalloc/allocator.go`
- Test: `internal/httpproxy/portalloc/allocator_test.go`

- [ ] **Step 1: Write the failing test**

`internal/httpproxy/portalloc/allocator_test.go`:

```go
package portalloc

import (
	"path/filepath"
	"testing"
)

func TestNew_CreatesEmptyAllocator(t *testing.T) {
	dir := t.TempDir()
	a, err := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil allocator")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```
devenv shell -- go test ./internal/httpproxy/portalloc/ -v -run TestNew_CreatesEmptyAllocator
```

Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Write the package**

`internal/httpproxy/portalloc/allocator.go`:

```go
// Package portalloc tracks which contiguous host-port ranges have been
// claimed by which session, so two concurrent claude-container sessions
// don't publish ports to the same host range.
//
// State lives in a JSON file on disk. Operations are guarded by a
// per-process mutex and a flock on the file so concurrent invocations
// (or hosts with multiple claude-container processes) coordinate.
package portalloc

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Allocation is one session's claim on the host port range.
type Allocation struct {
	Base int `json:"base"` // first port (inclusive)
	Size int `json:"size"` // number of contiguous ports
}

// Allocator holds the allocation state for the configured pool.
type Allocator struct {
	path       string
	poolStart  int
	poolEnd    int // inclusive
	defaultSz  int
	mu         sync.Mutex
}

// New returns an Allocator backed by the JSON file at path. The pool
// spans [poolStart, poolEnd] inclusive. defaultSize is how many ports
// each session gets if not overridden.
func New(path string, poolStart, poolEnd, defaultSize int) (*Allocator, error) {
	if poolStart > poolEnd {
		return nil, fmt.Errorf("portalloc: poolStart %d > poolEnd %d",
			poolStart, poolEnd)
	}
	if defaultSize <= 0 {
		return nil, fmt.Errorf("portalloc: defaultSize must be > 0")
	}
	return &Allocator{
		path:      path,
		poolStart: poolStart,
		poolEnd:   poolEnd,
		defaultSz: defaultSize,
	}, nil
}

// load reads the on-disk allocation map. Empty map if file is missing.
func (a *Allocator) load() (map[string]Allocation, error) {
	data, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return map[string]Allocation{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("portalloc: read %s: %w", a.path, err)
	}
	var m map[string]Allocation
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("portalloc: parse %s: %w", a.path, err)
	}
	return m, nil
}

// save writes the allocation map back to disk atomically.
func (a *Allocator) save(m map[string]Allocation) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```
devenv shell -- go test ./internal/httpproxy/portalloc/ -v -run TestNew_CreatesEmptyAllocator
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/httpproxy/portalloc/
git commit -m "feat(portalloc): Allocator skeleton with on-disk JSON state"
```

---

### Task 9: `Claim()` scans for the next free contiguous range

**Files:**
- Modify: `internal/httpproxy/portalloc/allocator.go`
- Test: `internal/httpproxy/portalloc/allocator_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestClaim_FirstSessionGetsPoolStart(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	alloc, err := a.Claim("session-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if alloc.Base != 30000 || alloc.Size != 10 {
		t.Errorf("got %+v, want base=30000 size=10", alloc)
	}
}

func TestClaim_SecondSessionGetsNextRange(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	_, _ = a.Claim("session-a", 10)
	alloc, err := a.Claim("session-b", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if alloc.Base != 30010 || alloc.Size != 10 {
		t.Errorf("got %+v, want base=30010 size=10", alloc)
	}
}

func TestClaim_ExistingSessionReturnsSameRange(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	first, _ := a.Claim("session-a", 10)
	again, _ := a.Claim("session-a", 10)
	if again != first {
		t.Errorf("re-claim returned %+v, want same as first %+v", again, first)
	}
}

func TestClaim_PoolExhaustionErrors(t *testing.T) {
	dir := t.TempDir()
	// pool size 20 ports, 10 each → 2 sessions max
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30019, 10)
	_, _ = a.Claim("s1", 10)
	_, _ = a.Claim("s2", 10)
	_, err := a.Claim("s3", 10)
	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
devenv shell -- go test ./internal/httpproxy/portalloc/ -v -run TestClaim
```

Expected: FAIL — `Claim` method doesn't exist.

- [ ] **Step 3: Implement `Claim`**

Append to `internal/httpproxy/portalloc/allocator.go`:

```go
// Claim reserves a contiguous range for the named session. If the
// session already has an allocation, that allocation is returned
// unchanged. Size 0 uses the configured defaultSize.
func (a *Allocator) Claim(sessionName string, size int) (Allocation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if size == 0 {
		size = a.defaultSz
	}

	m, err := a.load()
	if err != nil {
		return Allocation{}, err
	}

	// Idempotent: existing session keeps its range.
	if existing, ok := m[sessionName]; ok {
		return existing, nil
	}

	// Build a sorted list of occupied ranges so we can scan for a gap.
	type occ struct{ start, end int }
	occupied := make([]occ, 0, len(m))
	for _, al := range m {
		occupied = append(occupied, occ{al.Base, al.Base + al.Size - 1})
	}
	// sort.Slice would be nicer but we keep deps minimal.
	for i := range occupied {
		for j := i + 1; j < len(occupied); j++ {
			if occupied[j].start < occupied[i].start {
				occupied[i], occupied[j] = occupied[j], occupied[i]
			}
		}
	}

	// Walk the pool looking for a gap of `size` ports.
	cursor := a.poolStart
	for _, o := range occupied {
		if cursor+size-1 < o.start {
			// fits before this range
			break
		}
		cursor = o.end + 1
	}
	if cursor+size-1 > a.poolEnd {
		return Allocation{}, fmt.Errorf(
			"portalloc: pool %d-%d exhausted (cannot fit %d ports for session %q); "+
				"override with --publish-base / --publish-range",
			a.poolStart, a.poolEnd, size, sessionName)
	}

	al := Allocation{Base: cursor, Size: size}
	m[sessionName] = al
	if err := a.save(m); err != nil {
		return Allocation{}, err
	}
	return al, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
devenv shell -- go test ./internal/httpproxy/portalloc/ -v -run TestClaim
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```
git add internal/httpproxy/portalloc/
git commit -m "feat(portalloc): Claim() scans for first-fit free range"
```

---

### Task 10: `Release()` returns a session's range to the pool

**Files:**
- Modify: `internal/httpproxy/portalloc/allocator.go`
- Test: `internal/httpproxy/portalloc/allocator_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRelease_FreesTheRange(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	_, _ = a.Claim("session-a", 10)   // 30000-30009
	_, _ = a.Claim("session-b", 10)   // 30010-30019
	if err := a.Release("session-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// New session should now get the freed range.
	got, _ := a.Claim("session-c", 10)
	if got.Base != 30000 {
		t.Errorf("after Release, next Claim got base=%d, want 30000", got.Base)
	}
}

func TestRelease_UnknownSessionIsNoop(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	if err := a.Release("never-existed"); err != nil {
		t.Errorf("Release of unknown session should be a no-op, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
devenv shell -- go test ./internal/httpproxy/portalloc/ -v -run TestRelease
```

Expected: FAIL — `Release` doesn't exist.

- [ ] **Step 3: Implement `Release`**

```go
// Release returns a session's range to the pool. Unknown session names
// are a no-op (idempotent — safe to call from cleanup paths).
func (a *Allocator) Release(sessionName string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	m, err := a.load()
	if err != nil {
		return err
	}
	if _, ok := m[sessionName]; !ok {
		return nil
	}
	delete(m, sessionName)
	return a.save(m)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
devenv shell -- go test ./internal/httpproxy/portalloc/ -v -run TestRelease
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/httpproxy/portalloc/
git commit -m "feat(portalloc): Release() returns a session's range to the pool"
```

---

### Task 11: `ProxyOpts.PublishRange` field + `RunArgs` emits ranges

**Files:**
- Modify: `internal/httpproxy/httpproxy.go`
- Test: `internal/httpproxy/httpproxy_test.go` (create if missing)

- [ ] **Step 1: Write the failing test**

In `internal/httpproxy/httpproxy_test.go`:

```go
package httpproxy

import (
	"strings"
	"testing"
)

func TestRunArgs_EmitsPublishRange(t *testing.T) {
	args := RunArgs(ProxyOpts{
		Session:       "s1",
		ConfigDir:     "/tmp/cfg",
		DashboardPort: 9000,
		PublishRange:  PortRange{Base: 30000, Size: 10},
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-p 127.0.0.1:30000-30009:30000-30009/tcp") {
		t.Errorf("missing TCP publish range; got %s", joined)
	}
	if !strings.Contains(joined, "-p 127.0.0.1:30000-30009:30000-30009/udp") {
		t.Errorf("missing UDP publish range; got %s", joined)
	}
	if !strings.Contains(joined, "PROXY_PUBLISH_RANGE=30000-30009") {
		t.Errorf("missing PROXY_PUBLISH_RANGE env; got %s", joined)
	}
}

func TestRunArgs_NoPublishRange_NoExtraArgs(t *testing.T) {
	args := RunArgs(ProxyOpts{
		Session:       "s1",
		ConfigDir:     "/tmp/cfg",
		DashboardPort: 9000,
	})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "PROXY_PUBLISH_RANGE") {
		t.Errorf("PROXY_PUBLISH_RANGE leaked into args without a range; got %s", joined)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
devenv shell -- go test ./internal/httpproxy/ -v -run TestRunArgs_EmitsPublishRange
```

Expected: FAIL — `PortRange` and `PublishRange` field don't exist.

- [ ] **Step 3: Add `PortRange` type and `PublishRange` field**

In `internal/httpproxy/httpproxy.go`, find the `ProxyOpts` struct and add:

```go
// PortRange describes a contiguous host-port range published by the proxy.
type PortRange struct {
	Base int // first port (inclusive)
	Size int // number of contiguous ports
}

// (Last) return the inclusive end of the range.
func (r PortRange) Last() int { return r.Base + r.Size - 1 }

// IsZero reports whether the range was left at its zero value.
func (r PortRange) IsZero() bool { return r.Base == 0 && r.Size == 0 }
```

Then add the field to `ProxyOpts`:

```go
type ProxyOpts struct {
	Session       string
	ConfigDir     string
	DashboardPort int
	ForceRestart  bool
	PublishRange  PortRange // NEW
}
```

- [ ] **Step 4: Update `RunArgs` to emit -p and env**

In `RunArgs`, after the existing `-p` for the dashboard port, append:

```go
	if !opts.PublishRange.IsZero() {
		spec := fmt.Sprintf("127.0.0.1:%d-%d:%d-%d",
			opts.PublishRange.Base, opts.PublishRange.Last(),
			opts.PublishRange.Base, opts.PublishRange.Last())
		args = append(args,
			"-p", spec+"/tcp",
			"-p", spec+"/udp",
		)
	}
```

And before the trailing `ImageTag()`, after the existing `-e PROXY_SESSION=...`:

```go
	if !opts.PublishRange.IsZero() {
		args = append(args, "-e", fmt.Sprintf("PROXY_PUBLISH_RANGE=%d-%d",
			opts.PublishRange.Base, opts.PublishRange.Last()))
	}
```

- [ ] **Step 5: Run tests to verify they pass**

```
devenv shell -- go test ./internal/httpproxy/ -v -run TestRunArgs_EmitsPublishRange
devenv shell -- go test ./internal/httpproxy/ -v -run TestRunArgs_NoPublishRange
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/httpproxy/httpproxy.go internal/httpproxy/httpproxy_test.go
git commit -m "feat(httpproxy): ProxyOpts.PublishRange emits TCP+UDP -p and env"
```

---

### Task 12: Plumb `PortAllocator` through `session.Launch`

**Files:**
- Modify: `internal/session/options.go`
- Modify: `internal/session/launch.go`

- [ ] **Step 1: Add Opts fields**

In `internal/session/options.go`, add to `Opts`:

```go
	// Inbound port publishing — pool and size are configurable.
	PublishBase  int // first host port the pool may use (default 30000)
	PublishRange int // ports per session (default 10)
```

- [ ] **Step 2: Add defaults in `ApplyDefaults`**

In the same file:

```go
	if o.PublishBase == 0 {
		o.PublishBase = 30000
	}
	if o.PublishRange == 0 {
		o.PublishRange = 10
	}
```

- [ ] **Step 3: Claim a range in `Launch` and pass it to proxy**

In `internal/session/launch.go`, after `opts.ApplyDefaults()` and before the proxy is started, add (use the import path that matches your module name):

```go
	// Claim a host-port range for inbound publishing. Released in the
	// cleanup closure below when the session is removed.
	allocPath := filepath.Join(config.DefaultDir(), "published-port-allocations.json")
	alloc, err := portalloc.New(
		allocPath, opts.PublishBase,
		opts.PublishBase+1000-1, // 100 sessions of size 10 by default
		opts.PublishRange,
	)
	if err != nil {
		return nil, fmt.Errorf("portalloc: %w", err)
	}
	allocation, err := alloc.Claim(opts.Name, opts.PublishRange)
	if err != nil {
		return nil, err
	}
```

And in the `httpproxy.ProxyOpts` you build below, set:

```go
		PublishRange: httpproxy.PortRange{Base: allocation.Base, Size: allocation.Size},
```

Add the import:

```go
	"github.com/joegoldin/claude-container/internal/httpproxy/portalloc"
```

- [ ] **Step 4: Release on cleanup**

In the `cleanup` closure inside `Launch`:

```go
	cleanup := func() {
		// existing teardown ...
		_ = alloc.Release(opts.Name)
	}
```

- [ ] **Step 5: Run unit tests**

```
devenv shell -- go test ./internal/session/ ./internal/httpproxy/ ./internal/httpproxy/portalloc/ -v
```

Expected: PASS — no behavior change for callers that don't use publish.

- [ ] **Step 6: Commit**

```
git add internal/session/
git commit -m "feat(session): claim host port range from PortAllocator at launch"
```

---

### Task 13: CLI flags `--publish-range` / `--publish-base`

**Files:**
- Modify: `cmd/root.go` (bare-invoke flag set)
- Modify: `cmd/run.go`
- Modify: `cmd/work.go`

- [ ] **Step 1: Add the vars + flag declarations**

Each of `cmd/root.go`, `cmd/run.go`, `cmd/work.go` already has a flag block. Add:

```go
	rootCmd.Flags().IntVar(&rootPublishRange, "publish-range", 0,
		"ports per session reserved for inbound publish (default 10)")
	rootCmd.Flags().IntVar(&rootPublishBase, "publish-base", 0,
		"first host port the inbound publish pool may use (default 30000)")
```

(Substitute the corresponding `run*`/`work*` variable names per file.)

And new vars at the top:

```go
	rootPublishRange int
	rootPublishBase  int
```

- [ ] **Step 2: Pipe into Opts**

Wherever the command builds `session.Opts{...}`, add:

```go
	PublishRange: rootPublishRange,
	PublishBase:  rootPublishBase,
```

- [ ] **Step 3: Build to confirm**

```
devenv shell -- go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add cmd/
git commit -m "feat(cmd): --publish-range / --publish-base CLI flags"
```

---

### Task 14: `publish-mgr` daemon — Unix socket skeleton

**Files:**
- Create: `proxy/publish-mgr/main.go`
- Create: `proxy/publish-mgr/go.mod`

- [ ] **Step 1: Initialize a tiny Go module**

```
mkdir -p proxy/publish-mgr
cd proxy/publish-mgr && devenv shell -- go mod init github.com/joegoldin/claude-container/proxy/publish-mgr
```

- [ ] **Step 2: Write the skeleton**

`proxy/publish-mgr/main.go`:

```go
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
	Protocol      string `json:"protocol"`       // "tcp" | "udp"
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

func (m *manager) handlePublish(w http.ResponseWriter, r *http.Request)  {
	// Task 15 implements this.
	writeJSON(w, 501, publishResp{Error: "not implemented yet"})
}

func (m *manager) handleUnpublish(w http.ResponseWriter, r *http.Request) {
	// Task 16 implements this.
	writeJSON(w, 501, publishResp{Error: "not implemented yet"})
}
```

- [ ] **Step 3: Build to confirm**

```
cd proxy/publish-mgr && devenv shell -- go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add proxy/publish-mgr/
git commit -m "feat(publish-mgr): Unix-socket HTTP skeleton with /publish /unpublish /list"
```

---

### Task 15: `publish-mgr` `/publish` allocates port + adds nft rule

**Files:**
- Modify: `proxy/publish-mgr/main.go`

- [ ] **Step 1: Add helper to invoke nft and pick a free port**

Append to `main.go`:

```go
import (
	// existing imports plus:
	"os/exec"
)

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
```

- [ ] **Step 2: Implement `handlePublish`**

Replace the existing `handlePublish` stub:

```go
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
```

- [ ] **Step 3: Build**

```
cd proxy/publish-mgr && devenv shell -- go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add proxy/publish-mgr/main.go
git commit -m "feat(publish-mgr): /publish allocates a port and adds nft INPUT accept"
```

---

### Task 16: `publish-mgr` `/unpublish` removes nft rule

**Files:**
- Modify: `proxy/publish-mgr/main.go`

- [ ] **Step 1: Add nft delete helper**

```go
func nftDelInputAccept(proto string, port int) error {
	// nft delete by handle would be cleaner, but for simplicity we use
	// the same rule expression to identify the rule. nft's delete-by-
	// expression is not supported directly, so we list handles first
	// and delete the matching one.
	out, err := exec.Command("nft", "-a", "list", "chain", "inet",
		"claude_proxy_fw", "input").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft list: %v: %s", err, out)
	}
	needle := fmt.Sprintf("%s dport %d accept", proto, port)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		// Format: "         tcp dport 3000 accept # handle 12"
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
```

- [ ] **Step 2: Implement `handleUnpublish`**

```go
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
```

- [ ] **Step 3: Build**

```
cd proxy/publish-mgr && devenv shell -- go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add proxy/publish-mgr/main.go
git commit -m "feat(publish-mgr): /unpublish deletes nft rule by handle"
```

---

### Task 17: Bake `publish-mgr` into the proxy image

**Files:**
- Modify: `nix/proxy-image.nix`

- [ ] **Step 1: Add the Go build to the derivation**

In `nix/proxy-image.nix`, near the existing python-package contents, define a Go-built binary. Use the existing `buildGoModule` pattern from the main image; if there isn't one, add:

```nix
let
  publishMgr = pkgs.buildGoModule {
    name = "publish-mgr";
    src = ../proxy/publish-mgr;
    vendorHash = null;  # update on first build with the hash nix complains about
    subPackages = [ "." ];
  };
```

- [ ] **Step 2: Include it in the image contents**

In the `contents` (or `paths`) list of the `dockerTools.buildLayeredImage` call:

```nix
    contents = [
      # existing entries…
      publishMgr
    ];
```

- [ ] **Step 3: Modify the proxy entrypoint to start publish-mgr**

In the entrypoint script (also in `proxy-image.nix`), before the line that exec's mitmproxy / claude_proxy.app, add:

```bash
# Start publish-mgr in the background (uid 1500 owns the socket).
${pkgs.coreutils}/bin/su-exec 1500:1500 ${publishMgr}/bin/publish-mgr &
```

- [ ] **Step 4: Build the proxy image and verify it loads**

```
nix build .#claude-proxy-image
docker load -i result
```

Expected: image loads.

- [ ] **Step 5: Commit**

```
git add nix/proxy-image.nix
git commit -m "build(proxy-image): bake publish-mgr binary and start it at boot"
```

---

### Task 18: Allow inbound on published ports in the firewall

**Files:**
- Modify: `nix/proxy-image.nix` (the proxy entrypoint nft rules)

The INPUT chain currently default-drops everything except `lo`, established/related, 8080, 8081. Published-port traffic must reach the in-container app over the netns's eth0 — which the INPUT chain sees. We need to allow inbound to user-published ports.

Two options: **dynamic** (publish-mgr adds the rule per port) — Tasks 15/16 already do that — or **range-wide** (allow the whole 30000-30050 range up front). Dynamic is what we built, but the entrypoint needs to ensure the chain is set up so publish-mgr can add to it.

- [ ] **Step 1: Verify the `input` chain exists**

Read the existing nft ruleset in `nix/proxy-image.nix`. Confirm the `input` chain is declared with `policy drop`. If publish-mgr adds rules to a non-existent chain, it'll fail.

- [ ] **Step 2: Add the `claude_proxy_fw_user_in` chain skeleton, jumped from input**

In the entrypoint's `nft -f -` heredoc, modify:

```
chain input {
    type filter hook input priority 0; policy drop;
    iif "lo" accept
    ct state established,related accept
    tcp dport 8081 accept
    tcp dport 8080 accept
}
```

to be:

```
chain input {
    type filter hook input priority 0; policy drop;
    iif "lo" accept
    ct state established,related accept
    tcp dport 8081 accept
    tcp dport 8080 accept
    jump publish_in
    log prefix "claude_proxy_fw input drop: " level debug
}

chain publish_in {
    # publish-mgr appends accept rules here at runtime
}
```

- [ ] **Step 3: Update publish-mgr to add rules to `publish_in`**

In `proxy/publish-mgr/main.go`, change `nftAddInputAccept` and `nftDelInputAccept` to operate on the `publish_in` chain instead of `input`:

```go
func nftAddInputAccept(proto string, port int) error {
	cmd := exec.Command("nft", "add", "rule", "inet", "claude_proxy_fw",
		"publish_in", proto, "dport", strconv.Itoa(port), "accept")
	// ...
}
```

(And the analogous change in `nftDelInputAccept` — list `publish_in`, not `input`.)

- [ ] **Step 4: Rebuild and confirm the proxy starts cleanly**

```
nix build .#claude-proxy-image && docker load -i result
docker run --rm -it --name test-proxy --cap-add NET_ADMIN \
    -e PROXY_SESSION=test -e PROXY_PUBLISH_RANGE=30000-30009 \
    claude-proxy 2>&1 | head -30
```

Expected: proxy starts, no nft errors.

- [ ] **Step 5: Commit**

```
git add nix/proxy-image.nix proxy/publish-mgr/main.go
git commit -m "feat(proxy-image): publish_in nft sub-chain + publish-mgr targets it"
```

---

### Task 19: Dashboard `POST /api/publish` proxies to the Unix socket

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py`
- Test: `proxy/tests/test_dashboard.py`

- [ ] **Step 1: Write the failing test**

In `proxy/tests/test_dashboard.py`:

```python
def test_publish_endpoint_calls_publish_mgr(client, monkeypatch):
    """POST /api/publish forwards to /run/publish-mgr.sock."""
    import httpx
    calls = []
    class FakeTransport:
        def handle_request(self, request):
            calls.append((request.method, request.url.path,
                          request.read().decode()))
            return httpx.Response(200, json={
                "host_port": 30005, "container_port": 3000,
                "protocol": "tcp", "ok": True,
            })

    # The dashboard's publish forwarder must use a Unix-socket httpx
    # client; we monkeypatch the transport factory.
    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport",
                        FakeTransport())

    resp = client.post("/api/publish",
        json={"protocol": "tcp", "container_port": 3000, "label": "vite"},
        headers={"X-Auth-Token": TEST_TOKEN})
    assert resp.status_code == 200
    assert resp.json()["host_port"] == 30005
    assert len(calls) == 1
    assert calls[0][1].endswith("/publish")
```

- [ ] **Step 2: Run the test to verify it fails**

```
devenv shell -- pytest proxy/tests/test_dashboard.py::test_publish_endpoint_calls_publish_mgr -v
```

Expected: FAIL — `/api/publish` doesn't exist yet.

- [ ] **Step 3: Add the endpoint**

In `proxy/claude_proxy/dashboard.py`:

```python
import httpx

_publish_mgr_transport = httpx.HTTPTransport(uds="/run/publish-mgr.sock")

async def publish(request: Request) -> JSONResponse:
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    body = await request.json()
    try:
        # Use a fresh client per call — the socket may have been
        # re-created if publish-mgr restarted.
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.post("http://publish-mgr/publish", json=body, timeout=5)
        return JSONResponse(r.json(), status_code=r.status_code)
    except Exception as e:
        return JSONResponse({"error": f"publish-mgr: {e}"}, status_code=502)

async def unpublish(request: Request) -> JSONResponse:
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    body = await request.json()
    try:
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.post("http://publish-mgr/unpublish", json=body, timeout=5)
        return JSONResponse(r.json(), status_code=r.status_code)
    except Exception as e:
        return JSONResponse({"error": f"publish-mgr: {e}"}, status_code=502)

async def list_published(request: Request) -> JSONResponse:
    try:
        with httpx.Client(transport=_publish_mgr_transport) as c:
            r = c.get("http://publish-mgr/list", timeout=5)
        return JSONResponse(r.json(), status_code=r.status_code)
    except Exception as e:
        return JSONResponse({"error": f"publish-mgr: {e}"}, status_code=502)
```

Then in the `routes = [...]` list near the bottom of `dashboard.py`:

```python
    Route("/api/publish", publish, methods=["POST"]),
    Route("/api/unpublish", unpublish, methods=["POST"]),
    Route("/api/published-ports", list_published, methods=["GET"]),
```

- [ ] **Step 4: Confirm `httpx` is on the proxy image's Python**

Check `proxy/pyproject.toml` for an `httpx` dep; if absent, add it:

```
[project]
dependencies = [
    # existing ...
    "httpx>=0.27",
]
```

Then update the lock and rebuild the proxy image.

- [ ] **Step 5: Run the test to verify it passes**

```
devenv shell -- pytest proxy/tests/test_dashboard.py::test_publish_endpoint_calls_publish_mgr -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add proxy/claude_proxy/dashboard.py proxy/pyproject.toml proxy/tests/test_dashboard.py
git commit -m "feat(proxy/dashboard): /api/publish endpoints proxy to publish-mgr socket"
```

---

### Task 20: Dashboard UI — "Published Ports" tab

**Files:**
- Modify: `proxy/static/index.html`
- Modify: `proxy/static/app.js`
- Modify: `proxy/static/style.css`

- [ ] **Step 1: Add the tab markup to `index.html`**

In the existing tab navigation:

```html
<nav class="tabs">
  <button data-tab="pending">Pending</button>
  <button data-tab="rules">Rules</button>
  <button data-tab="published">Published Ports</button>
</nav>
```

And the panel below:

```html
<section id="published-panel" class="panel" hidden>
  <h2>Published Ports</h2>

  <form id="publish-form">
    <label>Protocol:
      <select name="protocol">
        <option value="tcp">TCP</option>
        <option value="udp">UDP</option>
      </select>
    </label>
    <label>Container port: <input name="container_port" type="number" min="1024" max="65535" required></label>
    <label>Host port (auto if blank):
      <input name="host_port" type="number" min="1024" max="65535">
    </label>
    <label>Label: <input name="label" type="text"></label>
    <button type="submit">Publish</button>
  </form>

  <table id="published-table">
    <thead>
      <tr>
        <th>Proto</th><th>Host port</th><th>Container port</th><th>Label</th><th></th>
      </tr>
    </thead>
    <tbody></tbody>
  </table>
</section>
```

- [ ] **Step 2: Wire up event handlers in `app.js`**

Append:

```javascript
async function refreshPublished() {
  const r = await fetch('/api/published-ports');
  const items = await r.json();
  const tbody = document.querySelector('#published-table tbody');
  tbody.innerHTML = '';
  for (const it of items) {
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td>${it.protocol}</td>
      <td><a href="http://127.0.0.1:${it.host_port}" target="_blank">${it.host_port}</a></td>
      <td>${it.container_port}</td>
      <td>${it.label || ''}</td>
      <td><button class="unpublish-btn" data-port="${it.host_port}" data-proto="${it.protocol}">Unpublish</button></td>
    `;
    tbody.appendChild(tr);
  }
}

document.querySelector('#publish-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = {
    protocol: f.get('protocol'),
    container_port: parseInt(f.get('container_port'), 10),
    label: f.get('label') || '',
  };
  const hp = f.get('host_port');
  if (hp) body.host_port = parseInt(hp, 10);
  const r = await fetch('/api/publish', {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'X-Auth-Token': AUTH_TOKEN},
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    const err = await r.json();
    alert('Publish failed: ' + err.error);
    return;
  }
  e.target.reset();
  refreshPublished();
});

document.querySelector('#published-table tbody').addEventListener('click', async (e) => {
  if (!e.target.classList.contains('unpublish-btn')) return;
  const port = parseInt(e.target.dataset.port, 10);
  const proto = e.target.dataset.proto;
  await fetch('/api/unpublish', {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'X-Auth-Token': AUTH_TOKEN},
    body: JSON.stringify({protocol: proto, host_port: port}),
  });
  refreshPublished();
});

// Tab switching — wire the new tab into existing logic.
document.querySelectorAll('.tabs button').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.panel').forEach(p => p.hidden = true);
    document.querySelector('#' + btn.dataset.tab + '-panel').hidden = false;
    if (btn.dataset.tab === 'published') refreshPublished();
  });
});
```

(If there's already a tab-switching block, modify it instead of duplicating.)

- [ ] **Step 3: Minimal CSS in `style.css`**

```css
#publish-form { display: flex; gap: 1em; align-items: end; }
#publish-form label { display: flex; flex-direction: column; font-size: 0.9em; }
#published-table { width: 100%; border-collapse: collapse; margin-top: 1em; }
#published-table th, #published-table td {
  padding: 6px 12px; border-bottom: 1px solid #eee; text-align: left;
}
.unpublish-btn { padding: 2px 8px; font-size: 0.85em; }
```

- [ ] **Step 4: Reload the dashboard and confirm visually**

The full E2E loop is exercised in Task 22.

- [ ] **Step 5: Commit**

```
git add proxy/static/
git commit -m "feat(proxy/dashboard-ui): Published Ports tab with publish/unpublish"
```

---

### Task 21: E2E test — TCP publish from host

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/security_e2e_test.go`:

```go
// TestSecurity_Publish_TCP_RoundTrip verifies the dashboard /api/publish
// endpoint allocates a host port, the firewall accepts inbound on it,
// and a host-side curl reaches the container.
func TestSecurity_Publish_TCP_RoundTrip(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-pub-tcp"
	startSecurityContainer(t, name, "--yolo", "--publish-range=10")

	// Start an HTTP echo server inside the container, bound to 0.0.0.0.
	go boundedDockerExec(t, 30*time.Second, name, "sh", "-c",
		"echo 'HELLO PUBLISH' > /tmp/payload && "+
			"python3 -m http.server 3000 --bind 0.0.0.0 --directory /tmp")
	time.Sleep(2 * time.Second) // let the server bind

	// POST /api/publish via the dashboard.
	api := newProxyAPI(t, configDir, name)
	pub := api.publish(t, "tcp", 3000, "")
	t.Logf("published: %+v", pub)

	// Host-side curl should now reach the echo server through the
	// allocated host port (e.g., 30000).
	url := fmt.Sprintf("http://127.0.0.1:%d/payload", pub.HostPort)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("host curl: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "HELLO PUBLISH") {
		t.Errorf("body=%q, want HELLO PUBLISH", body)
	}
}
```

You'll also need a small helper on `proxyAPI`:

```go
type publishResult struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
}

func (p *proxyAPI) publish(t *testing.T, proto string, contPort int, label string) publishResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"protocol":       proto,
		"container_port": contPort,
		"label":          label,
	})
	req, _ := http.NewRequest("POST", p.baseURL+"/api/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", p.token)
	resp, err := p.http.Do(req)
	if err != nil {
		t.Fatalf("POST /api/publish: %v", err)
	}
	defer resp.Body.Close()
	var out publishResult
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || !out.OK {
		t.Fatalf("publish failed: status=%d body=%+v", resp.StatusCode, out)
	}
	return out
}
```

- [ ] **Step 2: Run the test (requires images rebuilt)**

```
nix build .#claude-proxy-image && docker load -i result
nix build .#claude-container-image && docker load -i result
devenv shell -- go test ./cmd/ -run TestSecurity_Publish_TCP_RoundTrip -v
```

Expected: PASS — host-side fetch returns the container's file.

- [ ] **Step 3: Commit**

```
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E TCP publish round-trip via dashboard"
```

---

### Task 22: E2E test — UDP publish

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
// TestSecurity_Publish_UDP_RoundTrip is the UDP analog: publish a UDP
// port, send a datagram from the host, verify the in-container echo
// server received it.
func TestSecurity_Publish_UDP_RoundTrip(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-pub-udp"
	startSecurityContainer(t, name, "--yolo", "--publish-range=10")

	// Start a tiny UDP echo in the container, bound to 0.0.0.0:5005.
	go boundedDockerExec(t, 30*time.Second, name, "sh", "-c",
		`python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(('0.0.0.0', 5005))
data, addr = s.recvfrom(1024)
open('/tmp/got', 'w').write(data.decode())
"`)
	time.Sleep(2 * time.Second)

	api := newProxyAPI(t, configDir, name)
	pub := api.publish(t, "udp", 5005, "udp-echo")

	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", pub.HostPort))
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()
	conn.Write([]byte("HELLO UDP"))
	time.Sleep(1 * time.Second)

	got, _ := boundedDockerExec(t, 5*time.Second, name, "cat", "/tmp/got")
	if !strings.Contains(got, "HELLO UDP") {
		t.Errorf("container received %q, want HELLO UDP", got)
	}
}
```

- [ ] **Step 2: Run the test**

```
devenv shell -- go test ./cmd/ -run TestSecurity_Publish_UDP_RoundTrip -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E UDP publish round-trip via dashboard"
```

---

### Task 23: E2E test — concurrent sessions get non-overlapping ranges

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
// TestSecurity_Publish_ConcurrentSessions_NoCollision verifies two
// simultaneous sessions get non-overlapping ranges and can both publish.
func TestSecurity_Publish_ConcurrentSessions_NoCollision(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	nameA, nameB := "sec-pub-a", "sec-pub-b"
	startSecurityContainer(t, nameA, "--yolo", "--publish-range=10")
	startSecurityContainer(t, nameB, "--yolo", "--publish-range=10")

	// Inspect allocations file directly.
	data, err := os.ReadFile(filepath.Join(configDir,
		"claude-container", "published-port-allocations.json"))
	if err != nil {
		t.Fatalf("read alloc file: %v", err)
	}
	var allocs map[string]struct {
		Base int `json:"base"`
		Size int `json:"size"`
	}
	if err := json.Unmarshal(data, &allocs); err != nil {
		t.Fatalf("parse allocs: %v", err)
	}
	a, b := allocs[nameA], allocs[nameB]
	if a.Base == b.Base {
		t.Errorf("sessions %s and %s got the same base %d",
			nameA, nameB, a.Base)
	}
	if a.Base < b.Base && a.Base+a.Size-1 >= b.Base {
		t.Errorf("ranges overlap: A=%d-%d B=%d-%d",
			a.Base, a.Base+a.Size-1, b.Base, b.Base+b.Size-1)
	}
}
```

- [ ] **Step 2: Run the test**

```
devenv shell -- go test ./cmd/ -run TestSecurity_Publish_ConcurrentSessions_NoCollision -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E concurrent sessions get non-overlapping publish ranges"
```

---

### Task 24: E2E test — unpublish removes nft rule

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
// TestSecurity_Publish_Unpublish_RemovesFirewallRule verifies that
// unpublishing closes the port: a fresh curl after unpublish fails.
func TestSecurity_Publish_Unpublish_RemovesFirewallRule(t *testing.T) {
	requireDockerAndAuth(t)
	configDir := setupIsolatedConfigDir(t)

	name := "sec-unpub"
	startSecurityContainer(t, name, "--yolo", "--publish-range=10")
	go boundedDockerExec(t, 30*time.Second, name, "sh", "-c",
		"python3 -m http.server 3001 --bind 0.0.0.0 --directory /tmp")
	time.Sleep(2 * time.Second)

	api := newProxyAPI(t, configDir, name)
	pub := api.publish(t, "tcp", 3001, "")
	url := fmt.Sprintf("http://127.0.0.1:%d/", pub.HostPort)

	// Sanity: should reach the server.
	if _, err := http.Get(url); err != nil {
		t.Fatalf("pre-unpublish fetch failed: %v", err)
	}

	api.unpublish(t, "tcp", pub.HostPort)

	// Post-unpublish: should NOT reach the server (firewall dropped).
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Errorf("unpublish did not close the port — fetch returned %d", resp.StatusCode)
	}
}
```

Also add the unpublish helper:

```go
func (p *proxyAPI) unpublish(t *testing.T, proto string, hostPort int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"protocol":  proto,
		"host_port": hostPort,
	})
	req, _ := http.NewRequest("POST", p.baseURL+"/api/unpublish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", p.token)
	resp, err := p.http.Do(req)
	if err != nil {
		t.Fatalf("POST /api/unpublish: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("unpublish status=%d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run the test**

```
devenv shell -- go test ./cmd/ -run TestSecurity_Publish_Unpublish_RemovesFirewallRule -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add cmd/security_e2e_test.go
git commit -m "test(security): E2E unpublish closes the firewall hole"
```

---

### Task 25: E2E test — allocator exhaustion errors cleanly

**Files:**
- Modify: `cmd/security_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
// TestSecurity_Publish_PoolExhaustionFailsFast verifies that starting
// more sessions than the pool can fit fails fast with a clear error.
func TestSecurity_Publish_PoolExhaustionFailsFast(t *testing.T) {
	requireDockerAndAuth(t)
	setupIsolatedConfigDir(t)

	// Pool of 20 ports, 10 per session -> exactly 2 sessions, then
	// the third should fail.
	startSecurityContainer(t, "sec-exh-a", "--yolo",
		"--publish-range=10", "--publish-base=40000")
	startSecurityContainer(t, "sec-exh-b", "--yolo",
		"--publish-range=10", "--publish-base=40000")

	// Manual start since startSecurityContainer t.Fatals on non-zero.
	out, err := exec.Command(testBinary, "run", "-b",
		"--name", "sec-exh-c", "--preset", "sec-exh-c", "--yolo",
		"--publish-range=10", "--publish-base=40000").CombinedOutput()
	t.Cleanup(func() {
		runCLI(t, "rm", "sec-exh-c")
	})
	if err == nil {
		t.Errorf("third session should have failed; got success: %s", out)
	}
	if !strings.Contains(string(out), "pool") {
		t.Errorf("error message should mention pool exhaustion; got: %s", out)
	}
}
```

Note: this needs the pool size to actually be 20 — which requires the Allocator to take pool size as a config too. If your Task 12 implementation hardcoded `opts.PublishBase + 1000 - 1`, override for this test by setting both PublishBase and PublishRange. The Allocator's poolEnd default in Task 12 was `PublishBase + 1000 - 1`. To exhaust with 2 sessions of 10 we need a smaller pool. Adjust Task 12 to also expose `PublishPoolSize`, defaulting to 1000, and override in this test via env or a CLI flag. Simplest: add `--publish-pool-size N` flag and set N=20 here.

- [ ] **Step 2: Add `--publish-pool-size` flag**

In `cmd/root.go` (and analogous in run.go, work.go):

```go
	rootCmd.Flags().IntVar(&rootPublishPoolSize, "publish-pool-size", 0,
		"size of the inbound publish pool in ports (default 1000)")
```

In `internal/session/options.go`, add `PublishPoolSize int` to Opts, default 1000 in ApplyDefaults, and plumb it into the Allocator's `poolEnd = poolStart + PublishPoolSize - 1` in `launch.go`.

- [ ] **Step 3: Re-run test**

```
devenv shell -- go test ./cmd/ -run TestSecurity_Publish_PoolExhaustionFailsFast -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add cmd/ internal/session/options.go internal/session/launch.go
git commit -m "feat(session): --publish-pool-size; test: pool exhaustion fails fast"
```

---

### Task 26: README — document the new `--publish-*` flags

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a section under "Network proxy"**

Append after the existing proxy section:

```markdown
## Inbound port publishing

Use `claude-container --publish-range N --publish-base PORT` to reserve
a contiguous host port range for inbound traffic. Dynamic assignment of
specific ports is done via the **Published Ports** tab of the proxy
dashboard.

Defaults: 10 ports per session, pool starts at 30000.

The published port range is `127.0.0.1`-bound only — never reachable
from your LAN. Dev servers inside the container must bind to
`0.0.0.0:PORT`, not `127.0.0.1:PORT`, for the docker port forward to
reach them.

Flags:

- `--publish-range N` — ports per session (default 10)
- `--publish-base PORT` — first port the pool may use (default 30000)
- `--publish-pool-size N` — total pool size (default 1000)

To get a URL, open the dashboard, go to **Published Ports**, fill
container port and protocol, click Publish. Use the returned
`http://127.0.0.1:<assigned>` URL from your host browser.
```

- [ ] **Step 2: Commit**

```
git add README.md
git commit -m "docs(README): inbound port publishing usage + flags"
```

---

## Phase 1 boundary check

After Task 26, run the full security suite once to catch any regressions:

```
nix build .#claude-proxy-image && docker load -i result
nix build .#claude-container-image && docker load -i result
./scripts/run-security-tests.sh --llm
```

Expected: previously-passing tests still green (was 34/34 at start of plan), plus the 5 new publish tests (Tasks 21, 22, 23, 24, 25) passing → 39/39.

If any regression: the proxy entrypoint nft setup is the most likely failure point (Task 18). Bisect by inspecting `nft list ruleset` inside a running proxy container.

---

## Self-review checklist

- [x] Phase 0 covers all spec §6 schema requirements: new fields (1), normalization (2), canonical to_dict (3), match_request (4), addon migration (5), dashboard back-compat (6), regression (7).
- [x] Phase 1 covers all spec §7 requirements: per-session port-range allocation (8-10, 12, 25), TCP+UDP publish docker -p (11), publish-mgr daemon with /publish /unpublish /list (14-16), bake into image (17), firewall sub-chain (18), dashboard endpoints (19), UI (20), tests (21-25), docs (26).
- [x] No TODO/TBD strings in tasks.
- [x] Every test has concrete assertion code.
- [x] Type consistency: `PortRange` (Task 11) used consistently in Tasks 11/12; `PublishRange`/`PublishBase`/`PublishPoolSize` named consistently across Opts and CLI; `publishResp` struct field names match between publish-mgr Go and the dashboard Python deserialization.
- [x] Phases 2-4 explicitly listed as out of scope, with the design doc as the cross-reference for when those plans are written.

## Execution handoff

Plan complete and saved to `docs/plans/2026-05-22-dashboard-all-traffic-phase-0-1.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session via executing-plans, batch with checkpoints.

Which approach?
