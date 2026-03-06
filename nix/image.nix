# nix/image.nix
{
  pkgs,
  lib,
  claude-code,
  settings ? { },
  managedSettings ? import ./managed-settings.nix,
  extraPackages ? [ ],
}:
let
  inherit (pkgs) bash coreutils shadow jq git bubblewrap socat cacert;
  su-exec = pkgs.su-exec;

  # Full Nix store paths for entrypoint commands
  getent = "${pkgs.glibc.getent}/bin/getent";
  groupadd = "${shadow}/bin/groupadd";
  useradd = "${shadow}/bin/useradd";
  chown = "${coreutils}/bin/chown";
  chmod = "${coreutils}/bin/chmod";
  cp = "${coreutils}/bin/cp";
  ln = "${coreutils}/bin/ln";
  mkdir = "${coreutils}/bin/mkdir";
  mv = "${coreutils}/bin/mv";
  cut = "${coreutils}/bin/cut";
  jqBin = "${jq}/bin/jq";
  suExec = "${su-exec}/bin/su-exec";
  gitBin = "${git}/bin/git";

  managedSettingsFile = pkgs.writeText "managed-settings.json" (builtins.toJSON managedSettings);

  settingsFile =
    if settings != { } then pkgs.writeText "settings.json" (builtins.toJSON settings) else null;

  entrypoint = pkgs.writeShellScript "entrypoint.sh" ''
    set -e

    USER_UID=''${USER_UID:-1000}
    USER_GID=''${USER_GID:-1000}

    # --- User setup ---
    # When UID=0 (rootless Docker or actual root), run as container root.
    # In rootless Docker, container UID 0 maps to the host user's UID, so
    # mounted volumes are accessible without any chown. We do NOT remap to
    # a non-root user because that would change ownership of mounted dirs
    # to a subordinate UID on the host.

    if [ "$USER_UID" -eq 0 ]; then
      USER_NAME="root"
      USER_HOME="/root"
      ${mkdir} -p /root
    else
      # Non-rootless Docker: create user matching the host UID.
      if ! ${getent} group "$USER_GID" >/dev/null 2>&1; then
        ${groupadd} -g "$USER_GID" claude 2>/dev/null || true
        GROUP_NAME="claude"
      else
        GROUP_NAME=$(${getent} group "$USER_GID" | ${cut} -d: -f1)
      fi

      if ! ${getent} passwd "$USER_UID" >/dev/null 2>&1; then
        ${useradd} -u "$USER_UID" -g "$GROUP_NAME" -d /home/claude -s ${bash}/bin/bash -M claude 2>/dev/null || true
        ${mkdir} -p /home/claude
        ${chown} "$USER_UID:$USER_GID" /home/claude
        USER_NAME="claude"
      else
        USER_NAME=$(${getent} passwd "$USER_UID" | ${cut} -d: -f1)
      fi

      USER_HOME=$(${getent} passwd "$USER_UID" | ${cut} -d: -f6)
      USER_HOME=''${USER_HOME:-/home/claude}
    fi

    export HOME="$USER_HOME"

    # --- Config setup ---

    # Copy host credentials if mounted read-only
    if [ -d /mnt/claude-host ]; then
      for f in .credentials.json settings.json .claude.json; do
        if [ -f "/mnt/claude-host/$f" ]; then
          ${cp} -L "/mnt/claude-host/$f" "/claude/$f"
          ${chmod} 600 "/claude/$f"
        fi
      done
    fi

    if [ -f /mnt/claude-host-json ]; then
      ${cp} -L /mnt/claude-host-json /claude/.claude.json
      ${chmod} 600 /claude/.claude.json
    fi

    # Symlink so Claude finds config at both paths
    ${ln} -sfn /claude "$HOME/.claude" 2>/dev/null || true

    # Ensure bypassPermissionsModeAccepted is set using jq
    if [ -f /claude/.claude.json ]; then
      ${jqBin} '.bypassPermissionsModeAccepted = true' /claude/.claude.json > /claude/.claude.json.tmp && \
        ${mv} /claude/.claude.json.tmp /claude/.claude.json
    else
      echo '{"bypassPermissionsModeAccepted":true}' > /claude/.claude.json
    fi

    # Copy to HOME root level
    if [ -f /claude/.claude.json ]; then
      ${cp} /claude/.claude.json "$HOME/.claude.json" 2>/dev/null || true
    fi

    # Copy baked-in settings if no settings exist in config dir
    if [ ! -f /claude/settings.json ] && [ -f /etc/claude-code/settings.json ]; then
      ${cp} /etc/claude-code/settings.json /claude/settings.json
    fi

    # If managed settings were provided via config dir (by the Go binary),
    # copy them to the enterprise path so they take priority over baked-in defaults.
    if [ -f /claude/managed-settings.json ]; then
      ${cp} /claude/managed-settings.json /etc/claude-code/managed-settings.json
    fi

    # For non-root user: fix ownership of config files created above by root.
    # Do NOT chown /workspace — Docker handles mount ownership via UID mapping.
    if [ "$USER_UID" -ne 0 ]; then
      ${chown} -R "$USER_UID:$USER_GID" /claude 2>/dev/null || true
      ${chmod} 755 /claude 2>/dev/null || true
      ${chown} "$USER_UID:$USER_GID" "$HOME/.claude.json" 2>/dev/null || true
    fi

    export SHELL=${bash}/bin/bash

    # --- Worktree setup ---
    # Helper: create a worktree from a repo dir to a target dir.
    create_worktree() {
      local repo_dir="$1"
      local target_dir="$2"

      ${gitBin} config --global --add safe.directory "$repo_dir"
      ${gitBin} config --global --add safe.directory "$target_dir"

      if [ "$USER_UID" -eq 0 ]; then
        if [ -n "$WORKTREE_FROM" ]; then
          ${gitBin} -C "$repo_dir" worktree add -b "$WORKTREE_BRANCH" "$target_dir" "$WORKTREE_FROM"
        else
          ${gitBin} -C "$repo_dir" worktree add -b "$WORKTREE_BRANCH" "$target_dir"
        fi
      else
        if [ -n "$WORKTREE_FROM" ]; then
          ${suExec} "$USER_NAME" ${gitBin} -C "$repo_dir" worktree add -b "$WORKTREE_BRANCH" "$target_dir" "$WORKTREE_FROM"
        else
          ${suExec} "$USER_NAME" ${gitBin} -C "$repo_dir" worktree add -b "$WORKTREE_BRANCH" "$target_dir"
        fi
      fi
    }

    # For non-root users, grant write access to /workspace so git worktree
    # add can populate it. git handles existing empty directories.
    if [ -n "$WORKTREE_BRANCH" ] && [ "$USER_UID" -ne 0 ]; then
      ${chown} "$USER_UID:$USER_GID" /workspace
      ${chown} "$USER_UID:$USER_GID" /worktrees
    fi

    # Single-repo worktree: /mnt/repo → /worktrees/<branch>, symlinked to /workspace
    if [ -n "$WORKTREE_BRANCH" ] && [ -d /mnt/repo ] && [ ! -e "/worktrees/$WORKTREE_BRANCH/.git" ]; then
      create_worktree /mnt/repo "/worktrees/$WORKTREE_BRANCH"
      # Replace empty /workspace dir with symlink (ln -sfn won't replace a dir, so rmdir first)
      rmdir /workspace 2>/dev/null || true
      ${ln} -sfn "/worktrees/$WORKTREE_BRANCH" /workspace
      # Re-enter /workspace so the shell cwd resolves through the new symlink;
      # without this, the process cwd is the deleted directory inode.
      cd /workspace
      ${gitBin} config --global --add safe.directory /workspace
    fi

    # Multi-repo worktrees: /mnt/repos/<name> → /worktrees/<branch>/<name>, symlinked into /workspace/
    if [ -n "$WORKTREE_BRANCH" ] && [ -n "$WORKTREE_REPOS" ]; then
      ${mkdir} -p "/worktrees/$WORKTREE_BRANCH"
      if [ "$USER_UID" -ne 0 ]; then
        ${chown} "$USER_UID:$USER_GID" "/worktrees/$WORKTREE_BRANCH"
      fi
      IFS=',' read -r -a _repos <<< "$WORKTREE_REPOS"
      for _repo_name in "''${_repos[@]}"; do
        if [ -d "/mnt/repos/$_repo_name" ] && [ ! -e "/workspace/$_repo_name/.git" ]; then
          create_worktree "/mnt/repos/$_repo_name" "/worktrees/$WORKTREE_BRANCH/$_repo_name"
          ${ln} -sfn "/worktrees/$WORKTREE_BRANCH/$_repo_name" "/workspace/$_repo_name"
        fi
      done
    fi

    if [ "$USER_UID" -eq 0 ]; then
      exec "$@"
    else
      exec ${suExec} "$USER_NAME" "$@"
    fi
  '';

  # Packages available on PATH inside the container
  pathPackages =
    [
      bash
      coreutils
      git
      bubblewrap
      socat
      jq
      claude-code
    ]
    ++ (with pkgs; [
      curl
      findutils
      gnugrep
      gnused
      gawk
      ripgrep
      fd
      tree
      diffutils
      gnutar
      gzip
      less
      file
      which
      python3Minimal
      nix
    ])
    ++ extraPackages;

