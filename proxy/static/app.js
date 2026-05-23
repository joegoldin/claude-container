/* Claude Proxy Dashboard — WebSocket client and UI logic */

(function () {
  "use strict";

  // --- Auth token (from URL ?token=... or #token=...) ---
  // The proxy generates a token at startup and writes it to a host-only
  // file. The host CLI prints the dashboard URL with ?token=... so the
  // browser can authenticate. The Claude container never sees this token,
  // so it cannot mutate proxy state.
  function getAuthToken() {
    const qs = new URLSearchParams(location.search);
    if (qs.has("token")) return qs.get("token");
    if (location.hash.startsWith("#token=")) {
      return decodeURIComponent(location.hash.slice("#token=".length));
    }
    return null;
  }
  const AUTH_TOKEN = getAuthToken();

  // Wrap fetch to auto-include the auth header on every request.
  const _origFetch = window.fetch.bind(window);
  window.fetch = function (url, opts) {
    opts = opts || {};
    opts.headers = Object.assign({}, opts.headers || {});
    if (AUTH_TOKEN) opts.headers["X-Auth-Token"] = AUTH_TOKEN;
    return _origFetch(url, opts);
  };

  // --- State ---
  let ws = null;
  let pending = [];
  let rules = [];
  let countdownInterval = null;

  // Hold timeout in seconds (should match server default)
  const HOLD_TIMEOUT = 120;

  // Convert server "remaining" seconds to a client-side expiresAt timestamp.
  // This avoids clock-skew between the proxy container and the browser.
  function stampExpiry(item) {
    const remaining = item.remaining != null ? item.remaining : HOLD_TIMEOUT;
    item.expiresAt = Date.now() / 1000 + remaining;
    return item;
  }

  // Duration presets in seconds (0 means forever)
  const DURATIONS = {
    forever: 0,
    "15min": 15 * 60,
    "1hr": 60 * 60,
    "1day": 24 * 60 * 60,
    "1week": 7 * 24 * 60 * 60,
    "1month": 30 * 24 * 60 * 60,
  };

  // --- DOM refs ---
  const pendingList = document.getElementById("pending-list");
  const rulesBody = document.getElementById("rules-body");
  const wsStatus = document.getElementById("ws-status");
  const tabs = document.querySelectorAll(".tab");
  const views = document.querySelectorAll(".view");

  // Add Rule form refs
  const addRuleInput = document.getElementById("add-rule-input");
  const addRulePreset = document.getElementById("add-rule-preset");
  const addRuleDuration = document.getElementById("add-rule-duration");
  const addRuleBtn = document.getElementById("add-rule-btn");
  const typeBtns = document.querySelectorAll(".type-btn");
  let selectedType = "allow";

  // --- Type toggle ---
  typeBtns.forEach((btn) => {
    btn.addEventListener("click", () => {
      typeBtns.forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      selectedType = btn.getAttribute("data-type");
    });
  });

  // --- Preset change handler ---
  addRulePreset.addEventListener("change", () => {
    const preset = addRulePreset.value;
    if (preset === "subdomain") {
      addRuleInput.placeholder = "Enter hostname (e.g. api.example.com)";
    } else if (preset === "base_domain") {
      addRuleInput.placeholder = "Enter domain (e.g. example.com)";
    } else {
      addRuleInput.placeholder = "Enter regex pattern...";
    }
  });

  // --- Add rule submit ---
  async function submitAddRule() {
    const rawValue = addRuleInput.value.trim();
    if (!rawValue) {
      addRuleInput.focus();
      return;
    }

    const preset = addRulePreset.value;
    let pattern;
    if (preset === "subdomain") {
      pattern = patternSubdomain(rawValue);
    } else if (preset === "base_domain") {
      pattern = patternBaseDomain(rawValue);
    } else {
      pattern = rawValue;
    }

    const durationKey = addRuleDuration.value;
    const durationSec = DURATIONS[durationKey] || 0;
    const expiresAt =
      durationSec > 0 ? Math.floor(Date.now() / 1000) + durationSec : null;

    const label =
      (selectedType === "allow" ? "Allow " : "Deny ") + rawValue;

    try {
      const resp = await fetch("/api/rules", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          type: selectedType,
          pattern: pattern,
          label: label,
          expires_at: expiresAt,
          source: "manual",
        }),
      });
      if (resp.ok) {
        addRuleInput.value = "";
      }
    } catch (err) {
      console.error("Failed to add rule:", err);
    }
  }

  addRuleBtn.addEventListener("click", submitAddRule);

  // --- Export / Import preset ---
  // Export downloads the live rules as a JSON file. Import POSTs an
  // uploaded JSON file to /api/rules/import which REPLACES the rule
  // store. We confirm before replacing because there's no undo.
  const exportBtn = document.getElementById("export-rules-btn");
  const importBtn = document.getElementById("import-rules-btn");
  const importFile = document.getElementById("import-rules-file");

  if (exportBtn) {
    exportBtn.addEventListener("click", async () => {
      try {
        const resp = await fetch("/api/rules");
        if (!resp.ok) throw new Error("export failed: " + resp.status);
        const data = await resp.json();
        const blob = new Blob([JSON.stringify(data, null, 2)], {
          type: "application/json",
        });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        const ts = new Date().toISOString().replace(/[:.]/g, "-");
        a.href = url;
        a.download = `claude-proxy-rules-${ts}.json`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } catch (err) {
        alert("Export failed: " + err.message);
      }
    });
  }

  if (importBtn && importFile) {
    importBtn.addEventListener("click", () => importFile.click());
    importFile.addEventListener("change", async () => {
      const file = importFile.files && importFile.files[0];
      if (!file) return;
      if (
        !confirm(
          "Replace ALL current rules with the contents of " +
            file.name +
            "? This cannot be undone."
        )
      ) {
        importFile.value = "";
        return;
      }
      try {
        const text = await file.text();
        let parsed;
        try {
          parsed = JSON.parse(text);
        } catch (err) {
          throw new Error("not valid JSON: " + err.message);
        }
        const resp = await fetch("/api/rules/import", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(parsed),
        });
        if (!resp.ok) {
          const body = await resp.text();
          throw new Error("server rejected: " + body);
        }
      } catch (err) {
        alert("Import failed: " + err.message);
      } finally {
        importFile.value = "";
      }
    });
  }

  addRuleInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      submitAddRule();
    }
  });

  // --- Tab navigation ---
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      tabs.forEach((t) => t.classList.remove("active"));
      views.forEach((v) => v.classList.remove("active"));
      tab.classList.add("active");
      const target = tab.getAttribute("data-tab");
      document.getElementById(target + "-view").classList.add("active");
      if (target === "published") refreshPublished();
      if (target === "userallow") {
        renderUserAllowFields();
        refreshUserAllow();
      }
    });
  });

  // --- Pattern helpers ---
  function escapeRegex(str) {
    return str.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  function parseUrl(url) {
    try {
      return new URL(url);
    } catch {
      return null;
    }
  }

  function patternExactUrl(url) {
    return "^" + escapeRegex(url) + "$";
  }

  function patternUrlNoParams(url) {
    const parsed = parseUrl(url);
    if (!parsed) return escapeRegex(url);
    const bare = parsed.origin + parsed.pathname;
    return "^" + escapeRegex(bare) + "(\\?.*)?$";
  }

  function patternSubdomain(host) {
    return "^https?://" + escapeRegex(host) + "(/.*)?$";
  }

  function patternBaseDomain(host) {
    // Extract base domain (last two parts)
    const parts = host.split(".");
    const base = parts.length > 2 ? parts.slice(-2).join(".") : host;
    return "^https?://([^/]*\\.)?" + escapeRegex(base) + "(/.*)?$";
  }

  function getPatternPresets(url, host) {
    return [
      { id: "exact", label: "Exact URL", value: patternExactUrl(url) },
      {
        id: "no_params",
        label: "URL (any params)",
        value: patternUrlNoParams(url),
      },
      {
        id: "subdomain",
        label: "Host: " + host,
        value: patternSubdomain(host),
      },
      {
        id: "base_domain",
        label:
          "Domain: " +
          (host.split(".").length > 2
            ? host.split(".").slice(-2).join(".")
            : host),
        value: patternBaseDomain(host),
      },
      { id: "custom", label: "Custom regex", value: "" },
    ];
  }

  // Pattern presets for raw TCP flows (e.g. SSH, custom protocols).
  // Synthetic URL form is `tcp://host:port`. The rule store matches the
  // same regex engine as HTTP rules, so the patterns just need to anchor
  // against that URL form.
  function getTcpPatternPresets(host, port) {
    const hostPort = host + ":" + port;
    return [
      {
        id: "exact",
        label: "Exact: " + hostPort,
        value: "^tcp://" + escapeRegex(hostPort) + "$",
      },
      {
        id: "host_any_port",
        label: "Host (any port): " + host,
        value: "^tcp://" + escapeRegex(host) + ":\\d+$",
      },
      {
        id: "port_any_host",
        label: "Port " + port + " (any host)",
        value: "^tcp://[^:]+:" + port + "$",
      },
      { id: "custom", label: "Custom regex", value: "" },
    ];
  }

  // --- Render pending requests ---
  function renderPending() {
    if (pending.length === 0) {
      pendingList.innerHTML =
        '<p class="empty-state">No pending requests.</p>';
      return;
    }

    pendingList.innerHTML = "";
    const now = Date.now() / 1000;

    pending.forEach((item) => {
      const remaining = Math.max(
        0,
        Math.ceil(item.expiresAt - now)
      );
      const isTcp = item.kind === "tcp";
      const presets = isTcp
        ? getTcpPatternPresets(item.host, item.port)
        : getPatternPresets(item.url, item.host);
      const headerHost = isTcp
        ? item.host + ":" + item.port + " (raw TCP)"
        : item.host;

      const card = document.createElement("div");
      card.className = "pending-card" + (isTcp ? " pending-card-tcp" : "");
      card.setAttribute("data-flow-id", item.flow_id);

      card.innerHTML = `
        <div class="card-header">
          <span class="host">${htmlEscape(headerHost)}</span>
          <span class="countdown ${remaining < 30 ? "urgent" : ""}" data-expires="${item.expiresAt}">${remaining}s</span>
        </div>
        <div class="url">${htmlEscape(item.url)}</div>
        ${item.dns_name ? `<div class="pending-dns">DNS query for <strong>${htmlEscape(item.dns_name)}</strong></div>` : ""}
        <div class="options">
          <div class="option-group">
            <label>Pattern</label>
            <div class="pattern-options">
              ${presets
                .map(
                  (p, i) => `
                <label class="pattern-option ${i === 0 ? "selected" : ""}">
                  <input type="radio" name="pattern-${item.flow_id}" value="${htmlAttrEscape(p.value)}"
                    data-preset-id="${p.id}" ${i === 0 ? "checked" : ""}>
                  ${htmlEscape(p.label)}
                </label>
              `
                )
                .join("")}
            </div>
            <input type="text" class="custom-pattern-input" placeholder="Enter custom regex..."
              data-flow-id="${item.flow_id}">
          </div>
          <div class="option-group">
            <label>Duration</label>
            <select data-flow-id="${item.flow_id}">
              <option value="forever">Forever</option>
              <option value="15min">15 minutes</option>
              <option value="1hr">1 hour</option>
              <option value="1day" selected>1 day</option>
              <option value="1week">1 week</option>
              <option value="1month">1 month</option>
            </select>
          </div>
        </div>
        <div class="card-actions">
          <button class="btn btn-allow" data-flow-id="${item.flow_id}" data-action="allow">Allow</button>
          <button class="btn btn-deny" data-flow-id="${item.flow_id}" data-action="deny">Deny</button>
        </div>
      `;

      pendingList.appendChild(card);

      // Wire up pattern radio buttons
      const radios = card.querySelectorAll(`input[name="pattern-${item.flow_id}"]`);
      const customInput = card.querySelector(`.custom-pattern-input[data-flow-id="${item.flow_id}"]`);
      const patternLabels = card.querySelectorAll(".pattern-option");

      radios.forEach((radio, idx) => {
        radio.addEventListener("change", () => {
          patternLabels.forEach((l) => l.classList.remove("selected"));
          patternLabels[idx].classList.add("selected");
          if (radio.getAttribute("data-preset-id") === "custom") {
            customInput.classList.add("visible");
            customInput.focus();
          } else {
            customInput.classList.remove("visible");
          }
        });
      });

      // Wire up action buttons
      card.querySelectorAll(".btn-allow, .btn-deny").forEach((btn) => {
        btn.addEventListener("click", () => {
          const flowId = btn.getAttribute("data-flow-id");
          const action = btn.getAttribute("data-action");
          resolveFlow(flowId, action, card);
        });
      });
    });
  }

  function resolveFlow(flowId, action, card) {
    // Find selected pattern
    const selectedRadio = card.querySelector(
      `input[name="pattern-${flowId}"]:checked`
    );
    let pattern = selectedRadio ? selectedRadio.value : "";

    if (
      selectedRadio &&
      selectedRadio.getAttribute("data-preset-id") === "custom"
    ) {
      const customInput = card.querySelector(
        `.custom-pattern-input[data-flow-id="${flowId}"]`
      );
      pattern = customInput ? customInput.value : "";
    }

    if (!pattern) {
      alert("Please enter a pattern.");
      return;
    }

    // Find selected duration
    const durationSelect = card.querySelector(
      `select[data-flow-id="${flowId}"]`
    );
    const durationKey = durationSelect ? durationSelect.value : "1day";
    const durationSec = DURATIONS[durationKey] || 0;
    const expiresAt =
      durationSec > 0 ? Math.floor(Date.now() / 1000) + durationSec : null;

    // Derive a label from the host
    const hostEl = card.querySelector(".host");
    const host = hostEl ? hostEl.textContent : "";
    const label = action === "allow" ? "Allow " + host : "Deny " + host;

    // Send via WebSocket
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(
        JSON.stringify({
          type: "resolve",
          data: {
            flow_id: flowId,
            action: action,
            pattern: pattern,
            label: label,
            expires_at: expiresAt,
          },
        })
      );
    }
  }

  // --- Render rules table ---
  function renderRules() {
    if (rules.length === 0) {
      rulesBody.innerHTML =
        '<tr class="empty-row"><td colspan="6">No rules configured.</td></tr>';
      return;
    }

    rulesBody.innerHTML = "";
    rules.forEach((rule) => {
      const tr = document.createElement("tr");
      const expiresStr = rule.expires_at
        ? new Date(rule.expires_at * 1000).toLocaleString()
        : "Never";

      tr.innerHTML = `
        <td><span class="rule-type ${rule.rule_type}">${htmlEscape(rule.rule_type)}</span></td>
        <td class="rule-pattern" title="${htmlAttrEscape(rule.pattern)}">${htmlEscape(rule.pattern)}</td>
        <td>${htmlEscape(rule.label || "")}</td>
        <td>${expiresStr}</td>
        <td>${htmlEscape(rule.source || "interactive")}</td>
        <td><button class="btn-delete" data-rule-id="${rule.id}">Delete</button></td>
      `;

      tr.querySelector(".btn-delete").addEventListener("click", async () => {
        try {
          const resp = await fetch("/api/rules/" + rule.id, {
            method: "DELETE",
          });
          if (resp.ok) {
            rules = rules.filter((r) => r.id !== rule.id);
            renderRules();
          }
        } catch (err) {
          console.error("Failed to delete rule:", err);
        }
      });

      rulesBody.appendChild(tr);
    });
  }

  // --- Countdown timer ---
  function startCountdown() {
    if (countdownInterval) clearInterval(countdownInterval);
    countdownInterval = setInterval(() => {
      const now = Date.now() / 1000;
      document.querySelectorAll(".countdown").forEach((el) => {
        const expiresAt = parseFloat(el.getAttribute("data-expires"));
        const remaining = Math.max(0, Math.ceil(expiresAt - now));
        el.textContent = remaining + "s";
        if (remaining < 30) {
          el.classList.add("urgent");
        } else {
          el.classList.remove("urgent");
        }
      });
    }, 1000);
  }

  // --- Notifications ---
  function requestNotificationPermission() {
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission();
    }
  }

  function showNotification(title, body) {
    if ("Notification" in window && Notification.permission === "granted") {
      new Notification(title, { body: body, icon: null });
    }
  }

  // --- HTML escaping ---
  function htmlEscape(str) {
    const div = document.createElement("div");
    div.textContent = str;
    return div.innerHTML;
  }

  function htmlAttrEscape(str) {
    return str
      .replace(/&/g, "&amp;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  // --- WebSocket ---
  function connectWebSocket() {
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const wsTokenSuffix = AUTH_TOKEN
      ? "?token=" + encodeURIComponent(AUTH_TOKEN)
      : "";
    ws = new WebSocket(protocol + "//" + location.host + "/ws" + wsTokenSuffix);

    ws.addEventListener("open", () => {
      wsStatus.textContent = "Connected";
      wsStatus.className = "connected";
    });

    ws.addEventListener("close", () => {
      wsStatus.textContent = "Disconnected - reconnecting...";
      wsStatus.className = "disconnected";
      setTimeout(connectWebSocket, 2000);
    });

    ws.addEventListener("error", () => {
      wsStatus.textContent = "Connection error";
      wsStatus.className = "disconnected";
    });

    ws.addEventListener("message", (event) => {
      let msg;
      try {
        msg = JSON.parse(event.data);
      } catch {
        return;
      }

      switch (msg.type) {
        case "init":
          pending = (msg.data.pending || []).map(stampExpiry);
          rules = msg.data.rules || [];
          renderPending();
          renderRules();
          break;

        case "pending":
          // New pending request
          pending.push(stampExpiry(msg.data));
          renderPending();
          showNotification(
            "Pending Request",
            msg.data.host + " - " + msg.data.url
          );
          // Switch to pending tab
          tabs.forEach((t) => t.classList.remove("active"));
          views.forEach((v) => v.classList.remove("active"));
          document.querySelector('[data-tab="pending"]').classList.add("active");
          document.getElementById("pending-view").classList.add("active");
          break;

        case "resolved":
          // Remove from pending list
          pending = pending.filter(
            (p) => p.flow_id !== msg.data.flow_id
          );
          renderPending();
          break;

        case "rules_changed":
          rules = msg.data || [];
          renderRules();
          break;
      }
    });
  }

  // --- Published Ports ---
  async function refreshPublished() {
    const r = await fetch("/api/published-ports");
    const items = await r.json();
    const tbody = document.querySelector("#published-table tbody");
    tbody.innerHTML = "";
    if (!items || items.length === 0) {
      tbody.innerHTML = '<tr class="empty-row"><td colspan="5">No published ports.</td></tr>';
      return;
    }
    for (const it of items) {
      const tr = document.createElement("tr");
      tr.innerHTML =
        '<td>' + it.protocol + '</td>' +
        '<td><a href="http://127.0.0.1:' + it.host_port + '" target="_blank">' + it.host_port + '</a></td>' +
        '<td>' + it.container_port + '</td>' +
        '<td>' + (it.label || '') + '</td>' +
        '<td><button class="btn btn-secondary unpublish-btn" data-port="' + it.host_port + '" data-proto="' + it.protocol + '">Unpublish</button></td>';
      tbody.appendChild(tr);
    }
  }

  const publishForm = document.querySelector("#publish-form");
  if (publishForm) {
    publishForm.addEventListener("submit", async (e) => {
      e.preventDefault();
      const f = new FormData(e.target);
      const body = {
        protocol: f.get("protocol"),
        container_port: parseInt(f.get("container_port"), 10),
        label: f.get("label") || "",
      };
      const hp = f.get("host_port");
      if (hp) body.host_port = parseInt(hp, 10);
      const r = await fetch("/api/publish", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        const err = await r.json().catch(() => ({ error: "unknown" }));
        alert("Publish failed: " + (err.error || r.status));
        return;
      }
      e.target.reset();
      refreshPublished();
    });
  }

  const publishedTableBody = document.querySelector("#published-table tbody");
  if (publishedTableBody) {
    publishedTableBody.addEventListener("click", async (e) => {
      if (!e.target.classList.contains("unpublish-btn")) return;
      const port = parseInt(e.target.dataset.port, 10);
      const proto = e.target.dataset.proto;
      await fetch("/api/unpublish", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ protocol: proto, host_port: port }),
      });
      refreshPublished();
    });
  }

  // --- Custom Firewall (user_allow) ---
  let userAllowTemplates = null;

  async function loadUserAllowTemplates() {
    if (userAllowTemplates) return userAllowTemplates;
    const r = await fetch("/api/user-allow/templates");
    userAllowTemplates = await r.json();
    return userAllowTemplates;
  }

  async function refreshUserAllow() {
    const r = await fetch("/api/user-allow");
    const items = await r.json();
    const tbody = document.querySelector("#userallow-table tbody");
    tbody.innerHTML = "";
    if (!Array.isArray(items) || items.length === 0) {
      tbody.innerHTML =
        '<tr class="empty-row"><td colspan="3">No custom firewall rules.</td></tr>';
      return;
    }
    for (const it of items) {
      const tr = document.createElement("tr");
      tr.innerHTML =
        "<td>" + htmlEscape(it.label || "") + "</td>" +
        "<td><code>" + htmlEscape(it.stmt) + "</code></td>" +
        '<td><button class="btn btn-secondary userallow-del-btn"' +
        ' data-id="' + htmlAttrEscape(it.id) + '">Delete</button></td>';
      tbody.appendChild(tr);
    }
  }

  async function renderUserAllowFields() {
    const templates = await loadUserAllowTemplates();
    const select = document.querySelector("#userallow-template");
    select.innerHTML = "";
    for (const name of Object.keys(templates)) {
      const opt = document.createElement("option");
      opt.value = name;
      opt.textContent = name.replace(/_/g, " ");
      select.appendChild(opt);
    }
    renderTemplateInputs();
  }

  function renderTemplateInputs() {
    const name = document.querySelector("#userallow-template").value;
    if (!name || !userAllowTemplates) return;
    const fields = userAllowTemplates[name] || [];
    const container = document.querySelector("#userallow-fields");
    container.innerHTML = "";
    for (const f of fields) {
      const div = document.createElement("div");
      div.className = "form-group";
      div.innerHTML =
        "<label>" + htmlEscape(f) + "</label>" +
        '<input name="' + htmlAttrEscape(f) + '" type="text" required>';
      container.appendChild(div);
    }
  }

  document.querySelector("#userallow-mode").addEventListener("change", (e) => {
    const isRaw = e.target.value === "raw";
    document.querySelector("#userallow-template-group").hidden = isRaw;
    document.querySelector("#userallow-fields").hidden = isRaw;
    document.querySelector("#userallow-raw-group").hidden = !isRaw;
  });

  document.querySelector("#userallow-template").addEventListener("change", renderTemplateInputs);

  document.querySelector("#userallow-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const mode = document.querySelector("#userallow-mode").value;
    const label = e.target.querySelector('[name="label"]').value;
    let body;
    if (mode === "raw") {
      const stmt = e.target.querySelector('[name="stmt"]').value;
      body = { stmt: stmt, label: label };
    } else {
      const template = document.querySelector("#userallow-template").value;
      const params = {};
      for (const inp of document.querySelectorAll("#userallow-fields input")) {
        params[inp.name] = inp.value;
      }
      body = { template: template, params: params, label: label };
    }
    const r = await fetch("/api/user-allow", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      const err = await r.json().catch(() => ({ error: "unknown" }));
      alert("Rule rejected: " + (err.error || r.status));
      return;
    }
    e.target.reset();
    document.querySelector("#userallow-mode").dispatchEvent(new Event("change"));
    refreshUserAllow();
  });

  document.querySelector("#userallow-table tbody").addEventListener("click", async (e) => {
    if (!e.target.classList.contains("userallow-del-btn")) return;
    const id = e.target.dataset.id;
    if (!confirm("Delete this firewall rule?")) return;
    await fetch("/api/user-allow/" + encodeURIComponent(id), { method: "DELETE" });
    refreshUserAllow();
  });

  // --- Init ---
  requestNotificationPermission();
  connectWebSocket();
  startCountdown();
})();
