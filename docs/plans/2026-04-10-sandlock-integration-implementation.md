# Sandlock Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the non-functional bubblewrap sandbox with Sandlock (Landlock + seccomp-bpf) as an outer confinement wrapper around Claude Code inside containers.

**Architecture:** Fork sandlock, apply security patch, package as a nix derivation, add to the container image, wrap the entrypoint exec in `sandlock run` with a dynamically-built policy. Add `--no-sandbox` escape hatch and kernel version check.

**Tech Stack:** Rust (sandlock build), Nix (packaging), bash (entrypoint), Go (CLI flags)

---

## File Structure

| File | Responsibility |
|------|---------------|
| `nix/sandlock.nix` | Nix derivation to build sandlock CLI from our fork |
| `nix/image.nix` | Replace bubblewrap with sandlock in pathPackages, add sandlock wrapping to entrypoint exec |
| `nix/flake.nix` | Add sandlock package to overlay |
| `internal/config/config.go` | `NoSandbox` field on Session struct |
| `internal/docker/docker.go` | Pass `NO_SANDBOX=1` env var when flag set |
| `cmd/new.go` | `--no-sandbox` flag |
| `cmd/work.go` | `--no-sandbox` flag |
| `cmd/run.go` | `--no-sandbox` flag |

---

## Phase 1: Fork and Patch Sandlock

### Task 1.1: Fork the repo and apply the security patch

- [ ] Fork `github.com/multikernel/sandlock` to our GitHub org
- [ ] Clone the fork locally
- [ ] Download the security patch from multikernel/sandlock#13: `sandlock-security-fixes.patch`
- [ ] Apply: `git apply sandlock-security-fixes.patch`
- [ ] Verify it applies cleanly to current main branch
- [ ] If conflicts: resolve manually, the patch touches 11 files in `crates/sandlock-core/src/`:
  - `context.rs`, `cow/dispatch.rs`, `cow/seccomp.rs`, `network.rs`, `procfs.rs`, `random.rs`, `resource.rs`, `sandbox.rs`, `seccomp/bpf.rs`, `seccomp/dispatch.rs`, `seccomp/state.rs`, `sys/structs.rs`
- [ ] Commit the patch: "security: apply fixes for 28 sandbox escape vulnerabilities"
- [ ] Push to fork

### Task 1.2: Build and test sandlock locally

- [ ] `cargo build --release -p sandlock-cli` — verify it compiles
- [ ] `cargo test --release` — run the test suite
- [ ] Quick smoke test: `./target/release/sandlock run -r /usr -r /lib -r /lib64 -r /bin -r /etc -w /tmp -- echo hello`
- [ ] Run the PoC script against the patched build: `python3 qmd-sandlock.py` — verify all 8 PoC categories fail (sandbox holds)
- [ ] Commit: (already committed in 1.1)

---

## Phase 2: Nix Packaging

### Task 2.1: Create `nix/sandlock.nix`

- [ ] Read `nix/flake.nix` to understand how packages are defined (lines 108-131 for the Go build pattern)
- [ ] Create `nix/sandlock.nix`:
  ```nix
  {
    lib,
    rustPlatform,
    fetchFromGitHub,
  }:

  rustPlatform.buildRustPackage {
    pname = "sandlock";
    version = "0.1.0-patched";

    src = fetchFromGitHub {
      owner = "<our-github-org>";
      repo = "sandlock";
      rev = "<commit-sha-with-patch>";
      hash = lib.fakeHash;  # nix build will tell you the real hash
    };

    cargoHash = lib.fakeHash;  # nix build will tell you the real hash

    cargoBuildFlags = [ "-p" "sandlock-cli" ];

    # Only install the CLI binary
    postInstall = ''
      rm -rf $out/lib  # remove any FFI library artifacts
    '';

    meta = with lib; {
      description = "Lightweight process sandbox using Landlock + seccomp";
      license = licenses.mit;
      platforms = platforms.linux;
    };
  }
  ```
- [ ] Run `nix build .#sandlock` — it will fail with hash mismatch, copy the correct hashes
- [ ] Update both `hash` and `cargoHash` with correct values
- [ ] Run `nix build .#sandlock` — verify it builds successfully
- [ ] Test the built binary: `./result/bin/sandlock run -r /usr -r /lib -w /tmp -- echo hello`
- [ ] Commit: "feat: nix derivation for sandlock CLI"

### Task 2.2: Add sandlock to flake.nix

- [ ] Read `nix/flake.nix` overlay section
- [ ] Add sandlock to the package set. In the `let` block where packages are defined, add:
  ```nix
  sandlock = pkgs.callPackage ./nix/sandlock.nix { };
  ```
- [ ] Export it as a package if desired (optional, mainly needed for image.nix)
- [ ] Run `nix build` — verify the full build still works
- [ ] Commit: "feat: add sandlock to nix flake"

---

## Phase 3: Container Image Integration

### Task 3.1: Replace bubblewrap with sandlock in pathPackages

