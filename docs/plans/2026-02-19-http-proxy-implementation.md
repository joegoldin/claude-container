# HTTP/S Proxy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an interactive HTTP/HTTPS proxy sidecar that intercepts outbound requests from Claude containers, allowing users to interactively allow/deny domains via a web dashboard.

**Architecture:** A Python mitmproxy-based sidecar container runs alongside Claude containers on a shared Docker network. Proxy containers are shared per-profile — multiple Claude sessions using the same `--proxy-profile` reuse one proxy instance. The Go CLI orchestrates lifecycle (start/stop/reuse), and a web dashboard on localhost provides interactive allow/deny with regex-based rules and time-based expiry.

**Tech Stack:** Python 3 (mitmproxy, starlette, uvicorn, websockets), Go (CLI orchestration), Nix (Docker image build), HTML/JS/CSS (dashboard)

---

## Proxy Reuse Model

Proxy containers are named by profile, not by session:
- Container: `claude-proxy_<profile>`
- Network: `claude-proxy-net_<profile>`

When a session starts with `--network-sandbox=proxy --proxy-profile=myproject`:
1. Check if `claude-proxy_myproject` is already running → reuse it
2. If not → create network, start proxy, wait for health
3. Connect Claude container to the proxy's network

When a session stops or is removed:
1. Query session store for other sessions using the same proxy profile
2. If none remain → stop proxy, remove network
3. If others exist → leave proxy running

---

## Task 1: Python Proxy — Rule Engine (`proxy/claude_proxy/rules.py`)

**Files:**
- Create: `proxy/claude_proxy/__init__.py`
- Create: `proxy/claude_proxy/rules.py`
- Create: `proxy/tests/__init__.py`
- Create: `proxy/tests/test_rules.py`

**Step 1: Write the failing tests**

```python
# proxy/tests/test_rules.py
import json
import os
import tempfile
import time
from claude_proxy.rules import RuleStore, Rule

class TestRuleStore:
    def test_add_and_match_allow(self):
        store = RuleStore()
        store.add("allow", r"^https?://example\.com(/.*)?$", "example.com", None)
        assert store.match("https://example.com/foo") == "allow"

    def test_add_and_match_deny(self):
        store = RuleStore()
        store.add("deny", r"^https?://evil\.com(/.*)?$", "evil.com", None)
        assert store.match("https://evil.com/steal") == "deny"

    def test_no_match_returns_none(self):
        store = RuleStore()
        store.add("allow", r"^https?://example\.com(/.*)?$", "example.com", None)
        assert store.match("https://unknown.com") is None

    def test_deny_takes_priority_over_allow(self):
        store = RuleStore()
        store.add("allow", r"^https?://example\.com(/.*)?$", "example.com", None)
        store.add("deny", r"^https?://example\.com/admin(/.*)?$", "example.com/admin", None)
        assert store.match("https://example.com/admin/secrets") == "deny"

    def test_expired_rule_ignored(self):
        store = RuleStore()
        # Expired 1 second ago
        store.add("allow", r"^https?://expired\.com(/.*)?$", "expired.com",
                  time.time() - 1)
        assert store.match("https://expired.com") is None

    def test_future_expiry_still_matches(self):
        store = RuleStore()
        store.add("allow", r"^https?://future\.com(/.*)?$", "future.com",
                  time.time() + 3600)
        assert store.match("https://future.com") == "allow"

    def test_remove_rule(self):
        store = RuleStore()
        rule_id = store.add("allow", r"^https?://example\.com(/.*)?$", "example.com", None)
        store.remove(rule_id)
        assert store.match("https://example.com") is None

    def test_list_rules(self):
        store = RuleStore()
        store.add("allow", r"^https?://a\.com(/.*)?$", "a.com", None)
        store.add("deny", r"^https?://b\.com(/.*)?$", "b.com", None)
        rules = store.list_rules()
        assert len(rules) == 2

    def test_save_and_load(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "test-profile.json")
            store = RuleStore()
            store.add("allow", r"^https?://saved\.com(/.*)?$", "saved.com", None)
            store.save(path)

            store2 = RuleStore()
            store2.load(path)
            assert store2.match("https://saved.com") == "allow"

    def test_cleanup_expired(self):
        store = RuleStore()
        store.add("allow", r"^https?://expired\.com(/.*)?$", "expired.com",
                  time.time() - 1)
        store.add("allow", r"^https?://valid\.com(/.*)?$", "valid.com", None)
        store.cleanup_expired()
        assert len(store.list_rules()) == 1
```

**Step 2: Run tests to verify they fail**

Run: `cd proxy && python -m pytest tests/test_rules.py -v`
Expected: ImportError — `claude_proxy.rules` doesn't exist

**Step 3: Write the implementation**

```python
# proxy/claude_proxy/__init__.py
# (empty)
```

```python
# proxy/claude_proxy/rules.py
"""Thread-safe rule store with regex matching, expiry, and JSON persistence."""

import json
import re
import threading
import time
import uuid
from dataclasses import dataclass, field, asdict
from typing import Optional


@dataclass
class Rule:
    id: str
    rule_type: str  # "allow" or "deny"
    pattern: str  # regex
    label: str
    created_at: float  # unix timestamp
    expires_at: Optional[float]  # unix timestamp or None for forever
    source: str = "interactive"
    _compiled: Optional[re.Pattern] = field(default=None, repr=False, compare=False)

    def is_expired(self) -> bool:
        if self.expires_at is None:
            return False
        return time.time() > self.expires_at

    def compiled(self) -> re.Pattern:
        if self._compiled is None:
            self._compiled = re.compile(self.pattern)
        return self._compiled

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "type": self.rule_type,
            "pattern": self.pattern,
            "label": self.label,
            "created_at": self.created_at,
            "expires_at": self.expires_at,
            "source": self.source,
        }

    @classmethod
    def from_dict(cls, d: dict) -> "Rule":
        return cls(
            id=d["id"],
            rule_type=d["type"],
            pattern=d["pattern"],
            label=d["label"],
            created_at=d["created_at"],
            expires_at=d.get("expires_at"),
            source=d.get("source", "interactive"),
        )


class RuleStore:
    """Thread-safe collection of allow/deny rules with regex matching."""

    def __init__(self):
        self._rules: list[Rule] = []
        self._lock = threading.Lock()

    def add(
        self,
        rule_type: str,
        pattern: str,
        label: str,
        expires_at: Optional[float],
        source: str = "interactive",
    ) -> str:
        rule_id = str(uuid.uuid4())
        rule = Rule(
            id=rule_id,
            rule_type=rule_type,
            pattern=pattern,
            label=label,
            created_at=time.time(),
            expires_at=expires_at,
            source=source,
        )
        # Validate regex compiles
        rule.compiled()
        with self._lock:
            self._rules.append(rule)
        return rule_id

    def remove(self, rule_id: str) -> bool:
        with self._lock:
            before = len(self._rules)
            self._rules = [r for r in self._rules if r.id != rule_id]
            return len(self._rules) < before

    def match(self, url: str) -> Optional[str]:
        """Check url against rules. Returns 'allow', 'deny', or None.
        Deny rules take priority over allow rules."""
        with self._lock:
            rules = list(self._rules)

        result = None
        for rule in rules:
            if rule.is_expired():
                continue
            if rule.compiled().search(url):
                if rule.rule_type == "deny":
                    return "deny"
                if result is None:
                    result = "allow"
        return result

    def list_rules(self) -> list[dict]:
        with self._lock:
            return [r.to_dict() for r in self._rules if not r.is_expired()]

    def cleanup_expired(self):
        with self._lock:
            self._rules = [r for r in self._rules if not r.is_expired()]

    def save(self, path: str):
        with self._lock:
            data = [r.to_dict() for r in self._rules]
        with open(path, "w") as f:
            json.dump(data, f, indent=2)

    def load(self, path: str):
        with open(path) as f:
            data = json.load(f)
        rules = [Rule.from_dict(d) for d in data]
        with self._lock:
            self._rules = rules
```

**Step 4: Run tests to verify they pass**

Run: `cd proxy && python -m pytest tests/test_rules.py -v`
Expected: All 10 tests PASS

**Step 5: Commit**

```bash
git add proxy/claude_proxy/__init__.py proxy/claude_proxy/rules.py \
       proxy/tests/__init__.py proxy/tests/test_rules.py
git commit -m "feat(proxy): add rule engine with regex matching and expiry"
```

---

## Task 2: Python Proxy — Pattern Generators (`proxy/claude_proxy/patterns.py`)

**Files:**
- Create: `proxy/claude_proxy/patterns.py`
- Create: `proxy/tests/test_patterns.py`

**Step 1: Write the failing tests**

