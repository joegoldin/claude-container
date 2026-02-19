#!/bin/sh
#
# Entrypoint script for Claude Code container
# Handles dynamic UID/GID mapping to match host user
#

set -e

# Default to user 1000:1000 if not specified
USER_UID=${USER_UID:-1000}
USER_GID=${USER_GID:-1000}

# If running as root (UID 0), stay as root
if [ "$USER_UID" -eq 0 ]; then
    exec "$@"
fi

# Create group if it doesn't exist
if ! getent group "$USER_GID" >/dev/null 2>&1; then
    addgroup -g "$USER_GID" claude 2>/dev/null || true
else
    EXISTING_GROUP=$(getent group "$USER_GID" | cut -d: -f1)
    if [ -n "$EXISTING_GROUP" ] && [ "$EXISTING_GROUP" != "claude" ]; then
        GROUP_NAME="$EXISTING_GROUP"
    else
        GROUP_NAME="claude"
    fi
fi

GROUP_NAME=${GROUP_NAME:-claude}

# Create user if it doesn't exist
if ! getent passwd "$USER_UID" >/dev/null 2>&1; then
    adduser -D -u "$USER_UID" -G "$GROUP_NAME" -h /home/claude -s /bin/sh claude 2>/dev/null || true
    USER_NAME="claude"
else
    USER_NAME=$(getent passwd "$USER_UID" | cut -d: -f1)
fi

# Ensure config directory is accessible
if [ -d /claude ]; then
    chown "$USER_UID:$USER_GID" /claude 2>/dev/null || true
    chmod 755 /claude 2>/dev/null || true
fi

if [ -d /workspace ]; then
    chmod 755 /workspace 2>/dev/null || true
fi

# Copy host credentials into the user's home .claude dir and the config dir.
# The host file is bind-mounted read-only at /tmp/host-credentials.json and
# may be unreadable due to Docker user namespace remapping, so we copy it
# as root (which can read any file) before dropping privileges.
if [ -f /tmp/host-credentials.json ]; then
    USER_HOME=$(getent passwd "$USER_UID" | cut -d: -f6)
    USER_HOME=${USER_HOME:-/home/claude}
    mkdir -p "$USER_HOME/.claude"
    cp /tmp/host-credentials.json "$USER_HOME/.claude/.credentials.json"
    chown -R "$USER_UID:$USER_GID" "$USER_HOME/.claude"
    chmod 600 "$USER_HOME/.claude/.credentials.json"
    # Also copy into CLAUDE_CONFIG_DIR in case claude looks there.
    if [ -n "$CLAUDE_CONFIG_DIR" ] && [ -d "$CLAUDE_CONFIG_DIR" ]; then
        cp /tmp/host-credentials.json "$CLAUDE_CONFIG_DIR/.credentials.json"
        chown "$USER_UID:$USER_GID" "$CLAUDE_CONFIG_DIR/.credentials.json"
        chmod 600 "$CLAUDE_CONFIG_DIR/.credentials.json"
    fi
fi

# Copy host Claude settings (.claude.json) to skip first-run onboarding.
if [ -f /tmp/host-claude-settings.json ]; then
    USER_HOME=$(getent passwd "$USER_UID" | cut -d: -f6)
    USER_HOME=${USER_HOME:-/home/claude}
    mkdir -p "$USER_HOME/.claude"
    cp /tmp/host-claude-settings.json "$USER_HOME/.claude/.claude.json"
    chown "$USER_UID:$USER_GID" "$USER_HOME/.claude/.claude.json"
    chmod 600 "$USER_HOME/.claude/.claude.json"
    if [ -n "$CLAUDE_CONFIG_DIR" ] && [ -d "$CLAUDE_CONFIG_DIR" ]; then
        cp /tmp/host-claude-settings.json "$CLAUDE_CONFIG_DIR/.claude.json"
        chown "$USER_UID:$USER_GID" "$CLAUDE_CONFIG_DIR/.claude.json"
        chmod 600 "$CLAUDE_CONFIG_DIR/.claude.json"
    fi
fi

export SHELL=/bin/bash
exec su-exec "${USER_NAME}" "$@"
