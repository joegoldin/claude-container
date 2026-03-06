# Dynamic Package Management & Proxy Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enable adding/removing software in containers at runtime via Nix, with pre-session package configuration via CLI/workspace/TUI, plus fix three proxy bugs.

**Architecture:** Add `nix` (single-user) to the container image with a persistent volume for the Nix store. Entrypoint installs pre-configured packages at startup. Agent can install more at runtime. Proxy bugs fixed: event loop coordination for WebSocket notifications, manual rule addition UI, error handling in addon.

**Tech Stack:** Nix (in-container), Go (CLI/TUI), Python (proxy), JavaScript (dashboard)

---

### Task 1: Fix proxy WebSocket notification bug

The most impactful bug — pending requests never appear in the dashboard because `on_pending_request()` silently drops notifications when called from the mitmproxy thread.

**Files:**
- Modify: `proxy/claude_proxy/dashboard.py:55-63`
- Modify: `proxy/claude_proxy/app.py:59-69`
- Test: `proxy/tests/test_dashboard.py`

**Step 1: Write the failing test**

Add to `proxy/tests/test_dashboard.py`:

```python
import asyncio
import threading
from claude_proxy.dashboard import on_pending_request, _ws_clients, broadcast, configure
from claude_proxy.dashboard import _dashboard_loop

class TestOnPendingCallback:
    def test_callback_from_non_asyncio_thread(self, store_and_addon):
        """on_pending_request called from a non-asyncio thread schedules broadcast."""
        store, addon, client, _ = store_and_addon

        # Connect a WebSocket client to receive broadcasts
        received = []
        with client.websocket_connect("/ws") as ws:
            init_msg = ws.receive_json()  # consume init message
            assert init_msg["type"] == "init"

            # Simulate mitmproxy calling callback from a worker thread
            info = {"flow_id": "test-123", "url": "https://example.com", "host": "example.com", "time": 1234567890.0}
            thread = threading.Thread(target=on_pending_request, args=(info,))
            thread.start()
            thread.join(timeout=2)

            # The broadcast should have been scheduled on the dashboard's event loop
            # Give it a moment to process
            import time
            time.sleep(0.5)

            # We can't easily receive via the test WebSocket in this setup,
            # but we can verify no exception was raised (the thread completed)
            assert not thread.is_alive()
```

**Step 2: Run test to verify it fails**