```python
# proxy/tests/test_patterns.py
import re
from claude_proxy.patterns import exact_url, url_no_params, subdomain_pattern, base_domain

class TestExactUrl:
    def test_matches_exact(self):
        p = exact_url("https://api.github.com/repos?page=1")
        assert re.search(p, "https://api.github.com/repos?page=1")

    def test_rejects_different_params(self):
        p = exact_url("https://api.github.com/repos?page=1")
        assert not re.search(p, "https://api.github.com/repos?page=2")

    def test_escapes_special_chars(self):
        p = exact_url("https://example.com/foo.bar?a=1&b=2")
        assert re.search(p, "https://example.com/foo.bar?a=1&b=2")
        assert not re.search(p, "https://example.com/fooXbar?a=1&b=2")

class TestUrlNoParams:
    def test_matches_without_params(self):
        p = url_no_params("https://api.github.com/repos")
        assert re.search(p, "https://api.github.com/repos")

    def test_matches_with_any_params(self):
        p = url_no_params("https://api.github.com/repos")
        assert re.search(p, "https://api.github.com/repos?page=5")

    def test_rejects_different_path(self):
        p = url_no_params("https://api.github.com/repos")
        assert not re.search(p, "https://api.github.com/users")

class TestSubdomainPattern:
    def test_matches_subdomain(self):
        p = subdomain_pattern("api.github.com")
        assert re.search(p, "https://api.github.com/anything")

    def test_rejects_different_subdomain(self):
        p = subdomain_pattern("api.github.com")
        assert not re.search(p, "https://www.github.com/anything")

    def test_matches_http_and_https(self):
        p = subdomain_pattern("api.github.com")
        assert re.search(p, "http://api.github.com/foo")
        assert re.search(p, "https://api.github.com/foo")

class TestBaseDomain:
    def test_matches_bare_domain(self):
        p = base_domain("github.com")
        assert re.search(p, "https://github.com/repo")

    def test_matches_any_subdomain(self):
        p = base_domain("github.com")
        assert re.search(p, "https://api.github.com/repo")
        assert re.search(p, "https://raw.githubusercontent.github.com/file")

    def test_rejects_different_domain(self):
        p = base_domain("github.com")
        assert not re.search(p, "https://gitlab.com/repo")

    def test_does_not_match_suffix(self):
        p = base_domain("hub.com")
        assert not re.search(p, "https://github.com/repo")
```

**Step 2: Run tests to verify they fail**

Run: `cd proxy && python -m pytest tests/test_patterns.py -v`
Expected: ImportError

**Step 3: Write the implementation**

```python
# proxy/claude_proxy/patterns.py
"""Generate regex patterns from URL/domain presets."""

import re
from urllib.parse import urlparse


def _escape(s: str) -> str:
    """Escape a string for use in a regex pattern."""
    return re.escape(s)


def exact_url(url: str) -> str:
    """Match this exact URL and nothing else."""
    return f"^{_escape(url)}$"


def url_no_params(url: str) -> str:
    """Match this URL with or without query parameters."""
    # Strip existing params
    parsed = urlparse(url)
    base = f"{parsed.scheme}://{parsed.netloc}{parsed.path}"
    return f"^{_escape(base)}(\\?.*)?$"


def subdomain_pattern(host: str) -> str:
    """Match this exact host (subdomain.domain.tld) on http or https."""
    return f"^https?://{_escape(host)}(/.*)?$"


def base_domain(domain: str) -> str:
    """Match this domain and all subdomains on http or https."""
    return f"^https?://([^/]*\\.)?{_escape(domain)}(/.*)?$"
```

**Step 4: Run tests to verify they pass**

Run: `cd proxy && python -m pytest tests/test_patterns.py -v`
Expected: All 12 tests PASS

**Step 5: Commit**

```bash
git add proxy/claude_proxy/patterns.py proxy/tests/test_patterns.py
git commit -m "feat(proxy): add regex pattern generators for URL presets"
```

---

## Task 3: Python Proxy — mitmproxy Addon (`proxy/claude_proxy/addon.py`)

**Files:**
- Create: `proxy/claude_proxy/addon.py`
- Create: `proxy/tests/test_addon.py`

**Step 1: Write the failing tests**

```python
# proxy/tests/test_addon.py
import asyncio
import time
from unittest.mock import MagicMock, patch
from claude_proxy.addon import ProxyAddon
from claude_proxy.rules import RuleStore


def make_flow(url: str, host: str = "example.com", port: int = 443):
    """Create a mock mitmproxy flow."""
    flow = MagicMock()
    flow.request.pretty_url = url
    flow.request.host = host
    flow.request.port = port
    flow.request.url = url
    flow.id = f"flow-{id(flow)}"
    flow.killable = True
    return flow


class TestProxyAddon:
    def test_allowed_domain_passes(self):
        store = RuleStore()
        store.add("allow", r"^https?://allowed\.com(/.*)?$", "allowed.com", None)
        addon = ProxyAddon(store)

        flow = make_flow("https://allowed.com/path")
        addon.request(flow)

        flow.kill.assert_not_called()
        assert flow.id not in addon.pending

    def test_denied_domain_killed(self):
        store = RuleStore()
        store.add("deny", r"^https?://denied\.com(/.*)?$", "denied.com", None)
        addon = ProxyAddon(store)

        flow = make_flow("https://denied.com/path")
        addon.request(flow)

        flow.kill.assert_called_once()
        assert flow.id not in addon.pending

    def test_unknown_domain_held(self):
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = make_flow("https://unknown.com/path")
        addon.request(flow)

        assert flow.id in addon.pending

    def test_resolve_allow_releases_flow(self):
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = make_flow("https://unknown.com/path")
        addon.request(flow)
        assert flow.id in addon.pending

        addon.resolve(flow.id, "allow", r"^https?://unknown\.com(/.*)?$",
                      "unknown.com", None)

        assert flow.id not in addon.pending
        assert store.match("https://unknown.com") == "allow"

    def test_resolve_deny_kills_flow(self):
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = make_flow("https://unknown.com/path")
        addon.request(flow)

        addon.resolve(flow.id, "deny", r"^https?://unknown\.com(/.*)?$",
                      "unknown.com", None)

        flow.kill.assert_called_once()
        assert flow.id not in addon.pending
        assert store.match("https://unknown.com") == "deny"

    def test_pending_list(self):
        store = RuleStore()
        addon = ProxyAddon(store)

        f1 = make_flow("https://a.com/x", host="a.com")
        f2 = make_flow("https://b.com/y", host="b.com")
        addon.request(f1)
        addon.request(f2)

        pending = addon.get_pending()
        assert len(pending) == 2
        urls = {p["url"] for p in pending}
        assert "https://a.com/x" in urls
        assert "https://b.com/y" in urls
```

**Step 2: Run tests to verify they fail**

Run: `cd proxy && python -m pytest tests/test_addon.py -v`
Expected: ImportError

**Step 3: Write the implementation**

```python
# proxy/claude_proxy/addon.py
"""mitmproxy addon that checks requests against the rule store."""

import logging
import time
import threading
from typing import Optional, Callable

from claude_proxy.rules import RuleStore

log = logging.getLogger(__name__)


class ProxyAddon:
    """mitmproxy addon that intercepts requests and checks against rules.

    Unknown domains are held in `pending` until resolved via the dashboard.
    """

    def __init__(
        self,
        rule_store: RuleStore,
        on_pending: Optional[Callable[[dict], None]] = None,
        hold_timeout: int = 120,
    ):
        self.store = rule_store
        self.on_pending = on_pending  # callback for WebSocket notifications
        self.hold_timeout = hold_timeout
        self.pending: dict[str, dict] = {}  # flow_id -> {flow, url, host, time}
        self._lock = threading.Lock()

    def request(self, flow) -> None:
        url = flow.request.pretty_url
        result = self.store.match(url)

        if result == "allow":
            return
        if result == "deny":
            flow.kill()
            log.info("Denied: %s", url)
            return

        # Unknown — hold the flow
        entry = {
            "flow": flow,
            "url": url,
            "host": flow.request.host,
            "time": time.time(),
        }
        with self._lock:
            self.pending[flow.id] = entry
        log.info("Holding: %s (flow %s)", url, flow.id)

        if self.on_pending:
            self.on_pending({
                "flow_id": flow.id,
                "url": url,
                "host": flow.request.host,
                "time": entry["time"],
            })

    def resolve(
        self,
        flow_id: str,
        action: str,
        pattern: str,
        label: str,
        expires_at: Optional[float],
    ) -> bool:
        """Resolve a pending flow. Returns True if found and resolved."""
        with self._lock:
            entry = self.pending.pop(flow_id, None)
        if entry is None:
            return False

        self.store.add(action, pattern, label, expires_at)

        if action == "deny":
            entry["flow"].kill()
            log.info("User denied: %s", entry["url"])
        else:
            # Allow — flow continues naturally (just release the hold)
            log.info("User allowed: %s", entry["url"])

        return True

    def get_pending(self) -> list[dict]:
        """Return list of pending requests for the dashboard."""
        with self._lock:
            return [
                {
                    "flow_id": fid,
                    "url": e["url"],
                    "host": e["host"],
                    "time": e["time"],
                }
                for fid, e in self.pending.items()
            ]

    def cleanup_timed_out(self) -> list[str]:
        """Kill flows that have been pending longer than hold_timeout."""
        now = time.time()
        timed_out = []
        with self._lock:
            for fid, entry in list(self.pending.items()):
                if now - entry["time"] > self.hold_timeout:
                    entry["flow"].kill()
                    del self.pending[fid]
                    timed_out.append(fid)
                    log.info("Timeout: %s", entry["url"])
        return timed_out
```

