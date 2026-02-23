> **Disclaimer:** This software is provided "as is", without warranty of any
> kind. It is experimental, untested, non-production-ready code built with the
> assistance of LLMs (large language models). Use at your own risk. The
> author(s) accept no liability for any damage, data loss, or other issues
> arising from its use. See [LICENSE](LICENSE) for details.

# CLAUDE-CONTAINER(1)

## NAME

claude-container - run multiple Claude Code instances in isolated containers

## QUICK START

```sh
# Fix a bug on an isolated branch, hands-free
claude-container work --yolo -p "fix the race condition in src/worker.go"

# Run three agents in parallel on separate features
claude-container work -b -p "add input validation to the signup form" &
claude-container work -b -p "write unit tests for the payment module" &
claude-container work -b -p "migrate the config from YAML to TOML" &

# One-shot task piped to a file
claude-container task -p "explain the authentication flow" > auth-docs.md

# Interactive session with network proxy for approval-based internet access
claude-container run --proxy-profile=work --profile=med
```

## SYNOPSIS

```
claude-container                          # TUI dashboard
claude-container run [flags]              # quick-start in current dir
claude-container work [flags]             # quick-start with worktree
claude-container task [flags]             # run task, print result to stdout
claude-container new [flags]              # create session (wizard)
claude-container ps [--json]              # list sessions
claude-container attach <session>         # attach to session
claude-container stop <session>           # stop session
claude-container rm <session>             # remove session
claude-container logs [-f] <session>      # stream logs
claude-container build                    # load docker image
claude-container shell [workspace]        # debug shell
claude-container workspace add|list|show|rm  # manage named workspaces
claude-container auth                     # authenticate Claude Code
claude-container doctor                   # check system health
claude-container gc [--all] [--auth]      # garbage collect
claude-container fix-perms <session>      # fix workspace ownership
```

## DESCRIPTION

A CLI tool for running multiple Claude Code instances in isolated, sandboxed
Docker containers with git worktree separation and a TUI dashboard.

Three isolation layers prevent agents from interfering with each other and
with the host:

- **Docker containers** -- rootless Docker provides OS-level sandboxing
  without requiring root privileges or changing file ownership
- **Git worktrees** -- each session gets its own branch and working directory
- **Network proxy** -- optional HTTP/HTTPS proxy sidecar with per-domain
  allow/deny rules and a web dashboard for interactive approval

All sessions share a single Claude config directory for authentication. Run
`claude-container auth` once and all sessions use those credentials.

The Docker image is built entirely with Nix via `dockerTools.buildLayeredImage`.
No Dockerfile is involved -- the image is a Nix derivation that includes Claude
Code, common Unix tools, and baked-in sandbox settings.

## GETTING STARTED

```sh
claude-container auth          # authenticate (once, or use host ~/.claude/)
claude-container doctor        # verify everything is set up
claude-container run --yolo    # quick-start an interactive session
```

The Docker image is loaded automatically on first use from the Nix-built
tarball. No manual `build` step is required.

If you have already authenticated Claude Code on the host (credentials in
`~/.claude/`), those credentials are automatically mounted into containers --
no separate auth step needed.

## COMMANDS

<!-- Generated from: claude-container --help -->

```
Available Commands:
  attach      Attach to a running session
  auth        Authenticate Claude Code inside a container
  build       Load the Claude Code Docker image
  completion  Generate the autocompletion script for the specified shell
  doctor      Check system health and configuration
  fix-perms   Fix workspace ownership after container UID remapping
  gc          Clean up stopped containers and stale sessions
  logs        Stream logs from a session
  new         Create a new Claude Code session
  ps          List all sessions
  rm          Remove a session (stop + delete worktree + branch)
  run         Quick-start a session in the current directory
  shell       Drop into a bash shell in a container
  stop        Stop a session (keep worktree)
  task        Run a task and print the result to stdout
  work        Quick-start an isolated worktree session
  workspace   Manage named workspace definitions
```

### run

Quick-start a session in the current directory. No worktree, no wizard.
Name is auto-generated (e.g. `myproject-calm-reef`) unless `--name` is given.

<!-- Generated from: claude-container run --help -->

