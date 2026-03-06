"""Tests for the mitmproxy addon (ProxyAddon class)."""

from unittest.mock import MagicMock

from claude_proxy.addon import ProxyAddon
from claude_proxy.rules import RuleStore


def make_flow(url, host="example.com", port=443):
    """Create a mock mitmproxy flow object."""
    flow = MagicMock()
    flow.request.pretty_url = url
    flow.request.host = host
    flow.request.port = port
    flow.request.url = url
    flow.id = f"flow-{id(flow)}"
    flow.killable = True
    return flow


class TestProxyAddon:
    """Tests for ProxyAddon request interception and resolution."""

    def test_allowed_domain_passes(self):
        """A flow matching an allow rule passes through without being killed or held."""
        store = RuleStore()
        store.add("allow", r"https://example\.com/.*", "Allow example.com")
        addon = ProxyAddon(store)

        flow = make_flow("https://example.com/page")
        addon.request(flow)

        flow.kill.assert_not_called()
        assert flow.id not in {p["flow_id"] for p in addon.get_pending()}

    def test_denied_domain_killed(self):
        """A flow matching a deny rule is killed and not held as pending."""
        store = RuleStore()
        store.add("deny", r"https://evil\.com/.*", "Deny evil.com")
        addon = ProxyAddon(store)

        flow = make_flow("https://evil.com/malware", host="evil.com")
        addon.request(flow)

        flow.kill.assert_called_once()
        assert flow.id not in {p["flow_id"] for p in addon.get_pending()}

    def test_unknown_domain_held(self):
        """A flow with no matching rule is held as pending."""
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = make_flow("https://unknown.com/page", host="unknown.com")
        addon.request(flow)

        pending = addon.get_pending()
        assert len(pending) == 1
        assert pending[0]["flow_id"] == flow.id
        assert pending[0]["url"] == "https://unknown.com/page"
        flow.kill.assert_not_called()

    def test_resolve_allow_releases_flow(self):
        """Resolving a pending flow with 'allow' releases it and adds a rule."""
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = make_flow("https://newsite.com/api", host="newsite.com")
        addon.request(flow)
        assert len(addon.get_pending()) == 1

        result = addon.resolve(
            flow.id,
            "allow",
            r"https://newsite\.com/.*",
            "Allow newsite.com",
            expires_at=None,
        )

        assert result is True
        assert len(addon.get_pending()) == 0
        flow.kill.assert_not_called()
        # Verify the rule was added to the store
        assert store.match("https://newsite.com/api") == "allow"

    def test_resolve_deny_kills_flow(self):
        """Resolving a pending flow with 'deny' kills it and adds a rule."""
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = make_flow("https://badsite.com/track", host="badsite.com")
        addon.request(flow)
        assert len(addon.get_pending()) == 1

        result = addon.resolve(
            flow.id,
            "deny",
            r"https://badsite\.com/.*",
            "Deny badsite.com",
            expires_at=None,
        )

        assert result is True
        assert len(addon.get_pending()) == 0
        flow.kill.assert_called_once()
        # Verify the rule was added to the store
        assert store.match("https://badsite.com/track") == "deny"

    def test_pending_list(self):
        """Multiple unknown flows all appear in the pending list."""
        store = RuleStore()
        addon = ProxyAddon(store)

        flow1 = make_flow("https://site-a.com/page", host="site-a.com")
        flow2 = make_flow("https://site-b.com/data", host="site-b.com")
        addon.request(flow1)
        addon.request(flow2)

        pending = addon.get_pending()
        assert len(pending) == 2
        urls = {p["url"] for p in pending}
        assert urls == {"https://site-a.com/page", "https://site-b.com/data"}
        flow_ids = {p["flow_id"] for p in pending}
        assert flow1.id in flow_ids
        assert flow2.id in flow_ids

    def test_resolve_nonexistent_returns_false(self):
        """Resolving a flow ID that is not pending returns False."""
        store = RuleStore()
        addon = ProxyAddon(store)

        result = addon.resolve(
            "nonexistent-flow",
            "allow",
            r".*",
            "Test",
            expires_at=None,
        )
        assert result is False

    def test_on_pending_callback_called(self):
        """The on_pending callback is invoked when a flow is held."""
        store = RuleStore()
        callback = MagicMock()
        addon = ProxyAddon(store, on_pending=callback)

        flow = make_flow("https://unknown.com/page", host="unknown.com")
        addon.request(flow)

        callback.assert_called_once()
        call_arg = callback.call_args[0][0]
        assert call_arg["flow_id"] == flow.id
        assert call_arg["url"] == "https://unknown.com/page"

    def test_request_with_broken_flow_does_not_raise(self):
        """A flow whose pretty_url raises is handled gracefully: no exception, not pending."""
        store = RuleStore()
        addon = ProxyAddon(store)

        flow = MagicMock()
        flow.id = "broken-flow"
        # Make pretty_url raise an exception when accessed
        type(flow.request).pretty_url = property(
            lambda self: (_ for _ in ()).throw(RuntimeError("broken"))
        )

        # Should not raise
        addon.request(flow)

        # Flow should not be in pending
        assert flow.id not in {p["flow_id"] for p in addon.get_pending()}
        # Flow should have been killed
        flow.kill.assert_called_once()

    def test_cleanup_timed_out(self):
        """Flows pending longer than hold_timeout are killed and removed."""
        store = RuleStore()
        addon = ProxyAddon(store, hold_timeout=0)  # Immediate timeout

        flow = make_flow("https://slow.com/page", host="slow.com")
        addon.request(flow)
        assert len(addon.get_pending()) == 1

        timed_out = addon.cleanup_timed_out()

        assert flow.id in timed_out
        assert len(addon.get_pending()) == 0
        flow.kill.assert_called_once()
