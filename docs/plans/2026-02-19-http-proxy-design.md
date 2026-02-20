# HTTP/S Proxy for Network Sandboxing

## Overview

An HTTP/HTTPS proxy sidecar that sits between Claude and the internet, providing interactive network access control. Users get real-time notifications when Claude attempts to contact unknown domains and can allow or deny requests with pattern-based rules that persist across sessions.

## Architecture

### System Components

```
Host Machine
├── claude-container CLI (Go) — orchestrates lifecycle
├── Browser — web dashboard at http://localhost:<port>
│
Docker Network (claude-net-<session>)
├── Claude Container
│   ├── HTTP_PROXY / HTTPS_PROXY → proxy-<session>:8080
│   ├── SSL_CERT_FILE → /proxy-ca/mitmproxy-ca-cert.pem
│   └── Claude Code (sandbox always active, network unrestricted when proxy handles it)
│
└── Proxy Sidecar Container
    ├── mitmproxy (library mode) — port 8080
    ├── Web dashboard (Starlette) — port 8081
    ├── Profile storage — /config/<profile>.json
    └── CA cert — /config/ca/mitmproxy-ca-cert.pem
```

### Key Decisions

- **Sidecar container** — proxy runs in its own container on a shared Docker network
- **mitmproxy as a library** — single Python process running both proxy and web dashboard
- **Full HTTPS inspection** — mitmproxy terminates TLS, enabling URL-level rule matching (requires CA cert in Claude container)
- **Block-and-hold** — unknown requests are held for up to 120s while waiting for user decision
- **Same repo** — proxy code lives in `proxy/` directory of claude-container

## Network Sandbox Modes

The `--network-sandbox` flag controls network enforcement:

| Mode | Proxy | Claude sandbox network | Claude sandbox other |
|------|-------|----------------------|---------------------|
| `proxy` | Running | Unrestricted (allowedDomains unset) | Active |
| `claude` (default) | Not running | Profile-based allowedDomains | Active |
| `both` | Running | Profile-based allowedDomains | Active |
| `none` | Not running | Unrestricted | Active |

The Claude sandbox is always active for non-network features (deny paths, permissions, etc.). When `--network-sandbox=proxy`, the managed-settings.json simply omits `allowedDomains` so Claude allows all network traffic — the proxy handles enforcement.

## Request Flow

```
Request arrives at proxy
  │
  ├── Match deny rules → 403 Forbidden (immediate)
  ├── Match allow rules → Forward transparently
  └── No match → HOLD connection
       ├── Notify dashboard via WebSocket (domain, URL, timestamp)
       ├── Fire browser Notification API alert
       ├── Go TUI polls /api/pending, shows "[!] Proxy: N pending"
       └── Wait up to 120s
            ├── User allows → add rule, forward request
            ├── User denies → add rule, return 403
            └── Timeout → 504 Gateway Timeout
```

## Rule Model

```json
{
  "id": "uuid",
  "pattern": "^https?://([^/]*\\.)?github\\.com(/.*)?$",
  "type": "allow",
  "label": "github.com (base domain)",
  "created_at": "2026-02-19T10:30:00Z",
  "expires_at": "2026-02-20T10:30:00Z",
  "source": "interactive"
}
```

All rules are regex internally. The dashboard presents presets:

| Preset | Example | Generated Regex |
|--------|---------|-----------------|
| Exact URL | `https://api.github.com/repos?page=1` | `^https://api\.github\.com/repos\?page=1$` |
| URL (no params) | `https://api.github.com/repos` | `^https://api\.github\.com/repos(\?.*)?$` |
| Subdomain | `api.github.com` | `^https?://api\.github\.com(/.*)?$` |
| Base domain | `github.com` | `^https?://([^/]*\.)?github\.com(/.*)?$` |
| Custom regex | (user types) | (used as-is) |

**Expiry durations:** forever, 15 minutes, 1 hour, 1 day, 1 week, 1 month.

Expired rules are ignored during matching and cleaned up periodically.

## Profiles

Proxy profiles are named collections of allow/deny rules, stored as JSON at `~/.config/claude-container/proxy-profiles/<name>.json`. The profile name is passed via `--proxy-profile` flag (default: `"default"`).

Multiple Claude sessions can share the same proxy profile. Rules added interactively in one session are immediately visible to other sessions using the same profile (the proxy reloads from disk or watches for changes).

## CLI Integration

### New flags

```
--network-sandbox string    Network enforcement: proxy|claude|both|none (default "claude")
--proxy-profile string      Proxy rule profile name (default "default")
--proxy-port int            Dashboard port on host (default 8081)
```