**Step 4: Run tests to verify they pass**

Run: `cd proxy && python -m pytest tests/test_addon.py -v`
Expected: All 6 tests PASS

**Step 5: Commit**

```bash
git add proxy/claude_proxy/addon.py proxy/tests/test_addon.py
git commit -m "feat(proxy): add mitmproxy addon with hold/resolve flow"
```

---

## Task 4: Python Proxy — Web Dashboard Backend (`proxy/claude_proxy/dashboard.py`)

**Files:**
- Create: `proxy/claude_proxy/dashboard.py`

This task has no unit tests — it's a thin HTTP/WebSocket layer over the addon and rule store. It will be tested via integration tests in Task 6.

**Step 1: Write the implementation**

```python
# proxy/claude_proxy/dashboard.py
"""Starlette web dashboard with REST API and WebSocket for real-time notifications."""

import asyncio
import json
import logging
import os
import time
from typing import Optional

from starlette.applications import Starlette
from starlette.responses import JSONResponse, HTMLResponse, Response
from starlette.routing import Route, WebSocketRoute
from starlette.staticfiles import StaticFiles
from starlette.websockets import WebSocket, WebSocketDisconnect

from claude_proxy.addon import ProxyAddon
from claude_proxy.rules import RuleStore

log = logging.getLogger(__name__)

# Global references set by app.py at startup
_addon: Optional[ProxyAddon] = None
_store: Optional[RuleStore] = None
_profile_path: Optional[str] = None
_ws_clients: set[WebSocket] = set()


def configure(addon: ProxyAddon, store: RuleStore, profile_path: str):
    global _addon, _store, _profile_path
    _addon = addon
    _store = store
    _profile_path = profile_path


async def broadcast(message: dict):
    """Send a message to all connected WebSocket clients."""
    data = json.dumps(message)
    disconnected = set()
    for ws in _ws_clients:
        try:
            await ws.send_text(data)
        except Exception:
            disconnected.add(ws)
    _ws_clients.difference_update(disconnected)


def on_pending_request(info: dict):
    """Callback from addon when a new request is held."""
    asyncio.get_event_loop().call_soon_threadsafe(
        asyncio.ensure_future,
        broadcast({"type": "pending", "data": info}),
    )


# --- Routes ---

async def health(request):
    return JSONResponse({"status": "ok"})


async def get_pending(request):
    return JSONResponse(_addon.get_pending())


async def get_rules(request):
    return JSONResponse(_store.list_rules())


async def add_rule(request):
    body = await request.json()
    rule_id = _store.add(
        rule_type=body["type"],
        pattern=body["pattern"],
        label=body.get("label", ""),
        expires_at=body.get("expires_at"),
        source=body.get("source", "api"),
    )
    _store.save(_profile_path)
    return JSONResponse({"id": rule_id}, status_code=201)


async def delete_rule(request):
    rule_id = request.path_params["rule_id"]
    removed = _store.remove(rule_id)
    if removed:
        _store.save(_profile_path)
    return JSONResponse({"removed": removed})


async def resolve_pending(request):
    """Resolve a pending flow (allow or deny)."""
    body = await request.json()
    flow_id = body["flow_id"]
    action = body["action"]  # "allow" or "deny"
    pattern = body["pattern"]
    label = body.get("label", "")
    expires_at = body.get("expires_at")

    resolved = _addon.resolve(flow_id, action, pattern, label, expires_at)
    if resolved:
        _store.save(_profile_path)
        await broadcast({"type": "resolved", "data": {"flow_id": flow_id, "action": action}})
    return JSONResponse({"resolved": resolved})


async def websocket_endpoint(websocket: WebSocket):
    await websocket.accept()
    _ws_clients.add(websocket)
    try:
        # Send current pending on connect
        await websocket.send_text(json.dumps({
            "type": "init",
            "data": {"pending": _addon.get_pending(), "rules": _store.list_rules()},
        }))
        # Keep connection alive, handle incoming messages
        while True:
            data = await websocket.receive_text()
            msg = json.loads(data)
            if msg.get("type") == "resolve":
                d = msg["data"]
                resolved = _addon.resolve(
                    d["flow_id"], d["action"], d["pattern"],
                    d.get("label", ""), d.get("expires_at"),
                )
                if resolved:
                    _store.save(_profile_path)
                    await broadcast({
                        "type": "resolved",
                        "data": {"flow_id": d["flow_id"], "action": d["action"]},
                    })
    except WebSocketDisconnect:
        pass
    finally:
        _ws_clients.discard(websocket)


async def index(request):
    static_dir = os.path.join(os.path.dirname(__file__), "..", "static")
    index_path = os.path.join(static_dir, "index.html")
    with open(index_path) as f:
        return HTMLResponse(f.read())


routes = [
    Route("/", index),
    Route("/api/health", health),
    Route("/api/pending", get_pending),
    Route("/api/rules", get_rules, methods=["GET"]),
    Route("/api/rules", add_rule, methods=["POST"]),
    Route("/api/rules/{rule_id}", delete_rule, methods=["DELETE"]),
    Route("/api/resolve", resolve_pending, methods=["POST"]),
    WebSocketRoute("/ws", websocket_endpoint),
]

app = Starlette(routes=routes)
```

**Step 2: Commit**

```bash
git add proxy/claude_proxy/dashboard.py
git commit -m "feat(proxy): add web dashboard backend with REST API and WebSocket"
```

---

## Task 5: Python Proxy — App Entry Point (`proxy/claude_proxy/app.py`)

**Files:**
- Create: `proxy/claude_proxy/app.py`
- Create: `proxy/pyproject.toml`

**Step 1: Write pyproject.toml**

```toml
# proxy/pyproject.toml
[project]
name = "claude-proxy"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = [
    "mitmproxy>=10.0",
    "starlette>=0.37",
    "uvicorn[standard]>=0.29",
    "websockets>=12.0",
]

[project.optional-dependencies]
dev = ["pytest>=8.0"]

[tool.pytest.ini_options]
testpaths = ["tests"]
```

**Step 2: Write app.py**

```python
# proxy/claude_proxy/app.py
"""Entry point: runs mitmproxy and the web dashboard in a single process."""

import argparse
import asyncio
import logging
import os
import signal
import sys
import threading

from mitmproxy import options as moptions
from mitmproxy.tools.dump import DumpMaster

import uvicorn

from claude_proxy.addon import ProxyAddon
from claude_proxy.dashboard import app as dashboard_app, configure, on_pending_request
from claude_proxy.rules import RuleStore

log = logging.getLogger("claude_proxy")


def parse_args():
    parser = argparse.ArgumentParser(description="Claude HTTP/S Proxy")
    parser.add_argument("--profile", default="default",
                        help="Profile name for rule persistence")
    parser.add_argument("--config-dir", default="/config",
                        help="Directory for profile storage and CA certs")
    parser.add_argument("--proxy-port", type=int, default=8080,
                        help="Port for the proxy server")
    parser.add_argument("--dashboard-port", type=int, default=8081,
                        help="Port for the web dashboard")
    parser.add_argument("--hold-timeout", type=int, default=120,
                        help="Seconds to hold unknown requests before timeout")
    return parser.parse_args()


def run_dashboard(port: int):
    """Run the dashboard web server in a thread."""
    uvicorn.run(dashboard_app, host="0.0.0.0", port=port,
                log_level="warning", access_log=False)


async def run_proxy(args):
    """Run mitmproxy with our addon."""
    # Set up profile storage
    profile_dir = os.path.join(args.config_dir, "profiles")
    os.makedirs(profile_dir, exist_ok=True)
    profile_path = os.path.join(profile_dir, f"{args.profile}.json")

    # Load or create rule store
    store = RuleStore()
    if os.path.exists(profile_path):
        store.load(profile_path)
        log.info("Loaded profile %r from %s", args.profile, profile_path)
    else:
        log.info("New profile %r at %s", args.profile, profile_path)

    # Create addon
    addon = ProxyAddon(store, on_pending=on_pending_request,
                       hold_timeout=args.hold_timeout)

    # Configure dashboard
    configure(addon, store, profile_path)

    # Start dashboard in background thread
    dashboard_thread = threading.Thread(
        target=run_dashboard, args=(args.dashboard_port,), daemon=True)
    dashboard_thread.start()
    log.info("Dashboard at http://0.0.0.0:%d", args.dashboard_port)

    # Start mitmproxy
    # CA certs are stored in config_dir/ca/ by mitmproxy
    ca_dir = os.path.join(args.config_dir, "ca")
    os.makedirs(ca_dir, exist_ok=True)

    opts = moptions.Options(
        listen_host="0.0.0.0",
        listen_port=args.proxy_port,
        confdir=ca_dir,
        ssl_insecure=False,
    )

    master = DumpMaster(opts)
    master.addons.add(addon)

    log.info("Proxy listening on 0.0.0.0:%d", args.proxy_port)

    # Periodic cleanup of timed-out flows and expired rules
    async def cleanup_loop():
        while True:
            await asyncio.sleep(10)
            addon.cleanup_timed_out()
            store.cleanup_expired()

    asyncio.ensure_future(cleanup_loop())

    try:
        await master.run()
    except KeyboardInterrupt:
        master.shutdown()


def main():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )
    args = parse_args()
    asyncio.run(run_proxy(args))


if __name__ == "__main__":
    main()
```