```
Usage:
  claude-container run [flags]

Flags:
      --allow-command stringArray   Add command pattern to allow list (e.g., 'docker *')
      --allow-domain stringArray    Add domain to proxy allowlist
  -b, --background                  Don't attach after creation
      --deny-command stringArray    Add command pattern to deny list (e.g., 'rm -rf *')
      --deny-path stringArray       Add path to permissions deny list
  -w, --mount stringArray           Additional folders to mount (repeatable)
      --name string                 Session name (auto-generated if omitted)
      --profile string              Sandbox profile: low, default, med, high (default "default")
  -p, --prompt string               Initial prompt to send to Claude
      --proxy-port int              Dashboard port on host (0 = auto-assign)
      --proxy-profile string        Proxy rule profile name (default "default")
      --rm                          Auto-remove session when it exits
  -W, --workspace string            Named workspace from workspaces.json
      --yolo                        Skip permission prompts
```

### work

Quick-start a session with its own git worktree for isolation. Name and
branch are auto-generated unless `--name` is given.

<!-- Generated from: claude-container work --help -->

```
Usage:
  claude-container work [flags]

Flags:
      --allow-command stringArray   Add command pattern to allow list (e.g., 'docker *')
      --allow-domain stringArray    Add domain to proxy allowlist
  -b, --background                  Don't attach after creation
      --deny-command stringArray    Add command pattern to deny list (e.g., 'rm -rf *')
      --deny-path stringArray       Add path to permissions deny list
      --from string                 Base branch for worktree (default: current HEAD)
  -w, --mount stringArray           Additional folders to mount (repeatable)
      --name string                 Session name (auto-generated if omitted)
      --profile string              Sandbox profile: low, default, med, high (default "default")
  -p, --prompt string               Initial prompt to send to Claude
      --proxy-port int              Dashboard port on host (0 = auto-assign)
      --proxy-profile string        Proxy rule profile name (default "default")
      --rm                          Auto-remove session when it exits
  -W, --workspace string            Named workspace from workspaces.json
      --yolo                        Skip permission prompts
```

### task

Run Claude non-interactively. Final output goes to stdout (pipeable).
Summary (changed files, duration, tokens) goes to stderr.

<!-- Generated from: claude-container task --help -->

```
Usage:
  claude-container task [flags]

Flags:
      --allow-command stringArray   Add command pattern to allow list
      --allow-domain stringArray    Add domain to proxy allowlist
      --deny-command stringArray    Add command pattern to deny list
      --deny-path stringArray       Add path to permissions deny list
      --keep                        Keep session after completion (default: ephemeral)
      --max-turns int               Max agentic turns (passed to Claude CLI)
      --model string                Model to use (passed to Claude CLI)
  -w, --mount stringArray           Additional folders to mount
      --name string                 Session name (auto-generated if omitted)
      --profile string              Sandbox profile: low, default, med, high (default "default")
  -p, --prompt string               Task prompt (required)
      --proxy-port int              Dashboard port on host (0 = auto-assign)
      --proxy-profile string        Proxy rule profile name (default "default")
  -W, --workspace string            Named workspace from workspaces.json
```

Sessions are ephemeral by default -- the container and session record are
removed after Claude finishes. Use `--keep` to persist the session for
later inspection with `attach` or `logs`.

### new

Create a new session with an interactive wizard, or use flags to skip the
wizard.

<!-- Generated from: claude-container new --help -->

```
Usage:
  claude-container new [flags]

Flags:
      --allow-command stringArray   Add command pattern to allow list (e.g., 'docker *')
      --allow-domain stringArray    Add domain to proxy allowlist
  -b, --background                  Don't attach after creation
  -c, --continue                    Resume previous conversation
      --deny-command stringArray    Add command pattern to deny list (e.g., 'rm -rf *')
      --deny-path stringArray       Add path to permissions deny list
      --from string                 Base branch for worktree (default: current HEAD)
  -w, --mount stringArray           Additional folders to mount (repeatable)
      --name string                 Session name
      --no-worktree                 Use current directory directly
      --profile string              Sandbox profile: low, default, med, high (default "default")
  -p, --prompt string               Initial prompt to send to Claude
      --proxy-port int              Dashboard port on host (0 = auto-assign)
      --proxy-profile string        Proxy rule profile name (default "default")
      --rm                          Auto-remove session when it exits
  -W, --workspace string            Named workspace from workspaces.json
      --worktree string             Create worktree on new branch
      --yolo                        Skip permission prompts
```

