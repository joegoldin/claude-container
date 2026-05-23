"""Web dashboard backend for the Claude proxy.

Provides a Starlette web application with REST API endpoints and WebSocket
support for real-time management of proxy rules and pending requests.
"""

import asyncio
import json
import logging
from pathlib import Path
from typing import Optional

from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import FileResponse, JSONResponse
from starlette.routing import Mount, Route, WebSocketRoute
from starlette.staticfiles import StaticFiles
from starlette.websockets import WebSocket, WebSocketDisconnect

from claude_proxy.addon import ProxyAddon
from claude_proxy.rules import RuleStore

logger = logging.getLogger(__name__)

# Global refs set by app.py
_addon: Optional[ProxyAddon] = None
_store: Optional[RuleStore] = None
_profile_path: Optional[str] = None
_ws_clients: set[WebSocket] = set()
_dashboard_loop: Optional[asyncio.AbstractEventLoop] = None
_auth_token: Optional[str] = None


def set_auth_token(token: str) -> None:
    """Register the auth token required for mutating endpoints."""
    global _auth_token
    _auth_token = token


def _check_auth(request: Request) -> bool:
    """Return True if the request carries the correct auth token.

    Accepts the token via the X-Auth-Token header or the `token` query
    parameter (for browser links). When no token is configured (e.g. tests),
    auth is skipped.
    """
    if _auth_token is None:
        return True
    presented = request.headers.get("x-auth-token") or request.query_params.get("token")
    return presented == _auth_token


def _check_ws_auth(websocket: WebSocket) -> bool:
    """Same as _check_auth but for WebSocket connections."""
    if _auth_token is None:
        return True
    presented = websocket.headers.get("x-auth-token") or websocket.query_params.get("token")
    return presented == _auth_token


def set_dashboard_loop(loop: Optional[asyncio.AbstractEventLoop]) -> None:
    """Register the dashboard's asyncio event loop for cross-thread scheduling."""
    global _dashboard_loop
    _dashboard_loop = loop


def configure(addon: ProxyAddon, store: RuleStore, profile_path: str) -> None:
    """Called by app.py at startup to inject dependencies."""
    global _addon, _store, _profile_path
    _addon = addon
    _store = store
    _profile_path = profile_path


async def broadcast(message: dict) -> None:
    """Send JSON message to all connected WebSocket clients."""
    if not _ws_clients:
        return
    payload = json.dumps(message)
    stale: list[WebSocket] = []
    for ws in _ws_clients:
        try:
            await ws.send_text(payload)
        except Exception:
            stale.append(ws)
    for ws in stale:
        _ws_clients.discard(ws)


def on_pending_request(info: dict) -> None:
    """Callback for addon -- schedules broadcast to WS clients."""
    msg = {"type": "pending", "data": info}
    try:
        loop = asyncio.get_running_loop()
        loop.create_task(broadcast(msg))
    except RuntimeError:
        # No running event loop (e.g. called from mitmproxy thread).
        # Schedule on the dashboard's loop via call_soon_threadsafe.
        if _dashboard_loop is not None and not _dashboard_loop.is_closed():
            _dashboard_loop.call_soon_threadsafe(
                _dashboard_loop.create_task, broadcast(msg)
            )
        else:
            logger.warning(
                "on_pending_request: no dashboard loop available, notification dropped"
            )


# --- Route handlers ---


async def index(request: Request) -> FileResponse:
    """Serve index.html from the static directory."""
    static_dir = Path(__file__).parent.parent / "static"
    return FileResponse(static_dir / "index.html")


async def health(request: Request) -> JSONResponse:
    """Health check endpoint."""
    return JSONResponse({"status": "ok"})


async def get_pending(request: Request) -> JSONResponse:
    """Return list of pending requests from the addon."""
    if _addon is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    return JSONResponse(_addon.get_pending())


async def get_rules(request: Request) -> JSONResponse:
    """Return list of current rules from the store."""
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    return JSONResponse(_store.list_rules())


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


async def import_rules(request: Request) -> JSONResponse:
    """Replace the entire rule store with the uploaded JSON list.

    Body: a JSON array of rule objects in the same shape as
    `GET /api/rules` returns. The replacement is atomic from the API's
    point of view: the new rules are validated by the RuleStore loader
    via Rule.from_dict before any change is committed.

    Auth-token gated. Replace-only in v1; merge mode is not supported.
    """
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    try:
        body = await request.json()
    except Exception:
        return JSONResponse({"error": "body must be a JSON array"}, status_code=400)
    if not isinstance(body, list):
        return JSONResponse({"error": "body must be a JSON array"}, status_code=400)

    # Build Rule objects up front so a malformed entry rejects the whole
    # import without leaving the store half-replaced.
    from claude_proxy.rules import Rule  # local import avoids cycles
    try:
        new_rules = [Rule.from_dict(d) for d in body]
    except Exception as exc:
        return JSONResponse({"error": f"invalid rule: {exc}"}, status_code=400)

    # Replace under the store's lock by writing to a sibling list and swapping.
    with _store._lock:  # noqa: SLF001 — dashboard is the privileged surface
        _store._rules = new_rules  # noqa: SLF001
    _save_profile()
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"ok": True, "imported": len(new_rules)})


