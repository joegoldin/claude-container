# proxy/tests/test_integration.py
"""Integration tests for the proxy — run with unit tests, skip expensive ones."""
import json
import os
import tempfile
import time

import pytest

from claude_proxy.rules import RuleStore
from claude_proxy.patterns import base_domain


class TestRulesPersistence:
    """Test that rules survive save/load cycle with real filesystem."""

    def test_full_cycle(self):
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

    def test_save_preserves_all_fields(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "test.json")
            s = RuleStore()
            rule_id = s.add("allow", r"^https?://example\.com(/.*)?$",
                           "example.com", time.time() + 3600, source="api")
            s.save(path)

            # Verify JSON structure
            with open(path) as f:
                data = json.load(f)
            assert len(data) == 1
            rule = data[0]
            assert rule["id"] == rule_id
            assert rule["rule_type"] == "allow"
            assert rule["pattern"] == r"^https?://example\.com(/.*)?$"
            assert rule["label"] == "example.com"
            assert rule["source"] == "api"
            assert rule["expires_at"] is not None

    def test_concurrent_add_match(self):
        """Test that concurrent add and match operations don't crash."""
        import threading

        store = RuleStore()
        errors = []

        def adder():
            try:
                for i in range(50):
                    store.add("allow", f"^https?://host{i}\\.com(/.*)?$",
                             f"host{i}.com", None)
            except Exception as e:
                errors.append(e)

        def matcher():
            try:
                for i in range(50):
                    store.match(f"https://host{i}.com/path")
            except Exception as e:
                errors.append(e)

        threads = [
            threading.Thread(target=adder),
            threading.Thread(target=matcher),
            threading.Thread(target=adder),
            threading.Thread(target=matcher),
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        assert len(errors) == 0, f"Concurrent errors: {errors}"