The interactive wizard asks:

1. Session name
2. Worktree setup (new branch / from existing branch / no worktree)
3. Mode (normal / yolo)
4. Initial prompt (optional)

### ps

List all sessions with live status.

<!-- Generated from: claude-container ps --help -->

```
Usage:
  claude-container ps [flags]

Flags:
      --json   Machine-readable JSON output
```

Output columns: NAME, BRANCH, STATUS, UPTIME, REPO.

### attach

Attach to a running session. If the session is stopped, restarts it with
`claude --continue` to resume the previous conversation.

<!-- Generated from: claude-container attach --help -->

```
Usage:
  claude-container attach <session> [flags]

Flags:
  -b, --background   Start container in background without attaching
  -d, --dashboard    Start container then open the TUI dashboard
```

See **KEY BINDINGS** below for detach/quit controls.

### stop

Stop a session. The git worktree and branch are preserved for later resume
via `attach`.

<!-- Generated from: claude-container stop --help -->

```
Usage:
  claude-container stop <session> [flags]
```

### rm

Remove a session completely: stops container, removes worktree, deletes
branch, removes session record.

<!-- Generated from: claude-container rm --help -->

```
Usage:
  claude-container rm <session> [flags]
```

### logs

Stream logs from a session's docker container.

<!-- Generated from: claude-container logs --help -->

```
Usage:
  claude-container logs <session> [flags]

Flags:
  -f, --follow   Stream logs continuously
```

### build

Load the Nix-built Docker image into Docker. This is done automatically
when starting sessions, but can be run manually to force a reload.

<!-- Generated from: claude-container build --help -->

```
Usage:
  claude-container build [flags]
```

### shell

Drop into a bash shell inside an ephemeral container for debugging.

<!-- Generated from: claude-container shell --help -->

```
Usage:
  claude-container shell [workspace] [flags]
```

### workspace

Manage named workspace definitions (collections of folder paths).

```
Usage:
  claude-container workspace [command]

Available Commands:
  add         Create or append paths to a named workspace
  list        List all workspace names
  show        Show paths in a workspace
  rm          Remove a workspace definition
```

### auth

Authenticate Claude Code inside a container. Runs an interactive session
where you complete the Claude Code login flow. Credentials are stored in the
shared config directory and used by all sessions.

Auto-exits once authentication succeeds.

<!-- Generated from: claude-container auth --help -->

```
Usage:
  claude-container auth [flags]
  claude-container auth [command]

Available Commands:
  status      Check authentication status
```

Check auth state: `claude-container auth status`

Remove credentials: `claude-container gc --auth`

### doctor

Check system health and configuration. Verifies Docker is available and
running, the container image is loaded, the config directory exists, and
authentication is set up.

<!-- Generated from: claude-container doctor --help -->

```
Usage:
  claude-container doctor [flags]
```

### fix-perms

Fix workspace file ownership after container UID remapping. Runs
`sudo chown -R` to restore ownership to the current user.

<!-- Generated from: claude-container fix-perms --help -->

```
Usage:
  claude-container fix-perms <session> [flags]
```

### gc

Clean up stopped containers and stale sessions.

<!-- Generated from: claude-container gc --help -->

```
Usage:
  claude-container gc [flags]

Flags:
      --all    Also remove worktrees, branches, and session records
      --auth   Remove shared Claude config directory (logs you out)
```

## KEY BINDINGS

When attached to a session, a status bar is displayed on the last terminal
row showing the session name, branch, mode, and available key bindings.

All commands use a **Ctrl+B** prefix (tmux-style):

    Ctrl+B d       Detach from session (session keeps running)
    Ctrl+B q       Quit session (stop container; remove if --rm)
    Ctrl+B Ctrl+B  Send literal Ctrl+B to the container

The prefix key has a 200ms timeout. If no command key is pressed within
that window, the prefix is cancelled.

Ctrl+C is forwarded directly to the container (used by Claude Code to
cancel operations).

## TUI DASHBOARD

Run `claude-container` with no arguments to launch the dashboard.

