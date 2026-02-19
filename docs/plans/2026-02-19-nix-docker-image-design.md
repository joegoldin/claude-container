# Nix-based Docker Image Design

**Date:** 2026-02-19

**Goal:** Replace the Alpine/npm-based Docker image with a Nix-built image using `dockerTools`, and expose a NixOS-style module so consumers (dotfiles) can pass in plugins, settings, and extra packages using the same `claude-nix` patterns used on the host.

**Architecture:** The `claude-container` flake exposes a module with options for plugins, settings, and extra packages. The module builds a Docker image via `pkgs.dockerTools.buildLayeredImage` with everything baked in. The Go binary loads the image via `docker load` instead of `docker build`.

**Tech Stack:** Nix (dockerTools, buildLayeredImage), claude-nix lib, Go (CLI binary)

---

## Module API

The `claude-container` flake exposes configuration options that consumers set:

```nix
claude-container = {
  plugins = [ superpowersPluginComplete nixPlugin ];
  settings = { /* settings.json content */ };
  managedSettings = { /* sandbox/permissions config */ };
  extraPackages = with pkgs; [ ripgrep fd ];
};
```

The module produces:
- `pkgs.claude-container` — Go binary wrapped with the correct image reference
- `pkgs.claude-container-image` — OCI image tarball

## Image Contents

Built via `dockerTools.buildLayeredImage` with these layers:

1. **Base:** bash, coreutils, shadow, su-exec, git, bubblewrap, socat
2. **Claude Code:** `pkgs.llm-agents.claude-code` (from Nix, not npm)
3. **Plugins:** All plugin derivations merged via `buildEnv`, referenced by the wrapped `claude` binary
4. **Config:** `settings.json`, `managed-settings.json`, entrypoint script
5. **Extra packages:** Whatever `extraPackages` specifies

## Entrypoint

A Nix-built bash script (`writeShellScript`) that:

1. Creates user/group matching `USER_UID`/`USER_GID` (using shadow utilities from Nix)
2. Chowns `/claude` config directory
3. Copies host credentials from read-only mount (`/mnt/claude-host`) if present
4. Symlinks `$HOME/.claude -> /claude`
5. Sets `bypassPermissionsModeAccepted` in `.claude.json` (using `jq` instead of `node -e`)
6. Copies `.claude.json` to `$HOME/.claude.json`
7. Execs `su-exec $USER "$@"`

## Image Loading Strategy

- Nix build produces an OCI tarball (`.tar.gz`)
- Image tag includes a content hash for cache invalidation
- Go binary environment variable: `CLAUDE_CONTAINER_IMAGE_TARBALL` (path to tarball)
- `ensureImage()` runs `docker load -i <tarball>` when image is missing
- Replaces current `docker build` + `CLAUDE_CONTAINER_DOCKER_CONTEXT` pattern

## Auth Pattern (unchanged)

Two credential sources, same as current:

1. **Host credentials** (`~/.claude/`) — mounted read-only at `/mnt/claude-host`, entrypoint copies into `/claude/`
2. **Shared config dir** (`~/.config/claude-container/claude-config/`) — mounted at `/claude`, credentials written directly by Claude Code

Auth commands unchanged:
- `claude-container auth` — interactive container, polls for `.credentials.json`, auto-exits
- `claude-container auth status` — checks both host and shared config dir
- `requireAuth()` pre-flight in `new` and `attach`

Minor changes:
- `docker.ImageName` becomes dynamic (content-hashed tag)
- `jq` replaces `node -e` for JSON manipulation in entrypoint
- `su-exec` from Nix packages instead of Alpine

## Go Binary Changes

- Remove `CLAUDE_CONTAINER_DOCKER_CONTEXT` env var and `docker build` logic
- Add `CLAUDE_CONTAINER_IMAGE_TARBALL` env var (set by Nix wrapper)
- `docker.ImageName` becomes a function/variable that includes content hash
- New `docker.EnsureImage(tarball, tag)` — runs `docker load` if tag missing
- Remove `BuildArgs()`, `Build()` functions
- Remove `build` subcommand (or repurpose it to force `docker load`)

## Dotfiles Integration

```nix
# dotfiles/flake.nix — inputs unchanged
claude-container = {
  url = "git+file:///home/joe/Development/claude-container";
  inputs.nixpkgs.follows = "nixpkgs";
};

# dotfiles overlay or package reference
# claude-container flake exposes a function/overlay that accepts config
claude-container = inputs.claude-container.packages.${system}.claude-container.override {
  plugins = [ superpowersPluginComplete nixPlugin ];
  settings = settingsContent;
  extraPackages = with pkgs; [ ripgrep fd ];
};
```

## Directory Layout (unchanged on host)

```
~/.config/claude-container/
  sessions.json           # session tracking
  worktrees/              # git worktrees
  claude-config/          # shared CLAUDE_CONFIG_DIR
    .credentials.json     # written by Claude Code during auth
    .claude.json          # written by Claude Code
    projects/             # project settings
```
