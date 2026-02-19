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

  managedSettingsFile = pkgs.writeText "managed-settings.json" (builtins.toJSON managedSettings);

  settingsFile =
    if settings != { } then pkgs.writeText "settings.json" (builtins.toJSON settings) else null;

  entrypoint = pkgs.writeShellScript "entrypoint.sh" ''
    set -e

    USER_UID=''${USER_UID:-1000}
    USER_GID=''${USER_GID:-1000}

    # If running as root, exec directly
    if [ "$USER_UID" -eq 0 ]; then
      exec "$@"
    fi

    # Create group if it doesn't exist
    if ! ${getent} group "$USER_GID" >/dev/null 2>&1; then
      ${groupadd} -g "$USER_GID" claude 2>/dev/null || true
      GROUP_NAME="claude"
    else
      GROUP_NAME=$(${getent} group "$USER_GID" | ${cut} -d: -f1)
    fi

    # Create user if it doesn't exist
    if ! ${getent} passwd "$USER_UID" >/dev/null 2>&1; then
      ${useradd} -u "$USER_UID" -g "$GROUP_NAME" -d /home/claude -s ${bash}/bin/bash -M claude 2>/dev/null || true
      ${mkdir} -p /home/claude
      ${chown} "$USER_UID:$USER_GID" /home/claude
      USER_NAME="claude"
    else
      USER_NAME=$(${getent} passwd "$USER_UID" | ${cut} -d: -f1)
    fi

    # Fix config dir permissions
    if [ -d /claude ]; then
      ${chown} -R "$USER_UID:$USER_GID" /claude 2>/dev/null || true
      ${chmod} 755 /claude 2>/dev/null || true
    fi

    # Copy host credentials if mounted read-only
    if [ -d /mnt/claude-host ]; then
      for f in .credentials.json settings.json .claude.json; do
        if [ -f "/mnt/claude-host/$f" ]; then
          ${cp} -L "/mnt/claude-host/$f" "/claude/$f"
          ${chown} "$USER_UID:$USER_GID" "/claude/$f"
          ${chmod} 600 "/claude/$f"
        fi
      done
    fi

    if [ -f /mnt/claude-host-json ]; then
      ${cp} -L /mnt/claude-host-json /claude/.claude.json
      ${chown} "$USER_UID:$USER_GID" /claude/.claude.json
      ${chmod} 600 /claude/.claude.json
    fi

    # Set HOME
    USER_HOME=$(${getent} passwd "$USER_UID" | ${cut} -d: -f6)
    USER_HOME=''${USER_HOME:-/home/claude}
    export HOME="$USER_HOME"

    # Symlink so Claude finds config at both paths
    ${ln} -sfn /claude "$HOME/.claude" 2>/dev/null || true

    # Ensure bypassPermissionsModeAccepted is set using jq
    if [ -f /claude/.claude.json ]; then
      ${jqBin} '.bypassPermissionsModeAccepted = true' /claude/.claude.json > /claude/.claude.json.tmp && \
        ${mv} /claude/.claude.json.tmp /claude/.claude.json
    else
      echo '{"bypassPermissionsModeAccepted":true}' > /claude/.claude.json
    fi
    ${chown} "$USER_UID:$USER_GID" /claude/.claude.json 2>/dev/null || true

    # Copy to HOME root level
    if [ -f /claude/.claude.json ]; then
      ${cp} /claude/.claude.json "$HOME/.claude.json" 2>/dev/null || true
      ${chown} "$USER_UID:$USER_GID" "$HOME/.claude.json" 2>/dev/null || true
    fi

    # Copy baked-in settings if no settings exist in config dir
    if [ ! -f /claude/settings.json ] && [ -f /etc/claude-code/settings.json ]; then
      ${cp} /etc/claude-code/settings.json /claude/settings.json
      ${chown} "$USER_UID:$USER_GID" /claude/settings.json 2>/dev/null || true
    fi

    export SHELL=${bash}/bin/bash
    exec ${suExec} "$USER_NAME" "$@"
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
    ++ extraPackages;

in
pkgs.dockerTools.buildLayeredImage {
  name = "claude-code";
  tag = "nix";

  contents = pathPackages;

  extraCommands = ''
    # Create required directories
    mkdir -p workspace claude etc/claude-code tmp home root

    # Minimal /etc files for shadow/NSS to work at runtime
    echo "root:x:0:0:root:/root:${bash}/bin/bash" > etc/passwd
    echo "root:x:0:" > etc/group
    echo "root:!:1::::::" > etc/shadow

    # NSS configuration so getent reads /etc/passwd and /etc/group
    cat > etc/nsswitch.conf << 'EOF'
    passwd: files
    group: files
    shadow: files
    EOF

    # login.defs for useradd defaults
    cat > etc/login.defs << 'EOF'
    UID_MIN 1000
    UID_MAX 60000
    GID_MIN 1000
    GID_MAX 60000
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
    ];
  };
}