```
+-----------------------------+------------------------------+
|  Sessions                   |  Preview                     |
|                             |                              |
|  > feature-auth  [running]  |  > Analyzing auth module...  |
|    fix-payments  [running]  |  > Reading src/auth.ts       |
|    refactor-db   [stopped]  |  > ...                       |
+-----------------------------+------------------------------+
| n:new  enter:attach  d:stop  x:rm  tab:diff  q:quit       |
+------------------------------------------------------------+
```

Key bindings:

    j/k, arrows    navigate session list
    enter          attach to selected session
    n              new session (wizard)
    d              stop selected session
    x              remove selected session
    tab            toggle live preview / git diff view
    q              quit dashboard (sessions keep running)

## CONFIGURATION

Session state and authentication are stored at `$XDG_CONFIG_HOME/claude-container/`
(default `~/.config/claude-container/`).

## SANDBOX PROFILES

Four built-in profiles control Claude's permission rules:

    low        no permission prompts, unrestricted commands and filesystem
    default    same as low (sandbox enforced by Docker + proxy, not Claude)
    med        allow/deny lists, no interactive prompts (dontAsk mode)
    high       restricted to /workspace, denies curl/wget, Anthropic API only

Use `--profile` to select, `--allow-domain`, `--deny-path`, `--allow-command`,
and `--deny-command` to customize:

```sh
claude-container run --profile=high --allow-domain=github.com
claude-container work -w ~/code/a --profile=low
claude-container run --allow-command='docker *' --deny-command='rm -rf *'
```

`--yolo` is equivalent to `--profile=low`.

## NETWORK PROXY

An HTTP/HTTPS proxy sidecar can sit between Claude and the internet, providing
interactive network access control with a web dashboard.

### Usage

```sh
# Start with proxy — dashboard at http://localhost:<auto-port>
claude-container run --proxy-profile=work

# Custom dashboard port
claude-container run --proxy-profile=work --proxy-port=9090
```

When unknown domains are accessed, the proxy holds the connection and notifies
you via the web dashboard (and browser notifications). You can allow or deny
with pattern-based rules that persist across sessions.

### Flags

    --proxy-profile string      Rule profile name (default "default")
    --proxy-port int            Dashboard port on host (0 = auto-assign)

### Proxy reuse

Proxy containers are named by profile, not by session. Multiple sessions with
the same `--proxy-profile` share a single proxy process. Rules added in one
session are immediately visible to all sessions sharing that profile. The proxy
is stopped only when the last session using it is removed.

## ENVIRONMENT

    CLAUDE_CONTAINER_IMAGE_TARBALL           path to Nix-built OCI image tarball
                                             (set by nix wrapper)
    CLAUDE_CONTAINER_IMAGE_TAG               docker image tag (set by nix wrapper)
    CLAUDE_PROXY_IMAGE_TARBALL               path to Nix-built proxy image tarball
                                             (set by nix wrapper)
    CLAUDE_PROXY_IMAGE_TAG                   proxy docker image tag (set by nix wrapper)
    CLAUDE_CONTAINER_EXTRA_ALLOW_COMMANDS    JSON array of command patterns to allow
                                             (set by nix wrapper from extraPackages)
    XDG_CONFIG_HOME                          base config directory

## INSTALL

### Nix flake (basic)

```nix
# flake input
claude-container = {
  url = "github:joegoldin/claude-container";
  inputs.nixpkgs.follows = "nixpkgs";
};

# overlay
claude-container-packages = inputs.claude-container.overlays.default;

# then add to packages
home.packages = [ pkgs.claude-container ];
```

### Nix flake (custom image)

Use `lib.mkClaudeContainer` to customize the Docker image with extra
packages, settings, or managed settings:

```nix
claude-container = {
  url = "github:joegoldin/claude-container";
  inputs.nixpkgs.follows = "nixpkgs";
};

# in your packages or overlay
let
  cc = inputs.claude-container.lib.mkClaudeContainer {
    inherit pkgs;
    claude-code = pkgs.llm-agents.claude-code;
    extraPackages = with pkgs; [ ripgrep fd nodejs ];
    extraAllowCommands = [ "docker *" "kubectl *" ];  # optional
    settings = { /* settings.json content */ };
  };
in {
  home.packages = [ cc.cli ];
}
```