**Step 3: Commit**

```bash
git add proxy/pyproject.toml proxy/claude_proxy/app.py
git commit -m "feat(proxy): add app entry point running mitmproxy + dashboard"
```

---

## Task 6: Web Dashboard Frontend (`proxy/static/`)

**Files:**
- Create: `proxy/static/index.html`
- Create: `proxy/static/app.js`
- Create: `proxy/static/style.css`

**Step 1: Write index.html**

```html
<!-- proxy/static/index.html -->
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Claude Proxy Dashboard</title>
  <link rel="stylesheet" href="/static/style.css">
</head>
<body>
  <header>
    <h1>Claude Proxy</h1>
    <nav>
      <button class="tab active" data-view="pending">Pending</button>
      <button class="tab" data-view="rules">Rules</button>
    </nav>
  </header>

  <main>
    <section id="pending-view" class="view active">
      <div id="pending-list"></div>
      <p id="no-pending" class="muted">No pending requests.</p>
    </section>

    <section id="rules-view" class="view">
      <table id="rules-table">
        <thead>
          <tr>
            <th>Type</th><th>Label</th><th>Pattern</th><th>Expires</th><th></th>
          </tr>
        </thead>
        <tbody id="rules-body"></tbody>
      </table>
    </section>
  </main>

  <script src="/static/app.js"></script>
</body>
</html>
```

**Step 2: Write app.js**

```javascript
// proxy/static/app.js
const ws = new WebSocket(`ws://${location.host}/ws`);
let pending = [];
let rules = [];

// Browser notifications
if ("Notification" in window && Notification.permission === "default") {
  Notification.requestPermission();
}

function notify(title, body) {
  if ("Notification" in window && Notification.permission === "granted") {
    new Notification(title, { body });
  }
}

// Tabs
document.querySelectorAll(".tab").forEach(tab => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach(t => t.classList.remove("active"));
    document.querySelectorAll(".view").forEach(v => v.classList.remove("active"));
    tab.classList.add("active");
    document.getElementById(`${tab.dataset.view}-view`).classList.add("active");
  });
});

// Duration options
const DURATIONS = [
  { label: "Forever", value: null },
  { label: "15 minutes", value: 15 * 60 },
  { label: "1 hour", value: 3600 },
  { label: "1 day", value: 86400 },
  { label: "1 week", value: 604800 },
  { label: "1 month", value: 2592000 },
];

function escapeRegex(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function patternOptions(url, host) {
  const parsed = new URL(url);
  const baseDomain = host.split(".").slice(-2).join(".");
  return [
    { label: "Exact URL", pattern: `^${escapeRegex(url)}$` },
    { label: "URL (no params)", pattern: `^${escapeRegex(parsed.origin + parsed.pathname)}(\\?.*)?$` },
    { label: `Subdomain (${host})`, pattern: `^https?://${escapeRegex(host)}(/.*)?$` },
    { label: `Base domain (${baseDomain})`, pattern: `^https?://([^/]*\\.)?${escapeRegex(baseDomain)}(/.*)?$` },
  ];
}

function renderPending() {
  const list = document.getElementById("pending-list");
  const noMsg = document.getElementById("no-pending");
  list.innerHTML = "";
  noMsg.style.display = pending.length === 0 ? "block" : "none";

  pending.forEach(p => {
    const elapsed = Math.floor((Date.now() / 1000) - p.time);
    const remaining = Math.max(0, 120 - elapsed);
    let host;
    try { host = new URL(p.url).hostname; } catch { host = p.host; }
    const opts = patternOptions(p.url, host);

    const card = document.createElement("div");
    card.className = "card";
    card.dataset.flowId = p.flow_id;
    card.innerHTML = `
      <div class="card-header">
        <strong>${host}</strong>
        <span class="timer">${remaining}s</span>
      </div>
      <div class="card-url">${p.url}</div>
      <div class="card-options">
        <label>Pattern:</label>
        <select class="pattern-select">
          ${opts.map((o, i) => `<option value="${o.pattern}" ${i === 3 ? "selected" : ""}>${o.label}</option>`).join("")}
          <option value="custom">Custom regex</option>
        </select>
        <input class="custom-regex" type="text" placeholder="Custom regex" style="display:none">
        <label>Duration:</label>
        <select class="duration-select">
          ${DURATIONS.map(d => `<option value="${d.value}">${d.label}</option>`).join("")}
        </select>
      </div>
      <div class="card-actions">
        <button class="btn allow" data-flow-id="${p.flow_id}">Allow</button>
        <button class="btn deny" data-flow-id="${p.flow_id}">Deny</button>
      </div>
    `;

    // Show custom regex input when selected
    const sel = card.querySelector(".pattern-select");
    const customInput = card.querySelector(".custom-regex");
    sel.addEventListener("change", () => {
      customInput.style.display = sel.value === "custom" ? "block" : "none";
    });

    // Allow/Deny buttons
    card.querySelector(".btn.allow").addEventListener("click", () => resolve(p, card, "allow"));
    card.querySelector(".btn.deny").addEventListener("click", () => resolve(p, card, "deny"));

    list.appendChild(card);
  });
}

function resolve(p, card, action) {
  const sel = card.querySelector(".pattern-select");
  let pattern = sel.value;
  if (pattern === "custom") {
    pattern = card.querySelector(".custom-regex").value;
  }
  const label = sel.options[sel.selectedIndex].text;
  const durVal = card.querySelector(".duration-select").value;
  const expiresAt = durVal === "null" ? null : (Date.now() / 1000) + Number(durVal);

  ws.send(JSON.stringify({
    type: "resolve",
    data: { flow_id: p.flow_id, action, pattern, label, expires_at: expiresAt },
  }));
}

function renderRules() {
  const tbody = document.getElementById("rules-body");
  tbody.innerHTML = "";
  rules.forEach(r => {
    const tr = document.createElement("tr");
    const exp = r.expires_at ? new Date(r.expires_at * 1000).toLocaleString() : "never";
    tr.innerHTML = `
      <td class="${r.type}">${r.type}</td>
      <td>${r.label}</td>
      <td class="mono">${r.pattern}</td>
      <td>${exp}</td>
      <td><button class="btn-sm delete" data-id="${r.id}">Delete</button></td>
    `;
    tr.querySelector(".delete").addEventListener("click", async () => {
      await fetch(`/api/rules/${r.id}`, { method: "DELETE" });
      rules = rules.filter(x => x.id !== r.id);
      renderRules();
    });
    tbody.appendChild(tr);
  });
}

// WebSocket handlers
ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === "init") {
    pending = msg.data.pending;
    rules = msg.data.rules;
    renderPending();
    renderRules();
  } else if (msg.type === "pending") {
    pending.push(msg.data);
    renderPending();
    notify("Claude Proxy", `New request to ${msg.data.host}`);
  } else if (msg.type === "resolved") {
    pending = pending.filter(p => p.flow_id !== msg.data.flow_id);
    renderPending();
    // Refresh rules
    fetch("/api/rules").then(r => r.json()).then(r => { rules = r; renderRules(); });
  }
};

// Timer refresh
setInterval(() => {
  document.querySelectorAll(".card").forEach(card => {
    const fid = card.dataset.flowId;
    const p = pending.find(x => x.flow_id === fid);
    if (p) {
      const remaining = Math.max(0, 120 - Math.floor(Date.now() / 1000 - p.time));
      card.querySelector(".timer").textContent = `${remaining}s`;
    }
  });
}, 1000);
```

**Step 3: Write style.css**

```css
/* proxy/static/style.css */
* { box-sizing: border-box; margin: 0; padding: 0; }

body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  background: #1a1a2e; color: #e0e0e0; max-width: 900px; margin: 0 auto; padding: 1rem;
}

