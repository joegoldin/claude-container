"""Tests for the dashboard REST API endpoints."""

import json
import time
from unittest.mock import MagicMock

import pytest
from starlette.testclient import TestClient

from claude_proxy.addon import ProxyAddon
from claude_proxy.dashboard import app, configure
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
