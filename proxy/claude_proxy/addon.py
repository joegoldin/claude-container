"""mitmproxy addon that intercepts HTTP requests and checks them against the rule store.

Requests matching an allow rule pass through, requests matching a deny rule are
killed, and requests with no matching rule are held pending until resolved by
the web dashboard.
"""

import threading
import time
from typing import Callable, Optional

from claude_proxy.rules import RuleStore


class ProxyAddon:
    """mitmproxy addon that enforces URL allow/deny rules.

    Unknown requests are held in a pending dict until resolved via the
    dashboard. All public methods are thread-safe.
    """

    def __init__(
        self,
        rule_store: RuleStore,
        on_pending: Optional[Callable[[dict], None]] = None,
        hold_timeout: float = 120,
    ) -> None:
        self.store = rule_store
        self.on_pending = on_pending
        self.hold_timeout = hold_timeout
        self.pending: dict[str, dict] = {}
        self._lock = threading.Lock()

    def request(self, flow) -> None:
        """Called by mitmproxy for each intercepted request.

        Checks the URL against the rule store. Allowed requests pass through,
        denied requests are killed, and unknown requests are held as pending.
        """
        url = flow.request.pretty_url
        action = self.store.match(url)

        if action == "allow":
            return
        if action == "deny":
            flow.kill()
            return

        # Unknown — hold the flow as pending
        entry = {
            "flow": flow,
            "flow_id": flow.id,
            "url": url,
            "host": flow.request.host,
            "time": time.time(),
        }

        with self._lock:
            self.pending[flow.id] = entry

        if self.on_pending is not None:
            self.on_pending(
                {
                    "flow_id": flow.id,
                    "url": url,
                    "host": flow.request.host,
                    "time": entry["time"],
                }
            )

    def resolve(
        self,
        flow_id: str,
        action: str,
        pattern: str,
        label: str,
        expires_at: Optional[float] = None,
    ) -> bool:
        """Resolve a pending request by adding a rule and releasing or killing the flow.

        Returns True if the flow was found in pending, False otherwise.
        """
        with self._lock:
            entry = self.pending.pop(flow_id, None)

        if entry is None:
            return False

        self.store.add(action, pattern, label, expires_at=expires_at)

        if action == "deny":
            entry["flow"].kill()

        return True

    def get_pending(self) -> list[dict]:
        """Return a list of all pending requests as serializable dicts."""
        with self._lock:
            return [
                {
                    "flow_id": entry["flow_id"],
                    "url": entry["url"],
                    "host": entry["host"],
                    "time": entry["time"],
                }
                for entry in self.pending.values()
            ]

    def cleanup_timed_out(self) -> list[str]:
        """Kill and remove flows that have been pending longer than hold_timeout.

        Returns a list of flow IDs that were timed out.
        """
        now = time.time()
        timed_out_ids: list[str] = []

        with self._lock:
            to_remove = []
            for flow_id, entry in self.pending.items():
                if now - entry["time"] >= self.hold_timeout:
                    to_remove.append(flow_id)

            for flow_id in to_remove:
                entry = self.pending.pop(flow_id)
                entry["flow"].kill()
                timed_out_ids.append(flow_id)

        return timed_out_ids
