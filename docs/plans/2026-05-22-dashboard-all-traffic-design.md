# Dashboard — All-Traffic Control (Design)

## 1. Summary

Extend the per-session proxy's dashboard so it is the single live source
of truth for **every traffic decision the proxy makes**, in both
directions, regardless of protocol. Today the dashboard manages HTTP /
HTTPS and raw TCP outbound rules. After this work it also manages:

- inbound port publishing (TCP and UDP), dynamic per-session
- outbound UDP (with held-by-default semantics, like HTTPS today)
- inbound and outbound ICMP and other no-port protocols
- arbitrary nft `accept` rules a user wants to add as an escape hatch

The dashboard becomes the authoritative live config; mitmproxy, the new
UDP redirector, the publish manager, and the nft user sub-chain all read
from one unified rule store.

## 2. Goals

- One pane of glass for every active traffic rule.
- Adding or removing a port / domain / protocol takes effect immediately,
  no container restart, no `claude-container run` flag.
- Multiple sessions run concurrently without port collisions and without
  losing their publish allocations across restarts.
- New protocols (UDP, ICMP) get the same held-by-default-with-approval
  semantics that HTTPS has today.
- Security envelope is unchanged: a confused/compromised Claude cannot
  weaken the firewall, exfiltrate outside the rule store, or talk to
  hosts outside its own per-session netns.

## 3. Non-goals

- Cross-session control: each session keeps its own dashboard at its own
  port. No global multi-session controller.
- Raw `nft` syntax in the user-facing UI by default. Power users get a
  "Raw mode" textarea, but the primary interface is structured templates.
- L7 filtering for non-HTTP protocols (e.g., inspecting MQTT payloads).
- Bandwidth shaping / QoS (deferred; could become Phase 5 if needed).

## 4. Threat-model interaction

The whole design lives behind the security boundaries already audited
(`docs/security/audit-2026-05-22.md`,
`docs/security/audit-2026-05-22-adversarial.md`):

- Dashboard mutating endpoints require `X-Auth-Token`. The Claude
  container has no path to that token.
- `user_allow` sub-chain (Phase 3) can only contain `accept` rules. It
  evaluates AFTER the security drops, so it cannot subvert them.
- Inbound publish (Phase 1) is firewall-default-deny — docker `-p`
  forwards arrive at the netns but the nft INPUT chain drops unassigned
  ports.
- UDP outbound (Phase 2) defaults to hold-then-approve. No UDP escapes
  the redirector unless explicitly allowed in the rule store.
- The user can write a Phase 3 rule that allows `udp daddr 0/0 accept`,
  exfiltrating all UDP. That's intentional — the user is the trust
  anchor for their own escape-hatch rules.

## 5. Architecture

```
┌──────────────────────────────────────────────────────────────┐
│ Dashboard (Starlette/Uvicorn @ 127.0.0.1:<dash>/api/...)     │
│ ┌─────────┬───────┬─────────────────┬─────────────────────┐  │
│ │ Pending │ Rules │ Live Config NEW │ Custom Firewall NEW │  │
│ └─────────┴───────┴─────────────────┴─────────────────────┘  │
│         │       │           │                  │              │
│         ▼       ▼           ▼                  ▼              │
│  unified RuleStore  (direction, proto, match, action, label) │
│         │       │           │                  │              │
└─────────┼───────┼───────────┼──────────────────┼──────────────┘
          ▼       ▼           ▼                  ▼
    ┌──────────┐  ┌──────────┐  ┌──────────────┐  ┌──────────────┐
    │ mitmproxy│  │ udp-redir│  │ publish-mgr  │  │ nft user     │
    │ HTTP+TCP │  │  (NEW)   │  │  TCP + UDP   │  │ sub-chain    │
    │  addon   │  │  TPROXY  │  │  (NEW)       │  │ user_allow   │
    └──────────┘  └──────────┘  └──────────────┘  └──────────────┘
              all run in the proxy container's netns
              all owned by uid 1500 (mitmproxy user)
```

Three new components inside the proxy container:

### 5.1 `udp-redir` daemon

Small Go binary, ~300-500 lines. Listens on a UDP port with
`IP_TRANSPARENT`, reads original `(dst, dport)` from ancillary data,
consults the rule store, holds / forwards / drops. Replaces mitmproxy's
role for UDP. See Phase 2.

### 5.2 `publish-mgr` daemon

Small Go binary, ~200 lines. Owns nft INPUT rules for the
pre-allocated host-port range. Listens on a Unix socket inside the proxy
container; the dashboard POSTs publish/unpublish actions. See Phase 1.