Commands from `extraPackages` are automatically derived (using `meta.mainProgram`
or `pname`) and added to the sandbox allow list. Use `extraAllowCommands` for
additional patterns that aren't covered by a package.

### Build from source

```sh
nix build
# or
nix develop --command go build -o claude-container .
```

## DOCKER IMAGE

The Docker image is built with Nix (`dockerTools.buildLayeredImage`) and
contains no Alpine or Debian base -- only Nix store paths.

Default tools included in the image:

    bash, coreutils, git, jq, bubblewrap, socat, curl, findutils,
    grep, sed, awk, ripgrep, fd, tree, diffutils, tar, gzip,
    less, file, which, python3

Additional packages can be added via the `extraPackages` option in
`lib.mkClaudeContainer`.

The entrypoint handles UID/GID mapping via shadow utilities and su-exec.
In rootless Docker, the container runs as root (which maps to the host
user's UID) so mounted volumes keep their original ownership. In standard
Docker, a non-root user matching the host UID is created at startup.

## DEPENDENCIES

Runtime: `docker`.

The nix package wraps the binary with `git` and `docker` on PATH and sets
`CLAUDE_CONTAINER_IMAGE_TARBALL` to the Nix-built image tarball.

## FILES

    ~/.config/claude-container/sessions.json        session metadata
    ~/.config/claude-container/workspaces.json        named workspace definitions
    ~/.config/claude-container/worktrees/            git worktrees
    ~/.config/claude-container/claude-config/        shared Claude Code config
    ~/.config/claude-container/claude-config/.credentials.json   auth credentials
    ~/.config/claude-container/loaded-image          image load marker
    ~/.config/claude-container/proxy-profiles/         proxy rule profiles
    ~/.config/claude-container/proxy-profiles/ca/      mitmproxy CA certificates

## EXAMPLES

```sh
# First-time setup
claude-container auth
claude-container doctor

# Quick-start in current directory (auto-generated name)
claude-container run

# Quick-start in yolo mode with a prompt
claude-container run --yolo -p "fix the login bug"

# Ephemeral session (auto-removed when Claude exits)
claude-container run --rm --yolo -p "explain this codebase"

# Start in background, attach later
claude-container run -b --name my-task -p "refactor auth module"
claude-container attach my-task

# Start an isolated worktree session
claude-container work

# Worktree session from a specific branch
claude-container work --from release-2.0

# Run a one-shot task, pipe output
claude-container task -p "fix the failing tests"

# Task with model and max turns
claude-container task -p "add input validation" --model sonnet --max-turns 10

# Task output piped to a file
claude-container task -p "explain the auth module" > explanation.md

# Keep task session for inspection
claude-container task -p "refactor auth" --keep
claude-container attach myproject-calm-reef

# Launch the TUI dashboard
claude-container

# Create a session with interactive wizard
claude-container new

# Create a session with flags (skip wizard)
claude-container new --name auth --worktree feature-auth

# Multi-folder workspace
claude-container workspace add my-work ~/code/repo-a ~/code/repo-b
claude-container run -W my-work

# Ad-hoc multi-folder mount
claude-container run -w ~/code/repo-a -w ~/code/repo-b

# High security profile
claude-container run --profile=high --allow-domain=github.com

# Low security (same as --yolo)
claude-container run --profile=low

# Allow extra commands in the sandbox
claude-container run --allow-command='docker *' --allow-command='kubectl *'

# List sessions
claude-container ps
claude-container ps --json

# Attach (Ctrl+B d to detach, Ctrl+B q to quit)
claude-container attach auth

# Stream logs
claude-container logs -f auth

# Stop (worktree preserved)
claude-container stop auth

# Resume a stopped session
claude-container attach auth

# Remove completely
claude-container rm auth

# Fix file ownership after UID remapping
claude-container fix-perms auth

# Force reload the Docker image
claude-container build

# Clean up stopped containers
claude-container gc

# Clean up everything (containers + worktrees + session records)
claude-container gc --all

# Log out (remove credentials)
claude-container gc --auth

# Run with proxy-based network control
claude-container run --proxy-profile=work

# Share proxy rules across sessions
claude-container run --proxy-profile=work
claude-container run --proxy-profile=work -p "another task"
```
