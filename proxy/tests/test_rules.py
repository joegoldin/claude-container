"""Tests for the rule engine (Rule dataclass and RuleStore class)."""

import json
import time
from pathlib import Path

from claude_proxy.rules import RuleStore


class TestRuleStore:
    """Tests for RuleStore and Rule behavior."""

    def test_add_and_match_allow(self):
        """Adding an allow rule matches the correct URL."""
        store = RuleStore()
        rule_id = store.add("allow", r"https://example\.com/.*", "Allow example.com")
        assert isinstance(rule_id, str)
        assert len(rule_id) > 0
        result = store.match("https://example.com/path")
        assert result == "allow"

    def test_add_and_match_deny(self):
        """Adding a deny rule matches the correct URL."""
        store = RuleStore()
        store.add("deny", r"https://evil\.com/.*", "Deny evil.com")
        result = store.match("https://evil.com/malware")
        assert result == "deny"

    def test_no_match_returns_none(self):
        """URLs that don't match any rule return None."""
        store = RuleStore()
        store.add("allow", r"https://example\.com/.*", "Allow example.com")
        result = store.match("https://other.com/page")
        assert result is None

    def test_deny_takes_priority_over_allow(self):
        """When both allow and deny rules match, deny wins."""
        store = RuleStore()
        store.add("allow", r"https://example\.com/.*", "Allow example.com")
        store.add("deny", r"https://example\.com/secret/.*", "Deny secret path")
        # This URL matches both rules
        result = store.match("https://example.com/secret/data")
        assert result == "deny"

    def test_expired_rule_ignored(self):
        """Expired rules are not considered during matching."""
        store = RuleStore()
        # Create a rule that expired 10 seconds ago
        store.add(
            "allow",
            r"https://example\.com/.*",
            "Expired allow",
            expires_at=time.time() - 10,
        )
        result = store.match("https://example.com/page")
        assert result is None

    def test_future_expiry_still_matches(self):
        """Rules with a future expiry time still match."""
        store = RuleStore()
        store.add(
            "allow",
            r"https://example\.com/.*",
            "Future allow",
            expires_at=time.time() + 3600,
        )
        result = store.match("https://example.com/page")
        assert result == "allow"

    def test_remove_rule(self):
        """Removing a rule makes it no longer match."""
        store = RuleStore()
        rule_id = store.add("deny", r"https://evil\.com/.*", "Deny evil.com")
        assert store.match("https://evil.com/page") == "deny"
        removed = store.remove(rule_id)
        assert removed is True
        assert store.match("https://evil.com/page") is None
        # Removing a non-existent rule returns False
        assert store.remove("nonexistent-id") is False

    def test_list_rules(self):
        """list_rules returns non-expired rules as dicts."""
        store = RuleStore()
        store.add("allow", r"https://a\.com/.*", "Allow A")
        store.add("deny", r"https://b\.com/.*", "Deny B")
        # Add an expired rule that should NOT appear
        store.add(
            "allow",
            r"https://expired\.com/.*",
            "Expired",
            expires_at=time.time() - 10,
        )
        rules = store.list_rules()
        assert len(rules) == 2
        assert all(isinstance(r, dict) for r in rules)
        labels = {r["label"] for r in rules}
        assert labels == {"Allow A", "Deny B"}

    def test_save_and_load(self, tmp_path: Path):
        """Rules can be saved to disk and loaded back."""
        store = RuleStore()
        store.add("allow", r"https://example\.com/.*", "Allow example")
        store.add("deny", r"https://evil\.com/.*", "Deny evil")

        filepath = tmp_path / "rules.json"
        store.save(str(filepath))

        # Verify the file is valid JSON
        with open(filepath) as f:
            data = json.load(f)
        assert isinstance(data, list)
        assert len(data) == 2

        # Load into a new store and verify
        store2 = RuleStore()
        store2.load(str(filepath))
        assert store2.match("https://example.com/page") == "allow"
        assert store2.match("https://evil.com/page") == "deny"

    def test_cleanup_expired(self):
        """cleanup_expired removes only expired rules."""
        store = RuleStore()
        store.add("allow", r"https://keep\.com/.*", "Keep this")
        store.add(
            "deny",
            r"https://expired\.com/.*",
            "Expired",
            expires_at=time.time() - 10,
        )
        store.add(
            "allow",
            r"https://also-expired\.com/.*",
            "Also expired",
            expires_at=time.time() - 5,
        )

        store.cleanup_expired()

        rules = store.list_rules()
        assert len(rules) == 1
        assert rules[0]["label"] == "Keep this"


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
