# Dynamic Package Management & Proxy Fixes

## Problem

Containers currently have a fixed set of software baked into the Nix image. When an agent needs a tool (rust, nodejs, etc.) mid-session, there's no way to install it. Users also can't pre-configure packages per session or workspace.

Additionally, the proxy has three bugs:
1. Pending request notifications silently fail (WebSocket broadcast from mitmproxy thread drops)
2. Dashboard has no UI for manually adding rules (only resolving pending requests or deleting existing rules)
3. No error handling/logging in the proxy addon request hook

## Design

### 1. Nix-in-Container

Add `nix` (single-user, no daemon) to the container image.

**Image changes (`nix/image.nix`):**
- Add `nix` package to `pathPackages`
- Add `/etc/nix/nix.conf` with `experimental-features = nix-command flakes` and `sandbox = false`
- Pre-configure nixpkgs registry so `nixpkgs#` references work without channels

**Persistent volume:**
- Docker volume for Nix mutable state (profiles, db) mounted at `/nix/var`
- Base `/nix/store` paths from image layers remain available
- Runtime installs cached in volume across container restarts

**Entrypoint changes:**
- New env var `EXTRA_PACKAGES` (comma-separated nixpkgs attribute names)
- If set, entrypoint runs `nix profile install nixpkgs#pkg1 nixpkgs#pkg2 ...` before launching Claude
- Nix profile bin directory added to PATH

**Sandbox permissions (`managed-settings.nix`):**
- Allow specific nix subcommands only:
  - `"nix profile install *"`
  - `"nix profile remove *"`
  - `"nix profile list *"`
  - `"nix search *"`
- Do NOT allow `"nix *"` — that would let the agent run arbitrary commands via `nix run`/`nix shell`

**Agent hints (managed settings):**
- Add instructions to managed settings telling the agent to use `nix profile install nixpkgs#<package>` for software installation
- Mention `nix search nixpkgs <query>` for finding packages
- Explicitly state that apt-get/yum/etc. are not available

### 2. CLI / Workspace / TUI Configuration

**CLI flag:**
- `claude-container new --packages rust,nodejs,python3`
- Comma-separated nixpkgs attribute names
- Stored in session config as `Packages []string`

**Workspace config:**
- Add `Packages []string` field to workspace struct
- When creating a session from a workspace, workspace packages are used as defaults
- CLI `--packages` flag overrides workspace packages

**TUI wizard:**
- Add "Packages" step in the new-session wizard
- Text input for comma-separated package names
- Also shown in workspace edit UI

**Session persistence:**
- `sessions.json` records requested packages
- On container restart, same packages re-installed (fast from Nix store cache)

### 3. Docker Integration (`internal/docker/docker.go`)

**RunArgs changes:**
- Pass `EXTRA_PACKAGES` env var from session config
- Add `-v claude-nix-store:/nix/var` volume mount for persistent Nix state
- Volume name can be shared across sessions for cache reuse

### 4. Proxy Bug Fixes

**Bug 1 — Silent WebSocket notification failure (`proxy/claude_proxy/dashboard.py:55-63`):**

Current code:
```python
def on_pending_request(info: dict) -> None:
    try:
        loop = asyncio.get_running_loop()
        loop.create_task(broadcast({"type": "pending", "data": info}))
    except RuntimeError:
        pass  # BUG: silently drops notification
```

Fix: Store the dashboard's event loop at startup. Use `call_soon_threadsafe()` from the mitmproxy thread:
```python
_dashboard_loop: Optional[asyncio.AbstractEventLoop] = None

def on_pending_request(info: dict) -> None:
    if _dashboard_loop is not None:
        _dashboard_loop.call_soon_threadsafe(
            _dashboard_loop.create_task,
            broadcast({"type": "pending", "data": info})
        )
```

Set `_dashboard_loop` when the Starlette/uvicorn server starts.

**Bug 2 — No manual rule addition in dashboard UI:**
- Add "Add Rule" form to the rules view in `static/app.js` and `static/index.html`
- Fields: pattern (text input), label (text input), type (allow/deny toggle), duration (dropdown)
- Calls existing `POST /api/rules` endpoint
- Include pattern preset helpers (subdomain, base domain) similar to the resolve UI

**Bug 3 — Missing error handling in addon:**
- Wrap `addon.request()` body in try/except
- Log errors to stderr via Python logging
- Visible in `docker logs <proxy-container>`

## Files to Modify

### Nix / Image
- `nix/image.nix` — add nix package, nix.conf, nixpkgs registry config
- `nix/managed-settings.nix` — add nix subcommand permissions and agent hints

### Go / CLI
- `internal/docker/docker.go` — add EXTRA_PACKAGES env var, nix store volume mount
- `internal/config/config.go` — add Packages field to Session and Workspace structs
- `cmd/new.go` — add --packages flag, pass to RunOpts
- `internal/tui/` — add packages step to wizard and workspace edit

### Proxy
- `proxy/claude_proxy/dashboard.py` — fix event loop coordination, store loop reference
- `proxy/claude_proxy/addon.py` — add error handling/logging in request()
- `proxy/static/app.js` — add manual rule creation UI
- `proxy/static/index.html` — add rule form markup (if separate from app.js)

## Non-Goals

- No apt-get/pip wrapper aliases (fragile, misleading)
- No derived image builds (too complex for the UX gain)
- No nix daemon (single-user mode is sufficient in a container)
- No CONNECT-level tunnel interception (mitmproxy handles tunnels correctly; the real bug is the notification failure)