async def delete_rule(request: Request) -> JSONResponse:
    """Remove a rule by id."""
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    rule_id = request.path_params["rule_id"]
    removed = _store.remove(rule_id)
    if not removed:
        return JSONResponse({"error": "rule not found"}, status_code=404)
    _save_profile()
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"ok": True})


async def resolve_pending(request: Request) -> JSONResponse:
    """Resolve a pending flow. Body: {flow_id, action, pattern, label?, expires_at?}."""
    if not _check_auth(request):
        return JSONResponse({"error": "unauthorized"}, status_code=401)
    if _addon is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    body = await request.json()
    flow_id = body.get("flow_id")
    action = body.get("action")
    pattern = body.get("pattern")
    if not flow_id or not action or not pattern:
        return JSONResponse(
            {"error": "flow_id, action, and pattern are required"}, status_code=400
        )
    label = body.get("label", "")
    expires_at = body.get("expires_at")
    found = _addon.resolve(flow_id, action, pattern, label, expires_at=expires_at)
    if not found:
        return JSONResponse({"error": "flow not found"}, status_code=404)
    _save_profile()
    await broadcast({
        "type": "resolved",
        "data": {
            "flow_id": flow_id,
            "action": action,
            "pattern": pattern,
        },
    })
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"ok": True})


async def websocket_endpoint(websocket: WebSocket) -> None:
    """WebSocket endpoint for real-time updates."""
    if not _check_ws_auth(websocket):
        await websocket.close(code=1008)
        return
    await websocket.accept()
    _ws_clients.add(websocket)
    logger.info("WebSocket client connected (%d total)", len(_ws_clients))
    try:
        # Send initial state
        init_data = {
            "type": "init",
            "data": {
                "pending": _addon.get_pending() if _addon else [],
                "rules": _store.list_rules() if _store else [],
            },
        }
        await websocket.send_text(json.dumps(init_data))

        # Listen for messages from the client
        while True:
            text = await websocket.receive_text()
            try:
                msg = json.loads(text)
            except json.JSONDecodeError:
                continue

            if msg.get("type") == "resolve" and _addon is not None:
                data = msg.get("data", {})
                flow_id = data.get("flow_id")
                action = data.get("action")
                pattern = data.get("pattern")
                if flow_id and action and pattern:
                    label = data.get("label", "")
                    expires_at = data.get("expires_at")
                    found = _addon.resolve(
                        flow_id, action, pattern, label, expires_at=expires_at
                    )
                    if found:
                        _save_profile()
                        await broadcast({
                            "type": "resolved",
                            "data": {
                                "flow_id": flow_id,
                                "action": action,
                                "pattern": pattern,
                            },
                        })
                        await broadcast({
                            "type": "rules_changed",
                            "data": _store.list_rules() if _store else [],
                        })
    except WebSocketDisconnect:
        pass
    except Exception:
        logger.exception("WebSocket error")
    finally:
        _ws_clients.discard(websocket)
        logger.info("WebSocket client disconnected (%d remaining)", len(_ws_clients))


def _save_profile() -> None:
    """Persist rules to the profile JSON file."""
    if _store is not None and _profile_path is not None:
        try:
            _store.save(_profile_path)
        except Exception:
            logger.exception("Failed to save profile to %s", _profile_path)


# --- Build the Starlette application ---

static_dir = Path(__file__).parent.parent / "static"

routes = [
    Route("/", index),
    Route("/api/health", health),
    Route("/api/pending", get_pending),
    Route("/api/rules", get_rules, methods=["GET"]),
    Route("/api/rules", add_rule, methods=["POST"]),
    Route("/api/rules/import", import_rules, methods=["POST"]),
    Route("/api/rules/{rule_id}", delete_rule, methods=["DELETE"]),
    Route("/api/resolve", resolve_pending, methods=["POST"]),
    WebSocketRoute("/ws", websocket_endpoint),
    Mount("/static", StaticFiles(directory=str(static_dir)), name="static"),
]

app = Starlette(routes=routes)