### Session lifecycle

When proxy is enabled (`proxy` or `both`):

1. CLI creates Docker network `claude-net-<session>`
2. Starts proxy sidecar with profile volume mount, exposed dashboard port
3. Waits for proxy healthcheck (`GET /api/health`)
4. Waits for CA cert file to exist in shared volume
5. Starts Claude container on same network with `HTTP_PROXY`, `HTTPS_PROXY`, `SSL_CERT_FILE` env vars
6. On session stop: stops both containers, removes network

### Session record changes

```go
type Session struct {
    // ... existing fields ...
    NetworkSandbox string `json:"network_sandbox,omitempty"` // proxy|claude|both|none
    ProxyProfile   string `json:"proxy_profile,omitempty"`
    ProxyPort      int    `json:"proxy_port,omitempty"`
}
```

## Proxy Application Structure

```
proxy/
├── pyproject.toml
├── claude_proxy/
│   ├── __init__.py
│   ├── app.py              # entry point: mitmproxy + web server
│   ├── addon.py            # mitmproxy addon (intercept, hold, check rules)
│   ├── rules.py            # rule store: load/save, match, expiry
│   ├── dashboard.py        # Starlette web app + WebSocket
│   └── patterns.py         # regex generators for presets
├── static/
│   ├── index.html
│   ├── app.js              # WebSocket client, Notification API, UI
│   └── style.css
└── tests/
    ├── test_rules.py
    ├── test_patterns.py
    └── test_addon.py
```

### Components

- **`app.py`** — starts mitmproxy master in a thread, runs Starlette/Uvicorn on port 8081. Reads `--profile` arg, loads `/config/<profile>.json`.
- **`addon.py`** — mitmproxy addon: `request(flow)` extracts domain+URL, checks rules. Uses `asyncio.Event` per pending flow to block until user responds.
- **`rules.py`** — thread-safe rule list. Load/save JSON. Match against compiled regex. Expiry checking.
- **`dashboard.py`** — REST API + WebSocket:
  - `GET /` — dashboard HTML
  - `GET /api/health` — healthcheck
  - `GET /api/pending` — pending requests (polled by Go TUI)
  - `GET /api/rules` — current rules
  - `POST /api/rules` — add rule
  - `DELETE /api/rules/{id}` — remove rule
  - `WS /ws` — real-time notifications + resolution
- **`patterns.py`** — `exact_url()`, `url_no_params()`, `subdomain()`, `base_domain()` regex generators.

## Web Dashboard

### Views

**Pending Requests (main):** Cards for each held request showing domain, full URL, countdown timer. Allow/deny radio buttons with pattern presets, duration dropdown.

**Rules:** Table of current profile rules — pattern, type, label, expiry, delete button.

**Profiles:** Dropdown to select/create/rename/delete profiles. Import/export JSON.

### Notifications

- Request `Notification.requestPermission()` on page load
- Fire browser `Notification` on new pending request via WebSocket
- Clicking notification focuses dashboard tab
- Go TUI polls `/api/pending` every 2s, shows `[!] Proxy: N pending` in status bar

## Nix / Docker Build

### Proxy sidecar image (`nix/proxy-image.nix`)

- `pkgs.dockerTools.buildLayeredImage`
- Contents: Python 3, mitmproxy, starlette, uvicorn
- Entrypoint: `python -m claude_proxy.app --profile $PROXY_PROFILE`
- Ports: 8080 (proxy), 8081 (dashboard)
- CA cert generated on first run at `/config/ca/`

### Flake outputs

```nix
packages.claude-proxy-image    # OCI image tarball
```

Wrapped binary gains `CLAUDE_PROXY_IMAGE_TARBALL` and `CLAUDE_PROXY_IMAGE_TAG` env vars.

### CA cert flow

1. Proxy starts, checks `/config/ca/mitmproxy-ca-cert.pem`
2. If missing, mitmproxy generates on first run
3. Claude container mounts same volume, `SSL_CERT_FILE` points to CA cert
4. Go CLI waits for cert file before starting Claude container

## Testing

- **`proxy/tests/test_rules.py`** — rule matching, expiry, persistence
- **`proxy/tests/test_patterns.py`** — regex generation correctness
- **`proxy/tests/test_addon.py`** — mitmproxy addon behavior with mock flows
- **Go integration test** — start proxy sidecar, make HTTP request through it, verify allow/deny
- **E2E** — spawn both containers, verify `curl` through proxy works for allowed domains, blocks unknown
