# Sandlock Integration: Container-Internal Sandbox

## Summary

Replace the non-functional bubblewrap sandbox inside containers with Sandlock as an outer confinement wrapper around Claude Code. Sandlock uses Landlock + seccomp-bpf, which work inside unprivileged Docker without the namespace/mount issues that break bwrap (see containers/bubblewrap#505).

## Problem

- Bubblewrap cannot run inside unprivileged Docker containers — it requires user namespaces and mount operations that Docker's default seccomp/AppArmor profiles block
- `enableWeakerNestedSandbox=true` in managed-settings effectively means "no sandbox" — Claude Code runs unconfined inside the container
- Docker provides OS-level isolation from the host, but within the container there's no confinement — Claude Code can read any mounted path, including sensitive mounts like `/mnt/claude-host` (addressed separately in the conversation persistence spec) and `/proxy-ca`

## Approach

Wrap Claude Code in sandlock at the entrypoint level:

```
# Current
exec claude "$@"

# New
exec sandlock run \
  -r /usr -r /lib -r /lib64 -r /bin -r /etc -r /run \
  -r /nix/store -r /proxy-ca \
  -w /workspace -w /claude -w /tmp \
  --net-connect 443 --net-connect 80 \
  -- claude "$@"
```

This is "option A" — an outer wrapper. Claude Code's internal sandbox stays in weaker mode (`enableWeakerNestedSandbox=true`), but sandlock provides the real filesystem and network confinement. Claude Code doesn't know it's running inside sandlock.

## Design

### 1. Build Sandlock from Source

**Source**: Fork of `github.com/multikernel/sandlock` with security patch from multikernel/sandlock#13 applied.

The patch fixes 28 identified sandbox escape vulnerabilities across 11 source files:
- Atomic resource tracking (race conditions in memory/process limits)
- Fork syscall blocking (direct `fork()` bypassed seccomp monitoring)
- AF_UNIX socket restriction (abstract sockets bypassed network allowlist)
- Memfd sealing (post-injection modification of sealed FDs)
- BPF program size validation (oversized seccomp filters)
- IPC syscall denial (message queues, semaphores, shared memory)
- Error handling hardening (fallback-to-allow replaced with explicit deny)

**Build**: Rust crate, compiled with `cargo build --release`. Only the CLI binary needed (`sandlock`), not the FFI library or Python SDK.

### 2. Nix Packaging

New nix derivation at `nix/sandlock.nix`:

```nix
{ lib, rustPlatform, fetchFromGitHub }:

rustPlatform.buildRustPackage {
  pname = "sandlock";
  version = "0.1.0-patched";

  src = fetchFromGitHub {
    owner = "<our-fork>";
    repo = "sandlock";
    rev = "<commit-with-security-patch>";
    hash = "...";
  };

  cargoBuildFlags = [ "-p" "sandlock-cli" ];
  cargoHash = "...";
}
```

Add `sandlock` to `pathPackages` in `nix/image.nix` so it's available on PATH inside the container. Remove `bubblewrap` from `pathPackages` since it's no longer needed.

### 3. Entrypoint Integration

Modify the entrypoint in `nix/image.nix` to wrap the final `exec` in sandlock.

**Policy construction**: The entrypoint builds the sandlock arguments dynamically based on what's mounted:

```bash
SANDLOCK_ARGS=(
  # Read-only system paths
  -r /nix/store
  -r /etc

  # Read-only proxy CA (if mounted)
  -r /proxy-ca

  # Writable workspace and config
  -w /workspace
  -w /claude
  -w /tmp
  -w /nix/var

  # Network: allow outbound HTTPS/HTTP (proxy handles filtering)
  --net-connect 443
  --net-connect 80
  --net-connect 8080   # proxy dashboard (loopback)
  --net-connect 8081   # proxy API (loopback)
)

# Credential file mounts (read-only, only if present)
for f in /mnt/claude-host/.credentials.json /mnt/claude-host/settings.json /mnt/claude-host/.claude.json; do
  [ -f "$f" ] && SANDLOCK_ARGS+=(-r "$f")
done

# Worktree mounts (if present)
[ -d /mnt/repo ] && SANDLOCK_ARGS+=(-r /mnt/repo)
[ -d /mnt/repos ] && SANDLOCK_ARGS+=(-r /mnt/repos)
```

**Final exec**:
```bash
exec sandlock run "${SANDLOCK_ARGS[@]}" -- "$@"
```

**Non-root users**: Sandlock doesn't require root, so the existing `su-exec` flow works. The entrypoint runs `su-exec $USER_NAME sandlock run ... -- claude`.

### 4. What Sandlock Confines

| Resource | Policy | Effect |
|----------|--------|--------|
| `/workspace` | writable | Claude Code can edit project files |
| `/claude` | writable | Claude Code can write config, transcripts |
| `/tmp` | writable | Scratch space |
| `/nix/var` | writable | Persistent nix store for package installs |
| `/nix/store` | read-only | System binaries and libraries |
| `/etc` | read-only | System config, nix config, nsswitch |
| `/proxy-ca` | read-only | Proxy CA cert for HTTPS |
| `/mnt/claude-host/*` | read-only | Individual credential files (post security fix) |
| `/mnt/repo`, `/mnt/repos` | read-only | Source repos for worktree creation |
| Everything else | denied | Not accessible |
| Outbound TCP 80, 443 | allowed | HTTP/HTTPS through proxy |
| Outbound TCP 8080, 8081 | allowed | Proxy dashboard/API (loopback) |
| All other network | denied | Landlock TCP restriction |

### 5. Interaction with Existing Sandbox Layers

**Docker** (outer): OS-level isolation, namespace separation, only explicitly mounted paths visible. Unchanged.

**Sandlock** (middle, new): Landlock filesystem rules restrict which mounted paths Claude Code can access. Seccomp-bpf blocks dangerous syscalls. This is the real confinement layer inside the container.

**Claude Code's internal sandbox** (inner): Stays at `enableWeakerNestedSandbox=true`. It's defense-in-depth — if sandlock is somehow bypassed, the weaker sandbox still provides some protection. No changes needed to managed-settings.

**Network proxy** (orthogonal): The per-session mitmproxy sidecar handles domain-level filtering. Sandlock's network rules are coarse (allow TCP 80/443) since the proxy does fine-grained URL filtering. They complement each other: sandlock blocks non-HTTP traffic, proxy blocks unauthorized HTTP destinations.

### 6. `--no-sandbox` Escape Hatch

Add `--no-sandbox` flag to `new`, `work`, `run` commands. When set:
- Entrypoint skips the sandlock wrapper, runs `claude` directly
- Stored in `Session.NoSandbox` so `attach` can reproduce it
- Use case: debugging, or when sandlock causes compatibility issues

Default is sandlock enabled.

### 7. Kernel Requirements

Sandlock requires Linux 5.13+ for Landlock filesystem rules, 6.7+ for TCP port rules. Our container image already targets Linux 6.12+ (per sandlock's README, needed for IPC scoping).

The entrypoint should check kernel version at startup. If the kernel is too old for Landlock, log a warning and fall back to no sandlock (same as current behavior).

```bash
KERNEL_VERSION=$(uname -r | cut -d. -f1-2)
if [ "$(printf '%s\n' "5.13" "$KERNEL_VERSION" | sort -V | head -1)" != "5.13" ]; then
  log "WARNING: kernel $KERNEL_VERSION too old for sandlock (need 5.13+), running without sandbox"
  exec "$@"
fi
```

## Testing

- **PoC validation**: Run the `qmd-sandlock.py` PoC script against our patched build to verify all 28 vulnerabilities are fixed
- **Smoke test**: Verify Claude Code starts, can read/write workspace, can reach the proxy, can install nix packages
- **Negative tests**: Verify Claude Code cannot read paths outside the policy (e.g., `/root`, `/proc/1/environ`)
- **Integration test**: Full session lifecycle (create, attach, detach, reattach) with sandlock enabled

## File Changes

| File | Change |
|------|--------|
| `nix/sandlock.nix` | New — nix derivation to build sandlock from our fork |
| `nix/image.nix` | Add sandlock to pathPackages, remove bubblewrap, wrap exec in sandlock |
| `nix/flake.nix` | Add sandlock input/overlay |
| `internal/config/config.go` | Add `NoSandbox` field to Session struct |
| `internal/docker/docker.go` | Pass `NO_SANDBOX` env var when flag set |
| `cmd/new.go` | Add `--no-sandbox` flag |
| `cmd/work.go` | Add `--no-sandbox` flag |
| `cmd/run.go` | Add `--no-sandbox` flag |

## Risks

- **Sandlock maturity**: The project has known vulnerabilities (issue #13). We mitigate by applying the security patch and running the PoC suite in CI.
- **Compatibility**: Some Claude Code operations might fail under Landlock restrictions we didn't anticipate. The `--no-sandbox` flag provides an escape hatch, and we can iteratively loosen the policy.
- **Nix package installs**: `nix profile install` needs write access to `/nix/var` (persistent volume) and may need to write new store paths to `/nix/store`. If substituters are used (binary cache), nix writes pre-built paths directly to the store. The policy includes `-w /nix/var`; we may also need `-w /nix/store` during the package install phase of the entrypoint, then re-exec sandlock with `/nix/store` read-only for the actual Claude Code process.