in
pkgs.dockerTools.buildLayeredImage {
  name = "claude-code";
  tag = "latest";

  contents = pathPackages;

  enableFakechroot = true;

  fakeRootCommands = ''
    ${pkgs.dockerTools.shadowSetup}
    groupadd -g 1000 claude
    useradd -u 1000 -g claude -d /home/claude -s ${bash}/bin/bash -m claude
  '';

  extraCommands = ''
    # Create required directories
    mkdir -p workspace worktrees claude etc/claude-code tmp
    chmod 1777 tmp

    # Nix configuration for in-container package management
    mkdir -p etc/nix
    cat > etc/nix/nix.conf << 'NIXCONF'
    experimental-features = nix-command flakes
    sandbox = false
    NIXCONF

    # Create nix profile and var directories
    mkdir -p nix/var/nix/profiles nix/var/nix/db nix/var/nix/gcroots

    # NSS configuration so getent reads /etc/passwd and /etc/group
    cat > etc/nsswitch.conf << 'EOF'
    passwd: files
    group: files
    shadow: files
    EOF

    # Baked-in managed settings
    cp ${managedSettingsFile} etc/claude-code/managed-settings.json

    ${lib.optionalString (settingsFile != null) ''
      cp ${settingsFile} etc/claude-code/settings.json
    ''}
  '';

  config = {
    WorkingDir = "/workspace";
    Entrypoint = [ "${entrypoint}" ];
    Cmd = [ "claude" ];
    Env = [
      "CLAUDE_CONFIG_DIR=/claude"
      "PATH=${lib.makeBinPath pathPackages}:/usr/local/bin:/usr/bin:/bin"
      "SSL_CERT_FILE=${cacert}/etc/ssl/certs/ca-bundle.crt"
      "NIX_SSL_CERT_FILE=${cacert}/etc/ssl/certs/ca-bundle.crt"
      "NIX_PATH=nixpkgs=${pkgs.path}"
    ];
  };
}
