"""Tests for the dashboard REST API endpoints."""

import asyncio
import json
import threading
import time
from unittest.mock import MagicMock

import pytest
from starlette.testclient import TestClient

from claude_proxy.addon import ProxyAddon
from claude_proxy.dashboard import app, configure, on_pending_request, set_dashboard_loop
from claude_proxy.rules import RuleStore


@pytest.fixture
def client(tmp_path):
    """Create a TestClient with a fresh RuleStore and ProxyAddon."""
    store = RuleStore()
    addon = ProxyAddon(store, hold_timeout=120)
    profile_path = str(tmp_path / "test-profile.json")
    configure(addon, store, profile_path)
    return TestClient(app)


@pytest.fixture
def store_and_addon(tmp_path):
    """Return (store, addon, client, profile_path) for tests needing direct access."""
    store = RuleStore()
    addon = ProxyAddon(store, hold_timeout=120)
    profile_path = str(tmp_path / "test-profile.json")
    configure(addon, store, profile_path)
    client = TestClient(app)
    return store, addon, client, profile_path


def _make_flow(flow_id, url, host):
    """Create a mock mitmproxy flow object."""
    flow = MagicMock()
    flow.id = flow_id
    flow.request.pretty_url = url
    flow.request.host = host
    flow.request.port = 443
    flow.request.url = url
    flow.killable = True
    return flow


class TestHealthEndpoint:
    def test_health_returns_ok(self, client):
        resp = client.get("/api/health")
        assert resp.status_code == 200
        assert resp.json() == {"status": "ok"}


class TestPendingEndpoint:
    def test_empty_pending(self, client):
        resp = client.get("/api/pending")
        assert resp.status_code == 200
        assert resp.json() == []

    def test_pending_with_held_flow(self, store_and_addon):
        store, addon, client, _ = store_and_addon
        flow = _make_flow("test-flow-1", "https://unknown.com/page", "unknown.com")
        addon.request(flow)

        resp = client.get("/api/pending")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 1
        assert data[0]["flow_id"] == "test-flow-1"
        assert data[0]["url"] == "https://unknown.com/page"


class TestRulesEndpoint:
    def test_empty_rules(self, client):
        resp = client.get("/api/rules")
        assert resp.status_code == 200
        assert resp.json() == []

    def test_add_rule(self, client):
        resp = client.post("/api/rules", json={
            "type": "allow",
            "pattern": r"^https?://example\.com(/.*)?$",
            "label": "example.com",
        })
        assert resp.status_code == 201
        data = resp.json()
        assert "id" in data

        # Verify it appears in GET
        resp2 = client.get("/api/rules")
        rules = resp2.json()
        assert len(rules) == 1
        assert rules[0]["label"] == "example.com"

    def test_add_rule_with_expiry(self, client):
        expires = time.time() + 3600
        resp = client.post("/api/rules", json={
            "type": "deny",
            "pattern": r"^https?://bad\.com(/.*)?$",
            "label": "bad.com",
            "expires_at": expires,
        })
        assert resp.status_code == 201

    def test_add_rule_missing_type(self, client):
        resp = client.post("/api/rules", json={
            "pattern": r"^https?://example\.com(/.*)?$",
        })
        assert resp.status_code == 400

    def test_add_rule_missing_pattern(self, client):
        resp = client.post("/api/rules", json={
            "type": "allow",
        })
        assert resp.status_code == 400

    def test_delete_rule(self, client):
        # Add a rule
        resp = client.post("/api/rules", json={
            "type": "allow",
            "pattern": r"^https?://temp\.com(/.*)?$",
            "label": "temp",
        })
        rule_id = resp.json()["id"]

        # Delete it
        resp2 = client.delete(f"/api/rules/{rule_id}")
        assert resp2.status_code == 200
        assert resp2.json() == {"ok": True}

        # Verify it's gone
        resp3 = client.get("/api/rules")
        assert resp3.json() == []

    def test_delete_nonexistent_rule(self, client):
        resp = client.delete("/api/rules/nonexistent-id")
        assert resp.status_code == 404

    def test_add_rule_persists_to_disk(self, store_and_addon):
        store, addon, client, profile_path = store_and_addon
        client.post("/api/rules", json={
            "type": "allow",
            "pattern": r"^https?://persist\.com(/.*)?$",
            "label": "persist.com",
        })
        # Check file exists and contains the rule
        with open(profile_path) as f:
            data = json.load(f)
        assert len(data) == 1
        assert data[0]["label"] == "persist.com"