- [ ] Read `nix/image.nix` lines 319-350 (pathPackages list)
- [ ] Remove `bubblewrap` from the list (line ~324)
- [ ] Add `sandlock` to the list (reference the package from flake.nix)
- [ ] The `image.nix` function signature (line 2-9) takes `pkgs` — sandlock should be available via `pkgs.sandlock` if added to the overlay, or passed as an explicit argument
- [ ] Run `nix build` — verify image builds
- [ ] Commit: "refactor: replace bubblewrap with sandlock in container image"

### Task 3.2: Add kernel version check to entrypoint

- [ ] Read `nix/image.nix` entrypoint exec section (lines 308-315)
- [ ] Add kernel version check just before the exec block:
  ```bash
  # --- Sandbox setup ---
  USE_SANDLOCK=1
  if [ "''${NO_SANDBOX:-}" = "1" ]; then
    USE_SANDLOCK=0
    log "sandlock disabled by NO_SANDBOX=1"
  else
    KERN_MAJ=$(${pkgs.coreutils}/bin/uname -r | ${pkgs.coreutils}/bin/cut -d. -f1)
    KERN_MIN=$(${pkgs.coreutils}/bin/uname -r | ${pkgs.coreutils}/bin/cut -d. -f2)
    if [ "$KERN_MAJ" -lt 5 ] || { [ "$KERN_MAJ" -eq 5 ] && [ "$KERN_MIN" -lt 13 ]; }; then
      USE_SANDLOCK=0
      log "WARNING: kernel $(uname -r) too old for sandlock (need 5.13+), running without sandbox"
    fi
  fi
  ```
- [ ] Run `nix build` — verify image builds
- [ ] Commit: "feat: kernel version check and NO_SANDBOX env var support"

### Task 3.3: Build sandlock policy dynamically in entrypoint

- [ ] Add sandlock argument construction after the kernel check, before the exec block:
  ```bash
  if [ "$USE_SANDLOCK" = "1" ]; then
    SANDLOCK_ARGS=(
      -r /nix/store
      -r /etc
      -w /workspace
      -w /claude
      -w /tmp
      -w /nix/var
    )

    # Proxy CA cert (read-only, if mounted)
    [ -d /proxy-ca ] && SANDLOCK_ARGS+=(-r /proxy-ca)

    # Host credential files (read-only, if mounted)
    for f in /mnt/claude-host/.credentials.json /mnt/claude-host/settings.json /mnt/claude-host/.claude.json; do
      [ -f "$f" ] && SANDLOCK_ARGS+=(-r "$f")
    done

    # Worktree source repos (read-only)
    [ -d /mnt/repo ] && SANDLOCK_ARGS+=(-r /mnt/repo)
    [ -d /mnt/repos ] && SANDLOCK_ARGS+=(-r /mnt/repos)

    # Network: proxy intercepts all HTTP(S), allow outbound on standard ports
    SANDLOCK_ARGS+=(--net-connect 443 --net-connect 80)
    # Proxy dashboard/API on loopback
    SANDLOCK_ARGS+=(--net-connect 8080 --net-connect 8081)

    # HOME directory needs to be readable (symlink to /claude)
    [ -L "$HOME/.claude" ] && SANDLOCK_ARGS+=(-r "$HOME")
  fi
  ```
- [ ] Run `nix build`
- [ ] Commit: "feat: dynamic sandlock policy construction in entrypoint"

### Task 3.4: Wrap the exec in sandlock

- [ ] Read the current exec section (lines 308-315):
  ```bash
  log "exec as $USER_NAME: $*"
  log "PATH=$PATH"
  if [ "$USER_UID" -eq 0 ]; then
    exec "$@"
  else
    exec ${suExec} "$USER_NAME" "$@"
  fi
  ```
- [ ] Replace with:
  ```bash
  log "exec as $USER_NAME: $*"
  log "PATH=$PATH"
  if [ "$USE_SANDLOCK" = "1" ]; then
    log "sandlock policy: ''${SANDLOCK_ARGS[*]}"
    if [ "$USER_UID" -eq 0 ]; then
      exec ${sandlockBin} run "''${SANDLOCK_ARGS[@]}" -- "$@"
    else
      exec ${suExec} "$USER_NAME" ${sandlockBin} run "''${SANDLOCK_ARGS[@]}" -- "$@"
    fi
  else
    if [ "$USER_UID" -eq 0 ]; then
      exec "$@"
    else
      exec ${suExec} "$USER_NAME" "$@"
    fi
  fi
  ```
  Where `sandlockBin` is defined at the top of the file alongside other full-path binaries:
  ```nix
  sandlockBin = "${sandlock}/bin/sandlock";
  ```
- [ ] Add `sandlock` to the `let` bindings at the top of image.nix (like `suExec`, `gitBin`, etc.)
- [ ] Run `nix build` — verify image builds
- [ ] Commit: "feat: wrap Claude Code exec in sandlock confinement"

---

## Phase 4: Go CLI `--no-sandbox` Flag

