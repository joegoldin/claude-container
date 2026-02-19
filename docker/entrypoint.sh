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

# Ensure config directory and its contents are accessible.
# Files may have been pre-seeded (e.g. .claude.json from a previous
# session) and owned by a different UID, so chown recursively.
if [ -d /claude ]; then
    chown -R "$USER_UID:$USER_GID" /claude 2>/dev/null || true
    chmod 755 /claude 2>/dev/null || true
fi

# Copy host Claude credentials if mounted (read-only at /mnt/claude-host).
# This allows using the host's existing authentication without re-login.
if [ -d /mnt/claude-host ]; then
    if [ -f /mnt/claude-host/.credentials.json ]; then
        cp /mnt/claude-host/.credentials.json /claude/.credentials.json
        chown "$USER_UID:$USER_GID" /claude/.credentials.json
        chmod 600 /claude/.credentials.json
    fi
    # Copy settings (.claude.json or settings.json).
    if [ -f /mnt/claude-host/settings.json ]; then
        cp /mnt/claude-host/settings.json /claude/settings.json
        chown "$USER_UID:$USER_GID" /claude/settings.json
        chmod 600 /claude/settings.json
    fi
    if [ -f /mnt/claude-host/.claude.json ]; then
        cp /mnt/claude-host/.claude.json /claude/.claude.json
        chown "$USER_UID:$USER_GID" /claude/.claude.json
        chmod 600 /claude/.claude.json
    fi
fi

if [ -d /workspace ]; then
    chmod 755 /workspace 2>/dev/null || true
fi

# Set HOME so Claude Code finds $HOME/.claude/ correctly.
USER_HOME=$(getent passwd "$USER_UID" | cut -d: -f6)
USER_HOME=${USER_HOME:-/home/claude}
export HOME="$USER_HOME"

export SHELL=/bin/bash
exec su-exec "${USER_NAME}" "$@"