class TestResolveEndpoint:
    def test_resolve_allow(self, store_and_addon):
        store, addon, client, _ = store_and_addon
        flow = _make_flow("resolve-test-1", "https://newsite.com/api", "newsite.com")
        addon.request(flow)

        resp = client.post("/api/resolve", json={
            "flow_id": "resolve-test-1",
            "action": "allow",
            "pattern": r"^https?://newsite\.com(/.*)?$",
            "label": "newsite.com",
        })
        assert resp.status_code == 200
        assert resp.json() == {"ok": True}

        # Flow should be released, not killed
        flow.resume.assert_called_once()
        flow.kill.assert_not_called()

        # Rule should exist
        assert store.match("https://newsite.com/api") == "allow"

    def test_resolve_deny(self, store_and_addon):
        store, addon, client, _ = store_and_addon
        flow = _make_flow("resolve-deny-1", "https://badsite.com/track", "badsite.com")
        addon.request(flow)

        resp = client.post("/api/resolve", json={
            "flow_id": "resolve-deny-1",
            "action": "deny",
            "pattern": r"^https?://badsite\.com(/.*)?$",
            "label": "badsite.com",
        })
        assert resp.status_code == 200
        flow.kill.assert_called_once()

    def test_resolve_missing_fields(self, client):
        resp = client.post("/api/resolve", json={
            "flow_id": "some-id",
        })
        assert resp.status_code == 400

    def test_resolve_nonexistent_flow(self, client):
        resp = client.post("/api/resolve", json={
            "flow_id": "nonexistent",
            "action": "allow",
            "pattern": r".*",
            "label": "test",
        })
        assert resp.status_code == 404


class TestWebSocket:
    def test_ws_connect_gets_init(self, store_and_addon):
        store, addon, client, _ = store_and_addon
        with client.websocket_connect("/ws") as ws:
            data = ws.receive_json()
            assert data["type"] == "init"
            assert "pending" in data["data"]
            assert "rules" in data["data"]


class TestOnPendingRequestCrossThread:
    """Verify on_pending_request works when called from a non-asyncio thread."""

    def test_on_pending_request_from_foreign_thread(self):
        """Simulate the mitmproxy thread calling on_pending_request.

        The mitmproxy worker thread has no running asyncio loop. The fix stores
        the dashboard's event loop and uses call_soon_threadsafe to schedule
        the broadcast coroutine on that loop.
        """
        from claude_proxy import dashboard as _dashboard_mod

        # 1. Create a dedicated event loop to act as the dashboard's loop.
        loop = asyncio.new_event_loop()
        set_dashboard_loop(loop)

        # 2. Monkey-patch broadcast to capture calls.
        received: list[dict] = []
        _original_broadcast = _dashboard_mod.broadcast

        async def _capturing_broadcast(message: dict) -> None:
            received.append(message)

        _dashboard_mod.broadcast = _capturing_broadcast

        try:
            # 3. Call on_pending_request from a plain thread (no asyncio loop).
            info = {"flow_id": "cross-thread-1", "url": "https://example.com"}
            call_done = threading.Event()
            error_in_thread: list[Exception] = []

            def worker():
                try:
                    on_pending_request(info)
                except Exception as exc:
                    error_in_thread.append(exc)
                finally:
                    call_done.set()

            t = threading.Thread(target=worker)
            t.start()
            call_done.wait(timeout=5)
            t.join(timeout=5)

            assert not error_in_thread, f"on_pending_request raised: {error_in_thread}"

            # 4. Run the loop to process the call_soon_threadsafe callback
            #    and the resulting task.  run_until_complete drives the loop
            #    until the given coroutine finishes, which also drains any
            #    pending callbacks (including create_task from call_soon_threadsafe).
            async def _drain():
                # Yield control so the task created by call_soon_threadsafe runs.
                await asyncio.sleep(0)

            loop.run_until_complete(_drain())

            assert len(received) == 1
            assert received[0]["type"] == "pending"
            assert received[0]["data"]["flow_id"] == "cross-thread-1"
        finally:
            _dashboard_mod.broadcast = _original_broadcast
            loop.close()
            set_dashboard_loop(None)


def test_post_rule_accepts_new_shape(client):
    resp = client.post("/api/rules", json={
        "direction": "out",
        "proto": "http",
        "match": {"host": "example.com"},
        "action": "allow",
        "label": "ex",
    })
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
    })
    assert resp.status_code == 201
    rules = client.get("/api/rules").json()
    found = next(r for r in rules if r["label"] == "gh")
    assert found["proto"] in ("http", "any")
    assert found["action"] == "allow"


def test_publish_endpoint_calls_publish_mgr(client, monkeypatch):
    """POST /api/publish forwards to /run/publish-mgr.sock."""
    import httpx
    calls = []
    class FakeTransport(httpx.BaseTransport):
        def handle_request(self, request):
            calls.append((request.method, request.url.path,
                          request.read().decode()))
            return httpx.Response(200, json={
                "host_port": 30005, "container_port": 3000,
                "protocol": "tcp", "ok": True,
            })

    from claude_proxy import dashboard
    monkeypatch.setattr(dashboard, "_publish_mgr_transport",
                        FakeTransport())

    resp = client.post("/api/publish",
        json={"protocol": "tcp", "container_port": 3000, "label": "vite"})
    assert resp.status_code == 200
    assert resp.json()["host_port"] == 30005
    assert len(calls) == 1
    assert calls[0][1].endswith("/publish")


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