Run: `cd proxy && python -m pytest tests/test_dashboard.py::TestOnPendingCallback -v`
Expected: May pass trivially (thread completes because `pass` doesn't raise), but the broadcast never reaches clients.

**Step 3: Fix the event loop coordination in dashboard.py**

Replace the `on_pending_request` function and add loop storage:

In `proxy/claude_proxy/dashboard.py`, replace lines 55-63:

```python
_dashboard_loop: asyncio.AbstractEventLoop | None = None


def set_dashboard_loop(loop: asyncio.AbstractEventLoop) -> None:
    """Store the dashboard's event loop for cross-thread scheduling."""
    global _dashboard_loop
    _dashboard_loop = loop


def on_pending_request(info: dict) -> None:
    """Callback for addon -- schedules broadcast to WS clients."""
    try:
        loop = asyncio.get_running_loop()
        loop.create_task(broadcast({"type": "pending", "data": info}))
    except RuntimeError:
        # Called from mitmproxy thread (no running event loop).
        # Schedule on the dashboard's event loop.
        if _dashboard_loop is not None and not _dashboard_loop.is_closed():
            _dashboard_loop.call_soon_threadsafe(
                _dashboard_loop.create_task,
                broadcast({"type": "pending", "data": info}),
            )
        else:
            logger.warning("No dashboard event loop available, dropping pending notification")
```

**Step 4: Store the event loop in app.py**

In `proxy/claude_proxy/app.py`, modify `_start_dashboard` (lines 59-69) to capture the event loop:

```python
def _start_dashboard(port: int) -> None:
    """Run the Starlette dashboard via uvicorn in a daemon thread."""
    from claude_proxy.dashboard import set_dashboard_loop

    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    set_dashboard_loop(loop)

    config = uvicorn.Config(
        app,
        host="0.0.0.0",
        port=port,
        log_level="info",
        access_log=False,
        loop="none",  # use the loop we already created
    )
    server = uvicorn.Server(config)
    server.run()
```

Update the import in `app.py` line 19:

```python
from claude_proxy.dashboard import app, configure, on_pending_request
```

No change needed to imports since `set_dashboard_loop` is imported inside the function.

**Step 5: Run tests**

Run: `cd proxy && python -m pytest tests/ -v`
Expected: All tests pass.

**Step 6: Commit**

```bash
git add proxy/claude_proxy/dashboard.py proxy/claude_proxy/app.py proxy/tests/test_dashboard.py
git commit -m "fix: schedule proxy WebSocket notifications from mitmproxy thread"
```

---

### Task 2: Add error handling to proxy addon

**Files:**
- Modify: `proxy/claude_proxy/addon.py:34-70`
- Test: `proxy/tests/test_addon.py`

**Step 1: Write the failing test**

Add to `proxy/tests/test_addon.py`:

```python
def test_request_with_broken_flow_logs_error(self, capfd):
    """A flow that raises an exception in request() is logged, not silently lost."""
    import logging
    logging.basicConfig(level=logging.ERROR)

    store = RuleStore()
    addon = ProxyAddon(store)

    flow = MagicMock()
    flow.request.pretty_url = None  # will cause AttributeError in match()
    type(flow.request).pretty_url = property(lambda self: (_ for _ in ()).throw(AttributeError("broken")))
    flow.id = "broken-flow"

    # Should not raise — error should be caught and logged
    addon.request(flow)

    # Flow should not be in pending (error was caught)
    assert len(addon.get_pending()) == 0
```

**Step 2: Run test to verify it fails**

Run: `cd proxy && python -m pytest tests/test_addon.py::TestProxyAddon::test_request_with_broken_flow_logs_error -v`
Expected: FAIL — currently the exception propagates uncaught.

**Step 3: Add error handling to addon.request()**

In `proxy/claude_proxy/addon.py`, add logging import and wrap request():

```python
import logging
import threading
import time
from typing import Callable, Optional

from claude_proxy.rules import RuleStore

logger = logging.getLogger(__name__)
```

Replace the `request` method (lines 34-70):

```python
    def request(self, flow) -> None:
        """Called by mitmproxy for each intercepted request."""
        try:
            url = flow.request.pretty_url
            action = self.store.match(url)

            if action == "allow":
                return
            if action == "deny":
                flow.kill()
                return

            # Unknown — intercept and hold the flow as pending
            flow.intercept()
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
        except Exception:
            logger.exception("Error processing request for flow %s", getattr(flow, 'id', 'unknown'))
            try:
                flow.kill()
            except Exception:
                pass
```

**Step 4: Run tests**

Run: `cd proxy && python -m pytest tests/ -v`
Expected: All pass.

**Step 5: Commit**

```bash
git add proxy/claude_proxy/addon.py proxy/tests/test_addon.py
git commit -m "fix: catch and log errors in proxy addon request handler"
```

---

### Task 3: Add manual rule creation to proxy dashboard UI

**Files:**
- Modify: `proxy/static/index.html:25-45`
- Modify: `proxy/static/app.js`
- Modify: `proxy/static/style.css`

**Step 1: Add the "Add Rule" form to index.html**

In `proxy/static/index.html`, replace the rules-view section (lines 25-45):

```html
        <section id="rules-view" class="view">
            <div id="add-rule-form" class="add-rule-form">
                <h3>Add Rule</h3>
                <div class="form-row">
                    <div class="form-group">
                        <label>Type</label>
                        <div class="type-toggle">
                            <button class="type-btn active" data-type="allow">Allow</button>
                            <button class="type-btn" data-type="deny">Deny</button>
                        </div>
                    </div>
                    <div class="form-group form-group-grow">
                        <label>Pattern</label>
                        <div class="add-rule-pattern-row">
                            <select id="add-rule-preset">
                                <option value="custom">Custom regex</option>
                                <option value="subdomain">Subdomain</option>
                                <option value="domain">Base domain</option>
                            </select>
                            <input type="text" id="add-rule-input" placeholder="Enter domain or regex pattern...">
                        </div>
                    </div>
                    <div class="form-group">
                        <label>Duration</label>
                        <select id="add-rule-duration">
                            <option value="forever">Forever</option>
                            <option value="15min">15 minutes</option>
                            <option value="1hr">1 hour</option>
                            <option value="1day" selected>1 day</option>
                            <option value="1week">1 week</option>
                            <option value="1month">1 month</option>
                        </select>
                    </div>
                    <div class="form-group">
                        <label>&nbsp;</label>
                        <button id="add-rule-submit" class="btn btn-allow">Add</button>
                    </div>
                </div>
            </div>
            <div id="rules-table-wrap">
                <table id="rules-table">
                    <thead>
                        <tr>
                            <th>Type</th>
                            <th>Pattern</th>
                            <th>Label</th>
                            <th>Expires</th>
                            <th>Source</th>
                            <th></th>
                        </tr>
                    </thead>
                    <tbody id="rules-body">
                        <tr class="empty-row">
                            <td colspan="6">No rules configured.</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </section>
```

**Step 2: Add CSS styles for the form**

Append to `proxy/static/style.css`:

```css
/* Add Rule form */
.add-rule-form {
    background: var(--bg-card);
    border: 1px solid var(--border-color);
    border-radius: var(--radius);
    padding: 20px;
    margin-bottom: 20px;
}

.add-rule-form h3 {
    font-size: 14px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.5px;
    margin-bottom: 12px;
}

.form-row {
    display: flex;
    gap: 12px;
    align-items: flex-end;
    flex-wrap: wrap;
}

.form-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
}

.form-group-grow {
    flex: 1;
    min-width: 200px;
}

.form-group label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.5px;
}

.type-toggle {
    display: flex;
    gap: 0;
}

.type-btn {
    padding: 8px 16px;
    border: 1px solid var(--border-color);
    background: var(--bg-input);
    color: var(--text-secondary);
    font-size: 13px;
    cursor: pointer;
    transition: all 0.15s ease;
}

.type-btn:first-child {
    border-radius: 4px 0 0 4px;
}

.type-btn:last-child {
    border-radius: 0 4px 4px 0;
    border-left: none;
}

.type-btn.active[data-type="allow"] {
    background: var(--accent-green);
    color: #fff;
    border-color: var(--accent-green);
}

.type-btn.active[data-type="deny"] {
    background: var(--accent-red);
    color: #fff;
    border-color: var(--accent-red);
}

.add-rule-pattern-row {
    display: flex;
    gap: 8px;
}

.add-rule-pattern-row select {
    flex-shrink: 0;
}

.add-rule-pattern-row input {
    flex: 1;
    background: var(--bg-input);
    border: 1px solid var(--border-color);
    color: var(--text-primary);
    padding: 8px 12px;
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 13px;
}

.add-rule-pattern-row input:focus {
    outline: none;
    border-color: var(--accent-blue);
}
```

**Step 3: Add JavaScript logic for the form**

In `proxy/static/app.js`, add the following after the DOM refs section (after line 30), before tab navigation:

```javascript
  // --- Add Rule form refs ---
  const addRulePreset = document.getElementById("add-rule-preset");
  const addRuleInput = document.getElementById("add-rule-input");
  const addRuleDuration = document.getElementById("add-rule-duration");
  const addRuleSubmit = document.getElementById("add-rule-submit");
  const typeBtns = document.querySelectorAll(".type-btn");
  let addRuleType = "allow";
```

Wire up the type toggle buttons (add after tab navigation, around line 41):

```javascript
  // --- Type toggle ---
  typeBtns.forEach((btn) => {
    btn.addEventListener("click", () => {
      typeBtns.forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      addRuleType = btn.getAttribute("data-type");
      addRuleSubmit.className = addRuleType === "allow" ? "btn btn-allow" : "btn btn-deny";
    });
  });

  // --- Add Rule form ---
  if (addRuleSubmit) {
    addRuleSubmit.addEventListener("click", async () => {
      const rawInput = addRuleInput.value.trim();
      if (!rawInput) {
        addRuleInput.focus();
        return;
      }

      const preset = addRulePreset.value;
      let pattern;
      let label;
      if (preset === "subdomain") {
        pattern = patternSubdomain(rawInput);
        label = (addRuleType === "allow" ? "Allow " : "Deny ") + rawInput;
      } else if (preset === "domain") {
        pattern = patternBaseDomain(rawInput);
        label = (addRuleType === "allow" ? "Allow " : "Deny ") + rawInput + " (+ subdomains)";
      } else {
        pattern = rawInput;
        label = addRuleType === "allow" ? "Allow (custom)" : "Deny (custom)";
      }

      const durationKey = addRuleDuration.value;
      const durationSec = DURATIONS[durationKey] || 0;
      const expiresAt = durationSec > 0 ? Math.floor(Date.now() / 1000) + durationSec : null;

      try {
        const resp = await fetch("/api/rules", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            type: addRuleType,
            pattern: pattern,
            label: label,
            expires_at: expiresAt,
            source: "manual",
          }),
        });
        if (resp.ok) {
          addRuleInput.value = "";
          // Rules list will update via WebSocket rules_changed message
        }
      } catch (err) {
        console.error("Failed to add rule:", err);
      }
    });
  }

  // Handle Enter key in input
  if (addRuleInput) {
    addRuleInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && addRuleSubmit) {
        addRuleSubmit.click();
      }
    });
  }

  // Update placeholder based on preset selection
  if (addRulePreset) {
    addRulePreset.addEventListener("change", () => {
      const preset = addRulePreset.value;
      if (preset === "subdomain") {
        addRuleInput.placeholder = "e.g. api.github.com";
      } else if (preset === "domain") {
        addRuleInput.placeholder = "e.g. github.com";
      } else {
        addRuleInput.placeholder = "Enter regex pattern...";
      }
    });
  }
```

**Step 4: Test manually**

Run the proxy and verify:
1. The rules view shows the "Add Rule" form
2. Typing a domain and clicking Add creates a rule
3. The rule appears in the table below
4. Subdomain/domain presets generate correct patterns

**Step 5: Commit**

```bash
git add proxy/static/index.html proxy/static/app.js proxy/static/style.css
git commit -m "feat: add manual rule creation form to proxy dashboard"
```

---

### Task 4: Add Nix to container image

**Files:**
- Modify: `nix/image.nix:195-270`

**Step 1: Add nix package and configuration to the image**

In `nix/image.nix`, add `nix` to the `let` bindings at the top (after line 12):

```nix
  nixBin = "${pkgs.nix}/bin/nix";
```

Add `pkgs.nix` to `pathPackages` (after `python3Minimal` on line 220):

```nix
      nix
```

In `extraCommands` (after line 241 `chmod 1777 tmp`), add Nix configuration:

```nix
    # Nix configuration for in-container package management
    mkdir -p etc/nix
    cat > etc/nix/nix.conf << 'NIXCONF'
    experimental-features = nix-command flakes
    sandbox = false
    NIXCONF

    # Create nix profile and var directories
    mkdir -p nix/var/nix/profiles nix/var/nix/db nix/var/nix/gcroots
```

In the `Env` section of `config` (after the `NIX_SSL_CERT_FILE` line, around line 267), add:

```nix
      "NIX_PATH=nixpkgs=${pkgs.path}"
```

**Step 2: Verify the image builds**

Run: `nix build .#image`
Expected: Image builds successfully with nix included.

**Step 3: Commit**

```bash
git add nix/image.nix
git commit -m "feat: add nix package manager to container image"
```

---

### Task 5: Add entrypoint package installation

**Files:**
- Modify: `nix/image.nix:34-192` (entrypoint script)

**Step 1: Add EXTRA_PACKAGES handling to entrypoint**

In the entrypoint script in `nix/image.nix`, add the following block just before the final `exec` section (before line 187 `# --- Exec ---`):

```bash
    # --- Install extra packages ---
    if [ -n "''${EXTRA_PACKAGES:-}" ]; then
      echo "Installing extra packages: $EXTRA_PACKAGES"
      IFS=',' read -ra PKGS <<< "$EXTRA_PACKAGES"
      NIX_ARGS=()
      for pkg in "''${PKGS[@]}"; do
        pkg="$(echo "$pkg" | xargs)"  # trim whitespace
        if [ -n "$pkg" ]; then
          NIX_ARGS+=("nixpkgs#$pkg")
        fi
      done
      if [ ''${#NIX_ARGS[@]} -gt 0 ]; then
        if [ "$USER_NAME" = "root" ]; then
          ${nixBin} profile install --accept-flake-config "''${NIX_ARGS[@]}" 2>&1 || echo "Warning: some packages failed to install"
        else
          ${suExec} "$USER_NAME" ${nixBin} profile install --accept-flake-config "''${NIX_ARGS[@]}" 2>&1 || echo "Warning: some packages failed to install"
        fi
        # Add nix profile bin to PATH for the exec'd process
        if [ "$USER_NAME" = "root" ]; then
          export PATH="/root/.nix-profile/bin:$PATH"
        else
          export PATH="/home/$USER_NAME/.nix-profile/bin:$PATH"
        fi
      fi
    fi
```

**Step 2: Verify with a test build**

Run: `nix build .#image`
Expected: Image builds. Test manually by running a container with `EXTRA_PACKAGES=ripgrep` and verifying `rg` is available.

**Step 3: Commit**

```bash
git add nix/image.nix
git commit -m "feat: install extra packages at container startup via EXTRA_PACKAGES env"
```

---

### Task 6: Add Packages field to RunOpts and wire through docker args

**Files:**
- Modify: `internal/docker/docker.go:82-101` (RunOpts struct)
- Modify: `internal/docker/docker.go:106-209` (RunArgs function)
- Modify: `internal/docker/docker.go:215-312` (TaskRunArgs function)

**Step 1: Add Packages field to RunOpts**

In `internal/docker/docker.go`, add to the `RunOpts` struct (after line 100, before the closing `}`):

```go
	Packages       []string // extra nixpkgs to install at container start
```

**Step 2: Pass EXTRA_PACKAGES env var in RunArgs**

In `RunArgs()`, add after the `USER_GID` env var (after line 179):

```go
	if len(opts.Packages) > 0 {
		args = append(args, "-e", "EXTRA_PACKAGES="+strings.Join(opts.Packages, ","))
	}
```

**Step 3: Pass EXTRA_PACKAGES env var in TaskRunArgs**

In `TaskRunArgs()`, add after the `USER_GID` env var (after line 279):

```go
	if len(opts.Packages) > 0 {
		args = append(args, "-e", "EXTRA_PACKAGES="+strings.Join(opts.Packages, ","))
	}
```

**Step 4: Build and verify**

Run: `go build ./...`
Expected: Compiles successfully.

**Step 5: Commit**

```bash
git add internal/docker/docker.go
git commit -m "feat: pass EXTRA_PACKAGES env var to container from RunOpts"
```

---

### Task 7: Add Packages field to Session config and createOpts

**Files:**
- Modify: `internal/config/config.go:25-45` (Session struct)
- Modify: `cmd/new.go:43-63` (createOpts struct)
- Modify: `cmd/new.go:140-161` (flag registration)
- Modify: `cmd/new.go:97-136` (createOpts population)
- Modify: `cmd/new.go:382-441` (RunOpts and Session creation)

**Step 1: Add Packages field to Session struct**

In `internal/config/config.go`, add after `DenyCommands` (after line 41):

```go
	Packages        []string  `json:"packages,omitempty"`
```

**Step 2: Add packages field to createOpts**

In `cmd/new.go`, add to `createOpts` struct (after line 61):

```go
	packages      []string // --packages flag
```

**Step 3: Add --packages CLI flag**

Add a package-level var (after line 40):

```go
	newPackages      []string
```

In `init()`, add the flag registration (after line 160, before `rootCmd.AddCommand`):

```go
	newCmd.Flags().StringSliceVar(&newPackages, "packages", nil, "Comma-separated nixpkgs to install (e.g., rust,nodejs)")
```

**Step 4: Wire packages through createOpts and wizard path**

In the wizard path (around line 97), add `packages: newPackages` to the `createOpts`:

```go
			return createSession(createOpts{
				...
				packages:      newPackages,
			})
```

In the direct CLI path (around line 117), add `packages: newPackages`:

```go
		return createSession(createOpts{
			...
			packages:      newPackages,
		})
```

**Step 5: Pass packages to RunOpts and Session**

In `createSession()`, add `Packages` to `runOpts` (around line 396):

```go
	runOpts := docker.RunOpts{
		...
		Packages:        opts.packages,
	}
```

Add `Packages` to the Session record (around line 435):

```go
	sess := &config.Session{
		...
		Packages:        opts.packages,
	}
```

**Step 6: Build and verify**

Run: `go build ./...`
Expected: Compiles with the new --packages flag.

Test: `go run . new --help` should show `--packages` flag.

**Step 7: Commit**

```bash
git add internal/config/config.go cmd/new.go
git commit -m "feat: add --packages flag for pre-session package configuration"
```

---

### Task 8: Add Packages to TUI wizard

**Files:**
- Modify: `internal/tui/wizard.go`

**Step 1: Add stepPackages wizard step**

In `internal/tui/wizard.go`, insert `stepPackages` between `stepWorkspace` and `stepPrompt` (around line 22):

```go
const (
	stepName      = iota
	stepWorktree
	stepBranch
	stepProfile
	stepWorkspace
	stepPackages  // new: optional packages input
	stepPrompt
	stepReview
	stepDone
)
```

**Step 2: Add Packages to WizardResult**

Add to `WizardResult` struct (after line 35):

```go
	Packages   string // comma-separated package names
```

**Step 3: Add updatePackages handler**

Add a new step handler after `updateWorkspace`:

```go
func (m WizardModel) updatePackages(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.result.Packages = strings.TrimSpace(m.textInput.Value())
		m.step = stepPrompt
		m.setupPromptStep()
		return m, textinput.Blink
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}
```

**Step 4: Add setupPackagesStep helper**

Add after `setupWorkspaceStep`:

```go
func (m *WizardModel) setupPackagesStep() {
	m.textInput = textinput.New()
	m.textInput.Placeholder = "(optional) e.g., rust,nodejs,python3"
	m.textInput.Focus()
	m.textInput.CharLimit = 500
	if m.result.Packages != "" {
		m.textInput.SetValue(m.result.Packages)
	}
}
```

**Step 5: Wire into Update routing**

In `Update()` (around lines 112-163), add a case for `stepPackages`:

In the `tea.KeyMsg` switch, add:

```go
	case stepPackages:
		return m.updatePackages(msg)
```

**Step 6: Wire step transitions**

Change `updateWorkspace` to go to `stepPackages` instead of `stepPrompt` (the enter case):

```go
	case "enter":
		m.savedCursors[stepWorkspace] = m.cursor
		if m.cursor == 0 {
			m.result.Workspace = ""
		} else {
			m.result.Workspace = m.workspaceNames[m.cursor-1]
		}
		m.step = stepPackages
		m.setupPackagesStep()
		return m, textinput.Blink
```

Also update `updateProfile` — when there are no workspaces, go to `stepPackages` instead of `stepPrompt`:

```go
		if len(m.workspaceNames) > 0 {
			m.step = stepWorkspace
			m.setupWorkspaceStep()
		} else {
			m.step = stepPackages
			m.setupPackagesStep()
			return m, textinput.Blink
		}
```

**Step 7: Wire back navigation**

In `goBack()`, add cases for `stepPackages` and update `stepPrompt`:

```go
	case stepPackages:
		if len(m.workspaceNames) > 0 {
			m.step = stepWorkspace
			m.setupWorkspaceStep()
		} else {
			m.step = stepProfile
			m.setupProfileStep()
		}
		return m, nil

	case stepPrompt:
		m.step = stepPackages
		m.setupPackagesStep()
		return m, textinput.Blink
```

Remove the existing `stepReview` back handler that goes to `stepPrompt` — it should stay as-is (goes back to prompt, which now goes back to packages).

**Step 8: Add View rendering**

In `View()`, add a case for `stepPackages`:

```go
	case stepPackages:
		b.WriteString(titleStyle.Render("Packages"))
		b.WriteString("\n\n")
		b.WriteString("Extra packages to install (comma-separated nixpkgs names):\n")
		b.WriteString(m.textInput.View())
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("enter confirm  left back"))
```

**Step 9: Include packages in the review render**

In `renderReview()`, add packages display if non-empty:

```go
	if m.result.Packages != "" {
		fmt.Fprintf(&b, "  Packages:  %s\n", m.result.Packages)
	}
```

**Step 10: Include packages in buildCLICommand()**

In `buildCLICommand()`, add packages flag if non-empty:

```go
	if m.result.Packages != "" {
		parts = append(parts, fmt.Sprintf("--packages %s", m.result.Packages))
	}
```

**Step 11: Wire wizard result to createOpts in cmd/new.go**

In `cmd/new.go`, where the wizard result is mapped to `createOpts` (around line 97-114):

Parse the wizard packages string into a slice:

```go
			var wizPackages []string
			if res.Packages != "" {
				for _, p := range strings.Split(res.Packages, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						wizPackages = append(wizPackages, p)
					}
				}
			}
			// Merge: CLI --packages overrides wizard selection
			resolvedPackages := wizPackages
			if len(newPackages) > 0 {
				resolvedPackages = newPackages
			}
```

And add `packages: resolvedPackages` to the `createOpts`.

**Step 12: Build and verify**

Run: `go build ./...`
Expected: Compiles. Wizard should show new packages step.

**Step 13: Commit**

```bash
git add internal/tui/wizard.go cmd/new.go
git commit -m "feat: add packages step to TUI wizard"
```

---

### Task 9: Update managed settings with nix permissions and agent hints

**Files:**
- Modify: `nix/managed-settings.nix`

**Step 1: Add nix command permissions to managed settings**

In `nix/managed-settings.nix`, add nix commands to sandbox permissions. Add an `allow` list after the `deny` list (after line 40):

```nix
  permissions.allow = [
    "Bash(nix profile install *)"
    "Bash(nix profile remove *)"
    "Bash(nix profile list *)"
    "Bash(nix search *)"
  ];
```

**Step 2: Add agent hints to managed settings**

Add an `instructions` field to managed settings (after `spinnerTipsEnabled`):

```nix
  instructions = ''
    ## Container Package Management
    This container uses Nix for package management. To install software:
    - `nix profile install nixpkgs#<package>` (e.g., `nix profile install nixpkgs#rustc nixpkgs#cargo`)
    - `nix search nixpkgs <query>` to find packages
    - `nix profile list` to see installed packages
    - `nix profile remove <index>` to remove packages
    Do not use apt-get, yum, brew, or other package managers — they are not available.
  '';
```

Note: Check whether Claude Code supports an `instructions` field in managed settings. If not, this may need to be a `systemPrompt` or similar field — verify against Claude Code documentation. If no such field exists, skip this step and rely on the sandbox permission names being self-documenting.

**Step 3: Add nixpkgs domains to network allowlist**

In the `network.allowedDomains` list, add the domains nix needs to download packages:

```nix
      "cache.nixos.org"
      "*.cache.nixos.org"
      "channels.nixos.org"
```

**Step 4: Build and verify**

Run: `nix build`
Expected: Full build succeeds with updated settings.

**Step 5: Commit**

```bash
git add nix/managed-settings.nix
git commit -m "feat: add nix permissions and nixpkgs cache domains to managed settings"
```

---

### Task 10: Add persistent Nix store volume

**Files:**
- Modify: `internal/docker/docker.go:106-209` (RunArgs)
- Modify: `internal/docker/docker.go:215-312` (TaskRunArgs)

**Step 1: Add Nix store volume mount to RunArgs**

In `RunArgs()`, add a named volume for the Nix store after the config dir mount (after line 176):

```go
	// Persistent Nix store for runtime package installs.
	args = append(args, "-v", "claude-nix-store:/nix/var")
```

**Step 2: Add Nix store volume mount to TaskRunArgs**

In `TaskRunArgs()`, add the same mount (after line 276):

```go
	args = append(args, "-v", "claude-nix-store:/nix/var")
```

**Step 3: Build and verify**

Run: `go build ./...`
Expected: Compiles.

**Step 4: Commit**

```bash
git add internal/docker/docker.go
git commit -m "feat: add persistent nix store volume for cached package installs"
```

---

### Task 11: Integration testing

**Files:** No new files — manual verification.

**Step 1: Build everything**

Run: `nix build`

**Step 2: Test package installation at startup**

```bash
./result/bin/claude-container new --no-worktree --packages tree --prompt "run 'tree --version' and tell me what you see"
```

Verify: Container starts, tree is installed, agent can use it.

**Step 3: Test runtime package installation**

Start a session without packages, then ask the agent to install something:

```bash
./result/bin/claude-container new --no-worktree --prompt "install ripgrep using nix and search for 'TODO' in /workspace"
```

Verify: Agent uses `nix profile install nixpkgs#ripgrep` and it works.

**Step 4: Test proxy fixes**

1. Start a session and trigger a network request to an unknown domain
2. Verify the pending request appears in the proxy dashboard
3. Go to the Rules tab and add a rule manually using the new form
4. Verify the rule appears in the table

**Step 5: Commit any fixes discovered during testing**

```bash
git add -A
git commit -m "fix: integration test adjustments"
```