### 5.3 `user_allow` sub-chain

A dedicated nft chain (not a daemon — just a chain). Evaluated after the
security accept/drop rules. The publish-mgr also writes structured
ICMP / cross-protocol user rules here. See Phase 3.

## 6. Unified rule schema

Current shape (`proxy/claude_proxy/rules.py`):
```json
{"id": "...", "type": "http_allow", "pattern": "github.com", "label": "..."}
```

New shape:
```json
{
  "id":     "uuid",
  "direction": "out" | "in",
  "proto":  "http" | "tcp" | "udp" | "icmp" | "any",
  "match": {
    "host":       "github.com" | null,
    "host_regex": null,
    "port":       443 | "3000-3005" | null,
    "method":     "GET" | null,
    "path_regex": null,
    "dns_name":   "example.com" | null,
    "cidr":       "10.0.0.0/8" | null,
    "nft_statement": null  /* raw mode; pure phase-3 escape hatch */
  },
  "action":  "allow" | "deny" | "hold",
  "label":   "vite dev server",
  "source":  "interactive" | "preset" | "profile" | "import",
  "expires_at": null
}
```

Key choices:

- `hold` is a first-class action — explicit semantics for UDP and ICMP,
  not just an implicit fall-through.
- `match` is an object with all-optional fields, evaluated as AND. Regex
  fields are available where structured matching isn't enough.
- `proto: "any"` matches at the nft layer regardless of protocol; used
  by user-sub-chain rules.

### 6.1 Migration of existing rules

Old rules are accepted on POST and normalized at load:

| old `type`     | new `direction` | `proto` | `action` |
|----------------|-----------------|---------|----------|
| `http_allow`   | `out`           | `http`  | `allow`  |
| `http_deny`    | `out`           | `http`  | `deny`   |
| `tcp_allow`    | `out`           | `tcp`   | `allow`  |
| `tcp_deny`     | `out`           | `tcp`   | `deny`   |

Old `pattern` becomes `match.host_regex` (HTTP) or `match.host_regex`
+ port extraction (TCP). Both old and new shapes are accepted on POST
for one minor version; storage is canonical new-shape.

## 7. Phase 1 — inbound publish (TCP + UDP, dynamic)

### 7.1 Host-side port-range allocation

To support concurrent sessions, each session claims its own contiguous
port range from a host-side pool. Default 10 ports per session, pool
starts at 30000.

Allocation state lives at
`<configDir>/published-port-allocations.json`:
```json
{
  "myproject-calm-reef":  {"base": 30000, "size": 10},
  "myproject-eager-fox":  {"base": 30010, "size": 10}
}
```

`session.Launch` claims a range BEFORE the proxy container starts:

1. Open the file under an advisory lock (small, contention is fine).
2. Scan from `--publish-base` for the first contiguous free block of
   `--publish-range` size.
3. Write the new entry, release lock.
4. Pass the range to `httpproxy.ProxyOpts.PublishRange`.
5. The proxy's docker run gains two `-p` lines covering the full range:
   ```
   -p 127.0.0.1:30000-30009:30000-30009/tcp
   -p 127.0.0.1:30000-30009:30000-30009/udp
   ```
6. `PROXY_PUBLISH_RANGE=30000-30009` env tells the publish-mgr its
   bounds.

Released when the session is removed (`docker rm` + the existing cleanup
closure in `internal/session/launch.go`). Stale entries cleaned by `gc`.

### 7.2 New CLI flags and env vars

```
--publish-range N      (default 10)     ports per session
--publish-base PORT    (default 30000)  first port the pool can use
CLAUDE_CONTAINER_PUBLISH_RANGE / _BASE  env overrides
```

Capacity with defaults: 100 concurrent sessions before the pool is
exhausted at 30999. Exhaustion is a fail-fast error pointing at the
override flags.

### 7.3 publish-mgr daemon

Lives in the proxy container. Communication is a Unix socket at
`/run/publish-mgr.sock` (uid 1500 owned, mode 0600). The path lives on
the container's writable layer, NOT in the `/config` bind-mount, so
the socket file is never visible from the host filesystem.

API:
```
POST /publish      {protocol, container_port[, host_port], label}
                   -> {host_port, ok: true}
POST /unpublish    {host_port}
                   -> {ok: true}
GET  /list         -> [{host_port, container_port, protocol, label, listening: bool}]
```

The dashboard's `/api/publish` endpoint proxies to this socket.

