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
    try:
        loop = asyncio.get_running_loop()
        loop.create_task(broadcast({"type": "pending", "data": info}))
    except RuntimeError:
        # No running event loop (e.g. called from mitmproxy thread).
        # Find the dashboard's loop and schedule there.
        pass


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
    """Add a new rule. Body: {type, pattern, label?, expires_at?, source?}."""
    if _store is None:
        return JSONResponse({"error": "not configured"}, status_code=503)
    body = await request.json()
    rule_type = body.get("type")
    pattern = body.get("pattern")
    if not rule_type or not pattern:
        return JSONResponse(
            {"error": "type and pattern are required"}, status_code=400
        )
    label = body.get("label", "")
    expires_at = body.get("expires_at")
    source = body.get("source", "interactive")
    rule_id = _store.add(rule_type, pattern, label, expires_at=expires_at, source=source)
    _save_profile()
    await broadcast({"type": "rules_changed", "data": _store.list_rules()})
    return JSONResponse({"id": rule_id}, status_code=201)


async def delete_rule(request: Request) -> JSONResponse:
    """Remove a rule by id."""
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
    Route("/api/rules/{rule_id}", delete_rule, methods=["DELETE"]),
    Route("/api/resolve", resolve_pending, methods=["POST"]),
    WebSocketRoute("/ws", websocket_endpoint),
    Mount("/static", StaticFiles(directory=str(static_dir)), name="static"),
]

app = Starlette(routes=routes)