header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 1rem; }
h1 { font-size: 1.2rem; color: #7c83ff; }
nav { display: flex; gap: 0.5rem; }

.tab {
  background: #16213e; border: 1px solid #333; color: #aaa; padding: 0.4rem 1rem;
  cursor: pointer; border-radius: 4px; font-size: 0.85rem;
}
.tab.active { background: #7c83ff; color: #fff; border-color: #7c83ff; }

.view { display: none; }
.view.active { display: block; }
.muted { color: #666; text-align: center; padding: 2rem; }

.card {
  background: #16213e; border: 1px solid #333; border-radius: 6px;
  padding: 1rem; margin-bottom: 0.75rem;
}
.card-header { display: flex; justify-content: space-between; margin-bottom: 0.5rem; }
.card-header strong { color: #ff9f43; }
.timer { color: #ee5a24; font-weight: bold; }
.card-url { font-size: 0.8rem; color: #888; word-break: break-all; margin-bottom: 0.75rem; }
.card-options { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; margin-bottom: 0.75rem; }
.card-options label { font-size: 0.8rem; color: #aaa; }
.card-options select, .card-options input {
  background: #0f3460; border: 1px solid #444; color: #e0e0e0;
  padding: 0.3rem 0.5rem; border-radius: 4px; font-size: 0.8rem;
}
.custom-regex { width: 100%; }
.card-actions { display: flex; gap: 0.5rem; }

.btn {
  padding: 0.4rem 1.5rem; border: none; border-radius: 4px; cursor: pointer;
  font-weight: bold; font-size: 0.85rem;
}
.btn.allow { background: #2ecc71; color: #000; }
.btn.deny { background: #e74c3c; color: #fff; }
.btn-sm { padding: 0.2rem 0.6rem; font-size: 0.75rem; border: none; border-radius: 3px; cursor: pointer; }
.btn-sm.delete { background: #e74c3c; color: #fff; }

table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 0.5rem; border-bottom: 1px solid #333; font-size: 0.85rem; }
th { color: #7c83ff; }
.mono { font-family: monospace; font-size: 0.75rem; color: #888; }
.allow { color: #2ecc71; }
.deny { color: #e74c3c; }
```

**Step 4: Commit**

```bash
git add proxy/static/
git commit -m "feat(proxy): add web dashboard frontend with WebSocket and notifications"
```

---

## Task 7: Go — Proxy Lifecycle Management (`internal/httpproxy/httpproxy.go`)

This is the Go package that manages proxy sidecar containers. Key responsibility: **reuse proxies with the same profile**.

**Files:**
- Create: `internal/httpproxy/httpproxy.go`
- Create: `internal/httpproxy/httpproxy_test.go`

**Step 1: Write the failing tests**

```go
// internal/httpproxy/httpproxy_test.go
package httpproxy

import (
	"testing"
)

func TestProxyContainerName(t *testing.T) {
	got := ContainerName("myprofile")
	want := "claude-proxy_myprofile"
	if got != want {
		t.Errorf("ContainerName(%q) = %q, want %q", "myprofile", got, want)
	}
}

func TestNetworkName(t *testing.T) {
	got := NetworkName("myprofile")
	want := "claude-proxy-net_myprofile"
	if got != want {
		t.Errorf("NetworkName(%q) = %q, want %q", "myprofile", got, want)
	}
}

func TestRunArgs(t *testing.T) {
	args := RunArgs(ProxyOpts{
		Profile:       "default",
		ConfigDir:     "/home/user/.config/claude-container",
		DashboardPort: 8081,
	})

	joined := ""
	for _, a := range args {
		joined += a + " "
	}

	// Container name
	if !contains(args, "claude-proxy_default") {
		t.Errorf("missing container name in %v", args)
	}

	// Network
	if !contains(args, "claude-proxy-net_default") {
		t.Errorf("missing network name in %v", args)
	}

	// Dashboard port mapping
	if !containsArg(args, "-p", "8081:8081") {
		t.Errorf("missing dashboard port mapping in %v", args)
	}

	// Config volume
	if !containsVolume(args, "/home/user/.config/claude-container/proxy-profiles:/config") {
		t.Errorf("missing config volume in %v", args)
	}

	// Profile env
	if !containsArg(args, "-e", "PROXY_PROFILE=default") {
		t.Errorf("missing PROXY_PROFILE env in %v", args)
	}
}

func TestClaudeNetworkArgs(t *testing.T) {
	args := ClaudeNetworkArgs("myprofile", "/config/proxy-profiles/ca")
	joined := ""
	for _, a := range args {
		joined += a + " "
	}

	if !containsArg(args, "--network", "claude-proxy-net_myprofile") {
		t.Errorf("missing network flag in %v", args)
	}
	if !containsArg(args, "-e", "HTTP_PROXY=http://claude-proxy_myprofile:8080") {
		t.Errorf("missing HTTP_PROXY in %v", args)
	}
	if !containsArg(args, "-e", "HTTPS_PROXY=http://claude-proxy_myprofile:8080") {
		t.Errorf("missing HTTPS_PROXY in %v", args)
	}
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func containsArg(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func containsVolume(args []string, vol string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-v" && args[i+1] == vol {
			return true
		}
	}
	return false
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/httpproxy/ -v`
Expected: Package doesn't exist

**Step 3: Write the implementation**

```go
// internal/httpproxy/httpproxy.go
package httpproxy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	proxyPrefix  = "claude-proxy_"
	networkPrefix = "claude-proxy-net_"
	proxyPort    = 8080
)

// ProxyOpts holds options for starting a proxy sidecar.
type ProxyOpts struct {
	Profile       string
	ConfigDir     string // base config dir (~/.config/claude-container)
	DashboardPort int    // host port for dashboard (default 8081)
}

// ContainerName returns the Docker container name for a proxy profile.
func ContainerName(profile string) string {
	return proxyPrefix + profile
}

// NetworkName returns the Docker network name for a proxy profile.
func NetworkName(profile string) string {
	return networkPrefix + profile
}

// ImageTag returns the proxy sidecar image reference.
func ImageTag() string {
	if tag := os.Getenv("CLAUDE_PROXY_IMAGE_TAG"); tag != "" {
		return tag
	}
	return "claude-proxy:nix"
}

// ImageExists returns true if the proxy Docker image exists.
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageTag())
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsRunning returns true if the proxy for the given profile is running.
func IsRunning(profile string) bool {
	name := ContainerName(profile)
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Exists returns true if a proxy container for the profile exists.
func Exists(profile string) bool {
	name := ContainerName(profile)
	cmd := exec.Command("docker", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// RunArgs returns docker run arguments for the proxy sidecar.
func RunArgs(opts ProxyOpts) []string {
	name := ContainerName(opts.Profile)
	network := NetworkName(opts.Profile)
	profileDir := filepath.Join(opts.ConfigDir, "proxy-profiles")
	port := opts.DashboardPort
	if port == 0 {
		port = 8081
	}

	return []string{
		"run", "-d", "--rm",
		"--name", name,
		"--network", network,
		"-p", fmt.Sprintf("%d:8081", port),
		"-v", profileDir + ":/config",
		"-e", fmt.Sprintf("PROXY_PROFILE=%s", opts.Profile),
		ImageTag(),
	}
}

// ClaudeNetworkArgs returns the extra docker run flags needed to connect
// a Claude container to a proxy's network.
func ClaudeNetworkArgs(profile string, caCertDir string) []string {
	proxyHost := ContainerName(profile)
	network := NetworkName(profile)

	args := []string{
		"--network", network,
		"-e", fmt.Sprintf("HTTP_PROXY=http://%s:%d", proxyHost, proxyPort),
		"-e", fmt.Sprintf("HTTPS_PROXY=http://%s:%d", proxyHost, proxyPort),
	}

	// Mount CA cert if available
	if caCertDir != "" {
		args = append(args,
			"-v", caCertDir+":/proxy-ca:ro",
			"-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
			"-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
		)
	}

	return args
}

// EnsureNetwork creates the Docker network if it doesn't exist.
func EnsureNetwork(profile string) error {
	network := NetworkName(profile)
	cmd := exec.Command("docker", "network", "inspect", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if cmd.Run() == nil {
		return nil // already exists
	}
	create := exec.Command("docker", "network", "create", network)
	create.Stdout = os.Stdout
	create.Stderr = os.Stderr
	return create.Run()
}

// RemoveNetwork removes the Docker network.
func RemoveNetwork(profile string) error {
	network := NetworkName(profile)
	cmd := exec.Command("docker", "network", "rm", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// EnsureRunning starts the proxy sidecar if it's not already running.
// Returns true if a new proxy was started (false if reused).
func EnsureRunning(opts ProxyOpts) (started bool, err error) {
	if IsRunning(opts.Profile) {
		return false, nil
	}

	// Remove stale container if it exists but isn't running
	if Exists(opts.Profile) {
		exec.Command("docker", "rm", "-f", ContainerName(opts.Profile)).Run()
	}

	// Ensure profile dir exists
	profileDir := filepath.Join(opts.ConfigDir, "proxy-profiles")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return false, fmt.Errorf("create proxy profile dir: %w", err)
	}

	// Create network
	if err := EnsureNetwork(opts.Profile); err != nil {
		return false, fmt.Errorf("create proxy network: %w", err)
	}

	// Start proxy container
	args := RunArgs(opts)
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("start proxy: %w", err)
	}

	// Wait for health check
	if err := waitForHealth(opts); err != nil {
		return false, err
	}

	return true, nil
}

// waitForHealth polls the proxy's health endpoint until it responds.
func waitForHealth(opts ProxyOpts) error {
	port := opts.DashboardPort
	if port == 0 {
		port = 8081
	}
	url := fmt.Sprintf("http://localhost:%d/api/health", port)

	for i := 0; i < 30; i++ {
		cmd := exec.Command("curl", "-sf", url)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if cmd.Run() == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("proxy health check timed out after 30s")
}

// CACertDir returns the path where mitmproxy stores its CA cert.
func CACertDir(configDir string) string {
	return filepath.Join(configDir, "proxy-profiles", "ca")
}

// WaitForCACert waits for the mitmproxy CA cert to be generated.
func WaitForCACert(configDir string) error {
	certPath := filepath.Join(CACertDir(configDir), "mitmproxy-ca-cert.pem")
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(certPath); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("CA cert not generated after 30s at %s", certPath)
}

// Stop stops the proxy sidecar and removes the network.
func Stop(profile string) error {
	name := ContainerName(profile)
	cmd := exec.Command("docker", "rm", "-f", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Run() // ignore error — container may already be gone

	// Remove network (ignore error — may have other containers)
	RemoveNetwork(profile)
	return nil
}

// DashboardURL returns the URL for the proxy dashboard.
func DashboardURL(port int) string {
	if port == 0 {
		port = 8081
	}
	return fmt.Sprintf("http://localhost:%d", port)
}

// PendingCount queries the proxy for the number of pending requests.
func PendingCount(port int) int {
	if port == 0 {
		port = 8081
	}
	url := fmt.Sprintf("http://localhost:%d/api/pending", port)
	cmd := exec.Command("curl", "-sf", url)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if cmd.Run() != nil {
		return -1
	}
	// Count array elements (quick hack: count commas + 1 if non-empty)
	out := strings.TrimSpace(buf.String())
	if out == "[]" {
		return 0
	}
	return strings.Count(out, "flow_id")
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/httpproxy/ -v`
Expected: All 4 tests PASS

**Step 5: Commit**

```bash
git add internal/httpproxy/
git commit -m "feat: add Go httpproxy package for sidecar lifecycle management"
```

---

## Task 8: Go — Extend Session and Docker for Proxy Support

**Files:**
- Modify: `internal/config/config.go` — add proxy fields to Session struct
- Modify: `internal/docker/docker.go` — add proxy args to RunOpts and RunArgs
- Modify: `internal/docker/docker_test.go` — add tests for new fields

**Step 1: Add proxy fields to Session struct**

In `internal/config/config.go`, add to the `Session` struct (after line 35, the `DenyPaths` field):

```go
NetworkSandbox string `json:"network_sandbox,omitempty"` // proxy|claude|both|none
ProxyProfile   string `json:"proxy_profile,omitempty"`
ProxyPort      int    `json:"proxy_port,omitempty"`
```

**Step 2: Add proxy fields to RunOpts**

In `internal/docker/docker.go`, add to `RunOpts` struct (after `ExtraWorkspaces` field at line 88):

```go
NetworkSandbox string   // proxy|claude|both|none
ProxyProfile   string   // proxy profile name (for network/env args)
ProxyCACertDir string   // path to mitmproxy CA cert directory
```

**Step 3: Update RunArgs to inject proxy network flags**

In `internal/docker/docker.go`, in `RunArgs()` function, add after the ExtraWorkspaces loop (after line 117) and before the config dir mount (line 119):

```go
// When proxy is enabled, add network and proxy env vars.
if opts.ProxyProfile != "" {
    proxyContainer := "claude-proxy_" + opts.ProxyProfile
    network := "claude-proxy-net_" + opts.ProxyProfile
    args = append(args,
        "--network", network,
        "-e", fmt.Sprintf("HTTP_PROXY=http://%s:8080", proxyContainer),
        "-e", fmt.Sprintf("HTTPS_PROXY=http://%s:8080", proxyContainer),
    )
    if opts.ProxyCACertDir != "" {
        args = append(args,
            "-v", opts.ProxyCACertDir+":/proxy-ca:ro",
            "-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
            "-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
        )
    }
}
```

**Step 4: Add tests**

In `internal/docker/docker_test.go`, add:

```go
func TestRunArgsProxyNetwork(t *testing.T) {
	args := RunArgs(RunOpts{
		Name:           "test-session",
		Workspace:      "/tmp/ws",
		ConfigDir:      "/tmp/config",
		UID:            1000,
		GID:            1000,
		ProxyProfile:   "myprofile",
		ProxyCACertDir: "/tmp/ca",
	}, false)

	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--network claude-proxy-net_myprofile") {
		t.Errorf("missing network flag in %v", args)
	}
	if !slices.Contains(args, "HTTP_PROXY=http://claude-proxy_myprofile:8080") {
		t.Errorf("missing HTTP_PROXY in %v", args)
	}
	if !slices.Contains(args, "HTTPS_PROXY=http://claude-proxy_myprofile:8080") {
		t.Errorf("missing HTTPS_PROXY in %v", args)
	}
	if !strings.Contains(joined, "/tmp/ca:/proxy-ca:ro") {
		t.Errorf("missing CA cert volume in %v", args)
	}
}

func TestRunArgsNoProxy(t *testing.T) {
	args := RunArgs(RunOpts{
		Name:      "test-session",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
	}, false)

	for _, a := range args {
		if strings.Contains(a, "HTTP_PROXY") {
			t.Errorf("unexpected HTTP_PROXY in non-proxy args: %v", args)
		}
		if strings.Contains(a, "--network") {
			t.Errorf("unexpected --network in non-proxy args: %v", args)
		}
	}
}
```

**Step 5: Run tests**

Run: `nix develop --command go test ./internal/docker/ -v && nix develop --command go test ./internal/config/ -v`
Expected: All tests PASS

**Step 6: Commit**

```bash
git add internal/config/config.go internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat: extend Session and RunArgs with proxy network support"
```

---

## Task 9: Go — Update ManagedSettings for Proxy Mode

When `--network-sandbox=proxy`, the Claude sandbox should allow all network traffic (proxy handles it). When `--network-sandbox=none`, also unrestrict network.

**Files:**
- Modify: `internal/sandbox/sandbox.go` — add `NetworkUnrestricted` option to ManagedSettings
- Modify: `internal/sandbox/sandbox_test.go` — add tests

**Step 1: Write failing tests**

In `internal/sandbox/sandbox_test.go`, add:

```go
func TestManagedSettingsNetworkUnrestricted(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, nil)

	// Normal mode: allowedDomains present
	sb := settings["sandbox"].(map[string]any)
	network := sb["network"].(map[string]any)
	if network["allowedDomains"] == nil {
		t.Error("med profile should have allowedDomains")
	}

	// Unrestricted mode: allowedDomains omitted
	settingsU := p.ManagedSettingsUnrestricted(nil)
	sbU := settingsU["sandbox"].(map[string]any)
	if _, hasNetwork := sbU["network"]; hasNetwork {
		t.Error("unrestricted settings should not have network key")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/sandbox/ -run TestManagedSettingsNetworkUnrestricted -v`
Expected: FAIL — method doesn't exist

**Step 3: Add ManagedSettingsUnrestricted**

In `internal/sandbox/sandbox.go`, add after `ManagedSettings()` (after line 110):

```go
// ManagedSettingsUnrestricted generates settings with network access unrestricted.
// Used when proxy handles network sandboxing instead of Claude's built-in sandbox.
// Non-network sandbox features (deny paths, permissions) remain active.
func (p Profile) ManagedSettingsUnrestricted(extraDenyPaths []string) map[string]any {
	deny := make([]string, len(p.DenyPaths))
	copy(deny, p.DenyPaths)
	for _, path := range extraDenyPaths {
		deny = append(deny, fmt.Sprintf("Read(%s)", path))
	}

	settings := map[string]any{
		"env": map[string]any{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_AUTOUPDATER":                      "1",
		},
		"cleanupPeriodDays":     14,
		"alwaysThinkingEnabled": true,
		"showTurnDuration":      true,
		"spinnerTipsEnabled":    false,
		"sandbox": map[string]any{
			"enabled":                   p.SandboxEnabled,
			"autoAllowBashIfSandboxed":  true,
			"enableWeakerNestedSandbox": true,
			"allowUnsandboxedCommands":  false,
			"excludedCommands":          []string{"git"},
		},
	}

	if len(deny) > 0 {
		settings["permissions"] = map[string]any{
			"deny": deny,
		}
	}

	return settings
}
```

**Step 4: Run tests**

Run: `nix develop --command go test ./internal/sandbox/ -v`
Expected: All tests PASS

**Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go
git commit -m "feat: add ManagedSettingsUnrestricted for proxy network mode"
```

---

## Task 10: Go — CLI Flag Integration (`cmd/`)

Add `--network-sandbox`, `--proxy-profile`, `--proxy-port` flags to all session-creation commands and wire them through `createSession`.

**Files:**
- Modify: `cmd/new.go` — add flags, update createOpts, update createSession
- Modify: `cmd/run.go` — add flags, pass to createOpts
- Modify: `cmd/work.go` — add flags, pass to createOpts

**Step 1: Update createOpts struct**

In `cmd/new.go`, add to global vars (after line 35):

```go
newNetworkSandbox string
newProxyProfile   string
newProxyPort      int
```

Add to `createOpts` struct (after `denyPaths` field):

```go
networkSandbox string
proxyProfile   string
proxyPort      int
```

**Step 2: Register flags in new.go init()**

After line 128, add:

```go
newCmd.Flags().StringVar(&newNetworkSandbox, "network-sandbox", "claude",
    "Network enforcement: proxy, claude, both, none")
newCmd.Flags().StringVar(&newProxyProfile, "proxy-profile", "default",
    "Proxy rule profile name")
newCmd.Flags().IntVar(&newProxyPort, "proxy-port", 8081,
    "Dashboard port on host")
```

**Step 3: Wire opts through createSession calls**

In both `newCmd.RunE` branches (lines 79-92 and 95-110), add the new fields to the `createOpts{}` literal:

```go
networkSandbox: newNetworkSandbox,
proxyProfile:   newProxyProfile,
proxyPort:      newProxyPort,
```

**Step 4: Update createSession**

In `createSession()`, after the managed settings generation block (lines 265-278), add proxy-aware logic:

```go
// Determine which managed settings to write based on network sandbox mode.
networkSandbox := opts.networkSandbox
if networkSandbox == "" {
    networkSandbox = "claude"
}
var settingsJSON []byte
switch networkSandbox {
case "proxy", "none":
    // Unrestrict Claude's network — proxy (or nothing) handles it.
    settingsJSON, err = json.MarshalIndent(
        prof.ManagedSettingsUnrestricted(opts.denyPaths), "", "  ")
case "claude", "both":
    // Claude sandbox manages network.
    settingsJSON, err = json.MarshalIndent(
        prof.ManagedSettings(opts.allowDomains, opts.denyPaths), "", "  ")
default:
    return fmt.Errorf("invalid --network-sandbox value %q (valid: proxy, claude, both, none)", networkSandbox)
}
```

(This replaces the existing lines 270-274.)

After the Docker image ensure (line 219-221), add proxy sidecar startup:

```go
// Start proxy sidecar if needed.
proxyProfile := opts.proxyProfile
if proxyProfile == "" {
    proxyProfile = "default"
}
useProxy := networkSandbox == "proxy" || networkSandbox == "both"
if useProxy {
    if !httpproxy.ImageExists() {
        return fmt.Errorf("proxy image %q not found", httpproxy.ImageTag())
    }
    started, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
        Profile:       proxyProfile,
        ConfigDir:     config.DefaultDir(),
        DashboardPort: opts.proxyPort,
    })
    if err != nil {
        return fmt.Errorf("start proxy: %w", err)
    }
    if started {
        fmt.Printf("Proxy started for profile %q — dashboard at %s\n",
            proxyProfile, httpproxy.DashboardURL(opts.proxyPort))
    } else {
        fmt.Printf("Reusing proxy for profile %q — dashboard at %s\n",
            proxyProfile, httpproxy.DashboardURL(opts.proxyPort))
    }
    // Wait for CA cert
    if err := httpproxy.WaitForCACert(config.DefaultDir()); err != nil {
        return err
    }
}
```

Update `runOpts` (lines 280-292) to include proxy fields:

```go
runOpts := docker.RunOpts{
    // ... existing fields ...
    ProxyProfile:   func() string { if useProxy { return proxyProfile } else { return "" } }(),
    ProxyCACertDir: func() string { if useProxy { return httpproxy.CACertDir(config.DefaultDir()) } else { return "" } }(),
}
```

Update session record (lines 296-312) to include proxy fields:

```go
sess := &config.Session{
    // ... existing fields ...
    NetworkSandbox: networkSandbox,
    ProxyProfile:   proxyProfile,
    ProxyPort:      opts.proxyPort,
}
```

**Step 5: Mirror flags in run.go and work.go**

Add the same three global vars and flag registrations to both `cmd/run.go` and `cmd/work.go`, passing them through to `createOpts`.

**Step 6: Commit**

```bash
git add cmd/new.go cmd/run.go cmd/work.go
git commit -m "feat: add --network-sandbox, --proxy-profile, --proxy-port CLI flags"
```

---

## Task 11: Go — Update Attach, Stop, Remove for Proxy Lifecycle

**Files:**
- Modify: `cmd/attach.go` — ensure proxy running on reattach
- Modify: `cmd/stop.go` — conditionally stop proxy
- Modify: `cmd/rm.go` — conditionally cleanup proxy

**Step 1: Update ensureRunning in attach.go**

In `ensureRunning()`, after the container recreation block (before `return nil` at line 114), add proxy startup:

```go
// Ensure proxy is running if session uses one.
if sess.NetworkSandbox == "proxy" || sess.NetworkSandbox == "both" {
    _, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
        Profile:       sess.ProxyProfile,
        ConfigDir:     config.DefaultDir(),
        DashboardPort: sess.ProxyPort,
    })
    if err != nil {
        return fmt.Errorf("start proxy: %w", err)
    }
}
```

Also add proxy fields to the `RunOpts` in the recreation block (lines 96-108):

```go
ProxyProfile:   func() string {
    if sess.NetworkSandbox == "proxy" || sess.NetworkSandbox == "both" {
        return sess.ProxyProfile
    }
    return ""
}(),
ProxyCACertDir: func() string {
    if sess.NetworkSandbox == "proxy" || sess.NetworkSandbox == "both" {
        return httpproxy.CACertDir(config.DefaultDir())
    }
    return ""
}(),
```

**Step 2: Create proxyCleanupIfUnused helper in rm.go**

Add to `cmd/rm.go`:

```go
// proxyCleanupIfUnused stops the proxy for the given profile if no other
// sessions are using it.
func proxyCleanupIfUnused(store *config.Store, profile string, excludeSession string) {
    if profile == "" {
        return
    }
    for _, s := range store.List() {
        if s.Name == excludeSession {
            continue
        }
        if s.ProxyProfile == profile &&
            (s.NetworkSandbox == "proxy" || s.NetworkSandbox == "both") {
            return // another session uses this proxy
        }
    }
    // No other sessions — stop proxy
    httpproxy.Stop(profile)
}
```

**Step 3: Call cleanup in removeSession**

In `removeSession()`, after deleting the session record (line 42-44), add:

```go
if sess != nil && sess.ProxyProfile != "" {
    proxyCleanupIfUnused(store, sess.ProxyProfile, name)
}
```

**Step 4: Update stopCmd**

In `cmd/stop.go`, after stopping the container, add proxy cleanup:

```go
sess, _ := store.Get(name)
// ... existing stop logic ...
if sess != nil && sess.ProxyProfile != "" {
    proxyCleanupIfUnused(store, sess.ProxyProfile, name)
}
```

(Note: `proxyCleanupIfUnused` is in rm.go which is the same `cmd` package, so it's accessible.)

**Step 5: Commit**

```bash
git add cmd/attach.go cmd/stop.go cmd/rm.go
git commit -m "feat: proxy lifecycle in attach/stop/rm with per-profile reuse"
```

---

## Task 12: Go — Status Bar Proxy Indicator

**Files:**
- Modify: `internal/proxy/proxy.go` — add proxy pending count to status bar

**Step 1: Extend StatusBarInfo**

Add to `StatusBarInfo` struct (after `Yolo bool`):

```go
ProxyPort int  // if >0, poll proxy for pending count
```

**Step 2: Update renderStatusBar**

In `renderStatusBar()`, add proxy pending indicator to the parts list (after the `info.Yolo` check):

```go
if info.ProxyPort > 0 {
    count := httpproxy.PendingCount(info.ProxyPort)
    if count > 0 {
        parts = append(parts, fmt.Sprintf("proxy: %d pending", count))
    } else if count == 0 {
        parts = append(parts, "proxy")
    }
}
```

**Step 3: Wire ProxyPort through callers**

In `cmd/new.go` `createSession()` line 338, add `ProxyPort` to StatusBarInfo:

```go
StatusBar: proxy.StatusBarInfo{
    Name: name, Branch: branch, Yolo: profile == "low",
    ProxyPort: func() int { if useProxy { return opts.proxyPort } else { return 0 } }(),
},
```

Similarly in `cmd/attach.go` line 57:

```go
StatusBar: proxy.StatusBarInfo{
    Name: name, Branch: sess.Branch, Yolo: sess.Yolo,
    ProxyPort: sess.ProxyPort,
},
```

**Step 4: Commit**

```bash
git add internal/proxy/proxy.go cmd/new.go cmd/attach.go
git commit -m "feat: show proxy pending count in TUI status bar"
```

---

## Task 13: Nix — Proxy Sidecar Image (`nix/proxy-image.nix`)

**Files:**
- Create: `nix/proxy-image.nix`
- Modify: `flake.nix` — add proxy image output and wrapper env vars

**Step 1: Write proxy-image.nix**

```nix
# nix/proxy-image.nix
{ pkgs }:
let
  python = pkgs.python3.withPackages (ps: with ps; [
    mitmproxy
    starlette
    uvicorn
    websockets
  ]);

  proxySource = ../proxy;

  entrypoint = pkgs.writeShellScript "proxy-entrypoint.sh" ''
    set -e
    PROXY_PROFILE=''${PROXY_PROFILE:-default}
    exec ${python}/bin/python -m claude_proxy.app \
      --profile "$PROXY_PROFILE" \
      --config-dir /config \
      --proxy-port 8080 \
      --dashboard-port 8081
  '';
in
pkgs.dockerTools.buildLayeredImage {
  name = "claude-proxy";
  tag = "nix";

  contents = [
    python
    pkgs.coreutils
    pkgs.cacert
    pkgs.curl
  ];

  extraCommands = ''
    mkdir -p config
    # Copy proxy source into image
    cp -r ${proxySource}/claude_proxy opt/claude_proxy
    cp -r ${proxySource}/static opt/static
  '';

  config = {
    Entrypoint = [ "${entrypoint}" ];
    ExposedPorts = {
      "8080/tcp" = {};
      "8081/tcp" = {};
    };
    Env = [
      "PYTHONPATH=/opt"
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
    ];
  };
}
```

**Step 2: Update flake.nix**

Add proxy image to `mkClaudeContainer` output (after `image` definition around line 39):

```nix
proxyImage = pkgs.callPackage ./nix/proxy-image.nix {};
```

Update the wrapper to include proxy image env vars (after line 57):

```nix
--set CLAUDE_PROXY_IMAGE_TARBALL "${proxyImage}" \
--set CLAUDE_PROXY_IMAGE_TAG "claude-proxy:nix"
```

Update the `in` block to expose proxy image:

```nix
{ inherit image proxyImage cli; };
```

Add to packages outputs:

```nix
packages.claude-proxy-image = defaultContainer.proxyImage;
```

**Step 3: Commit**

```bash
git add nix/proxy-image.nix flake.nix
git commit -m "feat: add Nix-built proxy sidecar Docker image"
```

---

## Task 14: Integration Tests

**Files:**
- Create: `internal/httpproxy/httpproxy_integration_test.go`

**Step 1: Write integration tests**

```go
// internal/httpproxy/httpproxy_integration_test.go
package httpproxy

import (
	"os/exec"
	"testing"
)

func skipIfDockerUnavailable(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Skip("docker not available")
	}
	if !ImageExists() {
		t.Skipf("proxy image %q not loaded", ImageTag())
	}
}

func TestIntegrationProxyStartStop(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "integration-test"
	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     t.TempDir(),
		DashboardPort: 18081,
	}

	// Start proxy
	started, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Error("expected proxy to be started, not reused")
	}
	t.Cleanup(func() { Stop(profile) })

	// Verify running
	if !IsRunning(profile) {
		t.Error("proxy should be running")
	}

	// Reuse
	started2, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning (reuse): %v", err)
	}
	if started2 {
		t.Error("expected proxy to be reused, not started")
	}

	// Stop
	Stop(profile)
	if IsRunning(profile) {
		t.Error("proxy should be stopped")
	}
}

func TestIntegrationProxyHealthEndpoint(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "health-test"
	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     t.TempDir(),
		DashboardPort: 18082,
	}

	started, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected fresh start")
	}
	t.Cleanup(func() { Stop(profile) })

	// Health endpoint should respond
	count := PendingCount(18082)
	if count != 0 {
		t.Errorf("PendingCount = %d, want 0", count)
	}
}
```

**Step 2: Run tests**

Run: `nix develop --command go test ./internal/httpproxy/ -run TestIntegration -v -timeout 120s`
Expected: Tests pass (or skip if images not available)

**Step 3: Commit**

```bash
git add internal/httpproxy/httpproxy_integration_test.go
git commit -m "test: add integration tests for proxy sidecar lifecycle"
```

---

## Task 15: Python Integration Tests

**Files:**
- Create: `proxy/tests/test_integration.py`

**Step 1: Write integration tests**

```python
# proxy/tests/test_integration.py
"""Integration tests for the proxy — requires running the full app."""
import json
import os
import tempfile
import threading
import time

import pytest
import requests

# These tests only run when PROXY_INTEGRATION=1 is set
pytestmark = pytest.mark.skipif(
    os.environ.get("PROXY_INTEGRATION") != "1",
    reason="Set PROXY_INTEGRATION=1 to run"
)


class TestRulesPersistence:
    """Test that rules survive save/load cycle with real filesystem."""

    def test_full_cycle(self):
        from claude_proxy.rules import RuleStore
        from claude_proxy.patterns import base_domain

        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "test.json")

            # Create store, add rules
            s1 = RuleStore()
            s1.add("allow", base_domain("github.com"), "github.com", None)
            s1.add("deny", base_domain("evil.com"), "evil.com",
                   time.time() + 3600)
            s1.save(path)

            # Load in new store
            s2 = RuleStore()
            s2.load(path)
            assert s2.match("https://github.com/repo") == "allow"
            assert s2.match("https://evil.com/steal") == "deny"
            assert s2.match("https://unknown.com") is None

    def test_expired_rules_cleaned_on_load(self):
        from claude_proxy.rules import RuleStore

        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "test.json")
            s = RuleStore()
            s.add("allow", r"^https?://old\.com(/.*)?$", "old.com",
                  time.time() - 1)  # already expired
            s.add("allow", r"^https?://new\.com(/.*)?$", "new.com", None)
            s.save(path)

            s2 = RuleStore()
            s2.load(path)
            s2.cleanup_expired()
            assert len(s2.list_rules()) == 1
```

**Step 2: Run tests**

Run: `cd proxy && python -m pytest tests/ -v`
Expected: Integration tests skipped (no PROXY_INTEGRATION=1), unit tests pass

**Step 3: Commit**

```bash
git add proxy/tests/test_integration.py
git commit -m "test: add Python proxy integration tests"
```

---

## Task 16: Add `proxy/` to .gitignore and Update README

**Files:**
- Modify: `README.md` — add proxy documentation section

**Step 1: Update README**

Add a new section after the existing sandbox profile documentation:

```markdown
### HTTP Proxy (Network Sandbox)

Run an interactive HTTP/HTTPS proxy that intercepts outbound requests:

    claude-container run --network-sandbox=proxy --proxy-profile=myproject

Open the dashboard at http://localhost:8081 to manage allow/deny rules.

**Modes:**
- `--network-sandbox=claude` (default) — Claude's built-in sandbox only
- `--network-sandbox=proxy` — proxy handles network, Claude sandbox allows all network
- `--network-sandbox=both` — both layers active
- `--network-sandbox=none` — no network restrictions

**Proxy profiles** persist rules across sessions. Multiple sessions can share a profile:

    claude-container run --proxy-profile=trusted-project
    claude-container work --proxy-profile=trusted-project feat/auth

The proxy sidecar is automatically reused when sessions share a profile, and cleaned up when the last session using it exits.
```

**Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add HTTP proxy documentation to README"
```

---

## Verification

Run the full test suite:

```bash
# Go unit tests
nix develop --command go test ./... -v -timeout 120s

# Go integration tests (requires Docker + images)
nix develop --command go test ./internal/docker/ -run TestIntegration -v -timeout 120s
nix develop --command go test ./internal/httpproxy/ -run TestIntegration -v -timeout 120s

# Python unit tests
cd proxy && python -m pytest tests/ -v

# Build proxy image
nix build .#claude-proxy-image

# Manual E2E: start session with proxy, verify dashboard works
claude-container run --network-sandbox=proxy --proxy-profile=test --name=proxy-test
# Open http://localhost:8081 in browser
```
