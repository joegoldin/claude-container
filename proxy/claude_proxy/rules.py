"""Rule engine for the HTTP/HTTPS proxy.

Provides a thread-safe rule store that matches URLs against allow/deny
regex patterns. Deny rules take priority over allow rules.
"""

import json
import re
import threading
import time
import uuid
from dataclasses import dataclass, field
from functools import lru_cache
from typing import Optional


@dataclass
class Rule:
    """A single allow or deny rule with a regex pattern."""

    id: str = field(default_factory=lambda: str(uuid.uuid4()))
    rule_type: str = "allow"  # "allow" or "deny"
    pattern: str = ""
    label: str = ""
    created_at: float = field(default_factory=time.time)
    expires_at: Optional[float] = None
    source: str = "interactive"

    def is_expired(self) -> bool:
        """Return True if the rule has passed its expiration time."""
        if self.expires_at is None:
            return False
        return time.time() > self.expires_at

    def compiled(self) -> re.Pattern:
        """Return the compiled regex pattern (cached)."""
        return _compile_pattern(self.pattern)

    def to_dict(self) -> dict:
        """Serialize the rule to a JSON-compatible dict."""
        return {
            "id": self.id,
            "rule_type": self.rule_type,
            "pattern": self.pattern,
            "label": self.label,
            "created_at": self.created_at,
            "expires_at": self.expires_at,
            "source": self.source,
        }

    @classmethod
    def from_dict(cls, data: dict) -> "Rule":
        """Deserialize a rule from a dict."""
        return cls(
            id=data["id"],
            rule_type=data["rule_type"],
            pattern=data["pattern"],
            label=data["label"],
            created_at=data["created_at"],
            expires_at=data.get("expires_at"),
            source=data.get("source", "interactive"),
        )


@lru_cache(maxsize=256)
def _compile_pattern(pattern: str) -> re.Pattern:
    """Compile and cache a regex pattern."""
    return re.compile(pattern)


class RuleStore:
    """Thread-safe store for URL allow/deny rules.

    Deny rules always take priority over allow rules when both match.
    """

    def __init__(self) -> None:
        self._rules: list[Rule] = []
        self._lock = threading.Lock()

    def add(
        self,
        rule_type: str,
        pattern: str,
        label: str,
        expires_at: Optional[float] = None,
        source: str = "interactive",
    ) -> str:
        """Add a new rule and return its id."""
        rule = Rule(
            rule_type=rule_type,
            pattern=pattern,
            label=label,
            expires_at=expires_at,
            source=source,
        )
        with self._lock:
            self._rules.append(rule)
        return rule.id

    def remove(self, rule_id: str) -> bool:
        """Remove a rule by id. Returns True if the rule was found and removed."""
        with self._lock:
            for i, rule in enumerate(self._rules):
                if rule.id == rule_id:
                    self._rules.pop(i)
                    return True
        return False

    def match(self, url: str) -> Optional[str]:
        """Match a URL against all non-expired rules.

        Returns "allow", "deny", or None. Deny takes priority over allow.
        """
        allow = False
        with self._lock:
            for rule in self._rules:
                if rule.is_expired():
                    continue
                if rule.compiled().search(url):
                    if rule.rule_type == "deny":
                        return "deny"
                    allow = True
        return "allow" if allow else None

    def list_rules(self) -> list[dict]:
        """Return all non-expired rules as dicts."""
        with self._lock:
            return [rule.to_dict() for rule in self._rules if not rule.is_expired()]

    def cleanup_expired(self) -> None:
        """Remove all expired rules from the store."""
        with self._lock:
            self._rules = [rule for rule in self._rules if not rule.is_expired()]

    def save(self, path: str) -> None:
        """Save all rules to a JSON file."""
        with self._lock:
            data = [rule.to_dict() for rule in self._rules]
        with open(path, "w") as f:
            json.dump(data, f, indent=2)

    def load(self, path: str) -> None:
        """Load rules from a JSON file, replacing current rules."""
        with open(path) as f:
            data = json.load(f)
        rules = [Rule.from_dict(d) for d in data]
        with self._lock:
            self._rules = rules