On publish:
1. Pick the next free port in the range (or honor an explicit
   `host_port` if it's in range and free).
2. Add nft rule: `tcp dport <container_port> accept` or
   `udp dport <container_port> accept`, in the INPUT chain.
3. If `host_port != container_port`, add NAT rule mapping host to
   container port.
4. Append rule-store entry `{direction: "in", proto: <tcp|udp>,
   match: {port: container_port}, action: "allow", label}`.
5. Return host URL e.g. `http://127.0.0.1:30005`.

On unpublish: reverse all three (nft rule removed, rule-store entry
deleted, port marked free in publish-mgr's in-memory pool).

### 7.4 Dashboard UI

New tab — **Live Config** — with a "Published Ports" section:

```
Publish Inbound Port

  Protocol:   ( • TCP   ○ UDP   ○ Both )
  Container port:  [____]
  Host port:       [auto ▼]   (must be in 30000-30009)
  Label:           [__________________]

  [ Publish ]
```

Below: table of active publishes. Each row has Unpublish + Copy URL
buttons. Live indicator showing whether a process is actually listening
on the container side (poll `ss -ln` once per second, cheap).

### 7.5 Dev-server gotcha (documentation)

Many tools default-bind to `127.0.0.1` inside the container. Docker's
port-forward sends to eth0 (not lo), so a `127.0.0.1`-bound server is
unreachable. Publish form tooltip: "your server must listen on
`0.0.0.0:PORT`". If the live indicator shows no listener after 30s, the
row gets a warning with that hint.

### 7.6 Persistence across container restart

The published-port-allocations.json survives. On `claude-container
attach`, session.Launch re-claims the same range. The publish-mgr starts
fresh and re-reads its rules from the rule store, re-applying nft rules.

## 8. Phase 2 — UDP outbound

### 8.1 Mechanism: NFQUEUE, not TPROXY

mitmproxy's TCP setup uses `redirect to :8080` in nat OUTPUT. REDIRECT
overwrites the destination address but TCP doesn't care because
mitmproxy reads `SO_ORIGINAL_DST` from the connect socket. For UDP,
REDIRECT erases the original destination before the daemon can read it,
and unlike TCP there's no SO_ORIGINAL_DST equivalent for connectionless
sockets.

TPROXY is the textbook fix — but TPROXY is a PREROUTING-chain target
intended for FORWARDED traffic. Our UDP is locally-generated (Claude
container sources it inside the same netns), so we need to intercept it
in OUTPUT. nftables doesn't expose TPROXY as an OUTPUT target.

Two options actually work for OUTPUT-chain UDP interception on Linux:

1. **NFQUEUE** — nft rule queues matching packets to userspace via
   netlink. The daemon reads packets via `libnetfilter_queue`, makes a
   verdict (ACCEPT / DROP), and optionally reinjects modified packets.
   Standard, well-supported, no routing tricks.
2. **Mark + `ip rule` + local-route + `IP_TRANSPARENT`** — mark packets
   in nft OUTPUT, add `ip rule fwmark X lookup 100` and
   `ip route add local default dev lo table 100` so marked packets get
   routed to the local loopback where the daemon listens with
   `IP_TRANSPARENT`. More complex to set up; equivalent capability.

We pick **NFQUEUE** — simpler, no extra routing config, no kernel
fwmark dance. Trade-off: it requires the daemon to use
`libnetfilter_queue` instead of plain `recvmsg`. The Go binding is
mature (`github.com/florianl/go-nfqueue`).

### 8.2 udp-redir daemon

Go binary, runs as uid 1500 inside the proxy container. The same nix
derivation that builds the proxy image builds this binary alongside.

Listens on netfilter queue 0. For each enqueued datagram:

1. Parse the packet — IP + UDP headers + payload — to extract
   `(src, sport, dst, dport)` and the UDP payload.
2. If `dst_port == 53` AND `dst != 127.0.0.11`, parse the DNS query
   name from the payload (just the first question, RFC 1035
   wire-format, no recursion). Set `dns_name` for rule lookup.
3. Consult `RuleStore.match()` with synthetic URL
   `udp://dst_host:dst_port` (or `udp://dns_name?type=A` for DNS
   queries).
4. Verdict:
   - `allow` → reinject with `NF_ACCEPT`. Future return packets from
     that server are accepted by `ct state established,related accept`
     in nftables.
   - `deny` → reinject with `NF_DROP`.
   - `hold` → reinject with `NF_DROP` and buffer up to 16 datagrams per
     `(host, port)` tuple with a 30-second TTL; emit `pending` flow to
     the dashboard. While held, subsequent datagrams to the same tuple
     are also held. On user resolution, the daemon either crafts and
     emits the buffered datagrams (allow) or simply discards them
     (deny). Once a tuple has a stored rule, the daemon makes the
     verdict in-line without buffering.

### 8.3 nftables additions

```
chain output {
  ...existing rules...
  ; new — enqueues UDP from non-mitmproxy uids to userspace daemon:
  meta l4proto udp meta skuid != 1500 queue num 0 bypass
  meta l4proto udp drop  /* fallback if daemon is not running */
}
```

The `bypass` flag means: if the userspace daemon isn't running, packets
are not dropped at the queue stage — instead they fall through to the
next rule, which is the explicit drop. So if the daemon crashes, UDP
fails closed.

The existing `udp dport 443 drop` and `udp dport 53 log ... drop`
rules fire BEFORE the queue, so the daemon never sees QUIC or external
UDP/53 — those remain dropped at the firewall layer for defense in
depth. UDP/53 to `127.0.0.11` (docker embedded resolver) is accepted by
`oif "lo" accept` and never reaches the queue, so DNS via libc keeps
working.

### 8.4 DNS-aware UX

When the daemon sees a UDP/53 query, it parses the DNS question (no
recursion / no resolution — just RFC 1035 wire-format parsing) and
emits the pending flow with a human-readable label:

```
pending: DNS query for example.com via 1.1.1.1
   [ Allow once ]  [ Allow example.com via this resolver ]  [ Deny ]
```

The rule store gains a `match.dns_name` field that the daemon checks
on subsequent queries.

### 8.5 Inbound UDP

Handled by Phase 1 (publish-mgr). When the dashboard publishes UDP port
5060, the publish-mgr adds `udp dport 5060 accept` to INPUT. Return
traffic is allowed by `ct state established,related accept` (which
covers UDP "pseudo-connections" via the conntrack helper).

## 9. Phase 3 — nft user sub-chain (escape hatch)

### 9.1 Chain placement

Add `chain user_allow` to the existing `inet claude_proxy_fw` table.
Modify the base chains to `jump user_allow` immediately before the
final `log ... drop`:

```
chain output {
  oif "lo" accept
  ct state established,related accept
  meta skuid 1500 accept
  tcp dport 8080 accept
  tcp dport 8081 accept
  tcp dport 53 accept
  udp dport 443 drop
  udp dport 53 log prefix "claude_proxy_fw dns-udp drop: " level debug drop
  jump user_allow
  log prefix "claude_proxy_fw drop: " level debug
}
```

`user_allow` contains only `accept` statements. Cannot contain `drop` /
`reject` / `policy` / `flush` — any rule that would weaken the
security boundary is rejected at validation time.

### 9.2 Structured rule templates

Dashboard's "Custom Firewall" tab exposes a form:

```
[ Allow ▾ ]  [ inbound ▾ ]  [ ICMP echo ▾ ]
from [ 192.168.1.0/24 ]  (host or CIDR)
                                                  [ + Add Rule ]
```

Common templates:

| Template name           | Compiles to nft                                                                 |
|-------------------------|---------------------------------------------------------------------------------|
| outbound ICMP echo      | `ip daddr <addr> icmp type echo-request accept`                                 |
| inbound ICMP echo       | `ip saddr <addr> icmp type echo-reply accept`                                   |
| outbound TCP CIDR       | `ip daddr <cidr> tcp dport <port> accept`                                       |
| inbound TCP CIDR        | `ip saddr <cidr> tcp dport <port> accept`                                       |
| outbound UDP CIDR       | `ip daddr <cidr> udp dport <port> accept`                                       |
| outbound any protocol   | `ip daddr <addr> accept`                                                        |

Stored as rule-store entries with `proto: "icmp"` (or `"any"`) and a
`nft_statement` field holding the compiled string.

### 9.3 Raw mode

"Raw mode" toggle exposes a textarea for users who want to type nft
statements directly. Lines are validated:

1. Lex the statement; reject if it contains `drop`, `reject`, `policy`,
   `flush`, `delete`, `table`, `chain`, or any keyword that targets a
   chain other than `user_allow`.
2. Pipe through `nft --check -f -` after wrapping in
   `add rule inet claude_proxy_fw user_allow <stmt>` — `nft` rejects
   malformed syntax before commit.
3. Only on success: apply via `nft add rule inet claude_proxy_fw
   user_allow <stmt>` and persist to rule store.

### 9.4 Persistence

User rules survive proxy restart. The publish-mgr's startup loop
re-applies rules from the rule store: for each entry with
`nft_statement`, run `nft add rule inet claude_proxy_fw user_allow
<statement>`.

## 10. Phase 4 — TCP UX polish

Pure UI on top of phase 0 schema. Lives in the existing "Rules" tab.

- Group rules by protocol family (HTTP, TCP, UDP, ICMP, Custom).
- Each row shows live counters: bytes in/out, last-seen timestamp,
  open connection count. Counters come from periodic `conntrack -L` +
  `nft list counter` poll (every 5s, ~10ms each).
- Bulk actions: "Deny all hosts not seen in 7d", "Export current
  ruleset as preset".
- Sort and filter (by protocol, by recent activity, by label).

No new addons, no new daemons. Just dashboard reading existing kernel
state.

## 11. Backward compatibility

- Old rules in existing `rules.json` files are normalized on load. Both
  old and new POST shapes accepted for one minor version.
- Existing presets in `<configDir>/proxy-presets/*.json` keep working;
  load goes through the same normalizer.
- New `published-port-allocations.json` file is created on demand;
  absence is treated as "no allocations yet" (not an error).
- The dashboard's existing endpoints (`GET /api/rules`, `POST
  /api/rules`, `DELETE /api/rules/{id}`, `POST /api/resolve`) keep their
  shapes. New endpoints (`POST /api/publish`, etc.) sit alongside.

## 12. Testing strategy

Unit tests:
- Rule schema normalizer: old → new mapping is correct.
- Rule matcher: new shape with each `match.*` field exercised.
- nft statement compiler: each template produces the expected statement.
- Raw-mode validator: rejects forbidden keywords.

Integration tests (`cmd/security_e2e_test.go` group):

- `TestSecurity_Publish_TCP_AllocatesAndUnpublishes` — POST publish,
  curl from host to localhost:N succeeds, POST unpublish, curl fails.
- `TestSecurity_Publish_UDP_AllocatesAndUnpublishes` — same for UDP.
- `TestSecurity_Publish_ConcurrentSessions_NoCollision` — two
  sessions each publish; ranges don't overlap.
- `TestSecurity_UDP_UnknownHeld_DenyTimesOut` — UDP datagram to unknown
  dest is held; without approval the buffer expires and the datagram is
  dropped.
- `TestSecurity_UDP_Approve_DatagramFlows` — held UDP, dashboard
  approves, buffered datagrams are flushed.
- `TestSecurity_UDP_DNS_ParsesQueryName` — UDP/53 hold surfaces the
  DNS query name.
- `TestSecurity_UserAllow_AppendOnly` — POST a raw rule containing
  `drop` is rejected; `accept` rule applies.
- `TestSecurity_UserAllow_CannotBypassSecurityChain` — even with a
  user-allow rule `ip daddr 0/0 accept`, the existing `udp dport 53
  drop` still fires (rule evaluation order).
- `TestSecurity_PortRange_ExhaustionErrorsCleanly` — exhaust the pool,
  next session fails fast with a clear error message.

## 13. Risks and open items

1. **TPROXY needs CAP_NET_ADMIN at the binding step.** The proxy
   container already has it. The udp-redir daemon needs it too,
   inherited from the container's caps. Verify experimentally on
   rootless docker.
2. **`IP_TRANSPARENT` on a non-root socket.** With CAP_NET_ADMIN we can
   set it; verify uid 1500 + setcap on the binary works in the nix
   build.
3. **mitmproxy's conntrack expectations.** When we add tproxy for UDP,
   conntrack treats it as a new flow. Verify mitmproxy's TCP path isn't
   inadvertently affected.
4. **Performance of poll-based counters in Phase 4.** Five-second poll
   is cheap on small rulesets; sanity-check at 50-100 active rules.
5. **Documentation burden.** The dashboard gains real surface area.
   Plan for a `docs/dashboard.md` companion page.

## 14. Phasing summary

| Phase | Content                                       | Independent? | Estimated effort |
|-------|-----------------------------------------------|--------------|------------------|
| 0     | Unified rule schema + dashboard scaffolding   | Foundation   | Small            |
| 1     | Inbound publish (TCP+UDP), publish-mgr        | Yes          | Medium           |
| 2     | UDP outbound (udp-redir, TPROXY, DNS-aware)   | Yes          | Largest          |
| 3     | nft user sub-chain (templates + raw mode)     | Yes          | Medium           |
| 4     | TCP UX polish (counters, grouping, bulk ops)  | Yes          | Small            |

Each phase ends with: green security suite (existing 34 tests still
pass + new phase tests), updated `docs/security/` audit if the threat
model changes, and a brief CHANGELOG entry.