### Task 4.1: Add `NoSandbox` to Session struct

- [ ] Read `internal/config/config.go` Session struct (lines 24-48)
- [ ] Add `NoSandbox bool` field
- [ ] Run `go build ./...`
- [ ] Commit: "feat: NoSandbox field on Session"

### Task 4.2: Add `NO_SANDBOX` env var to docker.go

- [ ] Read `internal/docker/docker.go` RunOpts struct (lines 82-106)
- [ ] Add `NoSandbox bool` field to RunOpts
- [ ] In `RunArgs()`, after the existing env var section (~line 200):
  ```go
  if opts.NoSandbox {
      args = append(args, "-e", "NO_SANDBOX=1")
  }
  ```
- [ ] Do the same in `TaskRunArgs()`
- [ ] Run `go build ./...`
- [ ] Commit: "feat: pass NO_SANDBOX env var to container"

### Task 4.3: Add `--no-sandbox` flag to new, work, run

- [ ] Read `cmd/new.go` flag definitions (lines 23-45)
- [ ] Add `newNoSandbox bool` flag variable
- [ ] Register: `newCmd.Flags().BoolVar(&newNoSandbox, "no-sandbox", false, "disable sandlock confinement inside container")`
- [ ] Add `noSandbox bool` field to `createOpts` struct
- [ ] Wire through in `createSession()`: `createOpts.noSandbox` → `RunOpts.NoSandbox` and `sess.NoSandbox`
- [ ] Add same flag and wiring to `cmd/work.go` and `cmd/run.go`
- [ ] Update `cmd/attach.go` `ensureRunning()` to pass `NoSandbox: sess.NoSandbox` in RunOpts when recreating
- [ ] Run `go build ./...`
- [ ] Commit: "feat: --no-sandbox flag for new, work, run commands"

---

## Phase 5: Testing

### Task 5.1: Smoke test — sandlock confinement works

- [ ] Build the image: `nix build`
- [ ] Load the image: `docker load < result`
- [ ] Run a container manually:
  ```bash
  docker run --rm -it claude-code:latest /bin/sh
  ```
- [ ] Inside the container, verify sandlock is on PATH: `which sandlock`
- [ ] Test basic confinement:
  ```bash
  sandlock run -r /usr -r /lib -r /nix/store -r /etc -w /tmp -- ls /tmp
  sandlock run -r /usr -r /lib -r /nix/store -r /etc -w /tmp -- cat /etc/passwd  # should work (read-only)
  sandlock run -r /usr -r /lib -r /nix/store -r /etc -w /tmp -- touch /etc/test  # should fail (read-only)
  ```
- [ ] Commit: (no code changes, testing only)

### Task 5.2: Integration test — full session lifecycle

- [ ] Run `claude-container run --name sandbox-test` in a test repo
- [ ] Verify Claude Code starts successfully inside sandlock
- [ ] Verify Claude Code can read/write `/workspace` (create a file)
- [ ] Verify Claude Code can write to `/claude` (conversation transcript created)
- [ ] Verify network works (Claude can reach the API through the proxy)
- [ ] Detach, then `claude-container attach sandbox-test` — verify reattach works
- [ ] Stop and remove session
- [ ] Commit: (no code changes, testing only)

### Task 5.3: Negative test — sandlock blocks unauthorized access

- [ ] Run a container, exec into it:
  ```bash
  docker exec -it <container> /bin/sh
  ```
- [ ] Try to access paths outside the policy from within the sandlock-wrapped process:
  ```bash
  # These should fail inside the sandlock-wrapped Claude Code process
  # Test by running sandlock manually with the same policy:
  sandlock run -r /nix/store -r /etc -w /workspace -w /claude -w /tmp -- cat /root/.bashrc
  sandlock run -r /nix/store -r /etc -w /workspace -w /claude -w /tmp -- ls /mnt/claude-host/
  ```
- [ ] Verify access denied
- [ ] Commit: (no code changes, testing only)

### Task 5.4: Test `--no-sandbox` escape hatch

- [ ] Run `claude-container run --name nosandbox-test --no-sandbox`
- [ ] Verify Claude Code starts without sandlock wrapping (check entrypoint logs for "sandlock disabled")
- [ ] Verify full functionality
- [ ] Stop and remove session
- [ ] Commit: (no code changes, testing only)

---

## Phase 6: Cleanup

### Task 6.1: Update managed-settings documentation

- [ ] The `enableWeakerNestedSandbox` setting in managed-settings.nix now serves as defense-in-depth rather than the primary sandbox. Add a comment in `nix/managed-settings.nix` explaining this:
  ```nix
  # Claude Code's internal bwrap sandbox is weakened because full bwrap
  # doesn't work inside unprivileged Docker. Sandlock (Landlock + seccomp)
  # provides the real confinement layer as an outer wrapper. This setting
  # is kept as defense-in-depth.
  enableWeakerNestedSandbox = true;
  ```
- [ ] Commit: "docs: clarify sandbox layering in managed-settings"
