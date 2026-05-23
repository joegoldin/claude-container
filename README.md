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
# Drop-in safe replacement for `claude`: just cd in and run.
cd ~/code/my-project
claude-container

# Bare invocation creates a worktree at <repo>/.worktrees/<name>/ in a git
# repo, or a pwd-mounted session otherwise. All other commands are
# preserved.

# Fix a bug on an isolated branch, hands-free
claude-container work --yolo -p "fix the race condition in src/worker.go"

# Run three agents in parallel on separate features
claude-container work -b -p "add input validation to the signup form" &
claude-container work -b -p "write unit tests for the payment module" &
claude-container work -b -p "migrate the config from YAML to TOML" &

# One-shot task piped to a file
claude-container task -p "explain the authentication flow" > auth-docs.md

# Open the TUI dashboard (formerly the default bare-invoke behavior)
claude-container tui
```

## SYNOPSIS

```
claude-container [flags]                  # bare invoke: create + attach session in cwd
claude-container tui                      # TUI dashboard (formerly bare invoke)
claude-container run [flags]              # quick-start in current dir (pwd mount, no worktree)
claude-container work [flags]             # quick-start with a worktree
claude-container task [flags]             # run task, print result to stdout
claude-container new [flags]              # create session (wizard)
claude-container ps [--json]              # list sessions
claude-container attach <session>         # attach to session
claude-container stop <session>           # stop session
claude-container rm <session>             # remove session
claude-container logs [-f] <session>      # stream logs
claude-container extract <session>        # extract conversation (text/summary/resume)
claude-container build                    # load docker image
claude-container shell [workspace]        # debug shell
claude-container workspace add|list|show|rm  # manage named workspaces
claude-container auth                     # authenticate Claude Code
claude-container doctor                   # check system health
claude-container gc [--all] [--auth]      # garbage collect
```

## DESCRIPTION

A CLI tool for running multiple Claude Code instances in isolated, sandboxed
Docker containers with git worktree separation and a TUI dashboard.

Three isolation layers prevent agents from interfering with each other and
with the host:

- **Docker containers** -- rootless Docker provides OS-level sandboxing
  without requiring root privileges or changing file ownership
- **Git worktrees** -- each session gets its own branch and working copy
  (created inside the container from the mounted repository)
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
cd ~/code/my-project           # drop into your repo
claude-container               # bare invoke: creates a session in this dir
```

**Bare invoke** is the primary entry point. It feels like running `claude`
directly, except the session runs in an isolated, sandboxed Docker container
with a per-session HTTP proxy. In a git repo it creates a worktree at
`<repo>/.worktrees/<name>/`; otherwise it pwd-mounts the directory.

Run `claude-container tui` to open the dashboard (the old bare-invoke
behavior).

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
  gc          Clean up stopped containers and stale sessions
  logs        Stream logs from a session
  new         Create a new Claude Code session
  ps          List all sessions
  rm          Remove a session (stop + delete worktree + branch)
  run         Quick-start a session in the current directory
  shell       Drop into a bash shell in a container
  stop        Stop a session (keep worktree)
  task        Run a task and print the result to stdout
  tui         Open the dashboard
  work        Quick-start an isolated worktree session
  workspace   Manage named workspace definitions
```

### (bare invocation)

Bare `claude-container` (no subcommand) creates a sandboxed session in the
current directory and attaches to it — the drop-in safe replacement for
running `claude` directly. The flag set is the union of `run` and `work`.

In a git repo, a worktree is created at `<repo>/.worktrees/<name>/` and
the entry is added to `<repo>/.gitignore` if missing (the modification is
left in the working tree; commit when convenient). Outside a git repo,
the current directory is pwd-mounted as the workspace.

```
Usage:
  claude-container [flags]

Flags:
      --allow-command stringArray   shell command pattern to allow (repeatable)
      --allow-domain stringArray    domain the proxy should allow (repeatable)
      --allow-perm stringArray      raw permission rule to allow (repeatable)
  -b, --background                  run detached without attaching
      --deny-command stringArray    shell command pattern to deny (repeatable)
      --deny-path stringArray       filesystem path to deny (repeatable)
      --deny-perm stringArray       raw permission rule to deny (repeatable)
      --from string                 base branch/ref for the new worktree
  -w, --mount stringArray           extra host path to mount (repeatable)
      --name string                 session name (auto-generated if empty)
      --no-worktree                 pwd passthrough even in a git repo
      --packages stringArray        extra nixpkgs to install at start
      --preset string               proxy seed preset
      --profile string              sandbox profile (low|default|med|high)
  -p, --prompt string               initial prompt to send
      --proxy-port int              host port for the proxy dashboard
      --resume string               resume mode (picker, last, or session id)
      --rm                          remove container on exit
  -W, --workspace string            named workspace
      --yolo                        skip Claude Code permission prompts
```

On first run after upgrade, a one-line notice points users at
`claude-container tui` for the old dashboard behavior. Set
`CLAUDE_CONTAINER_QUIET=1` to suppress the notice.

### tui

Open the Bubble Tea dashboard (previously the default behavior of bare
`claude-container`). Lists sessions, opens the wizard for new ones, and
attaches into a running container.

```
Usage:
  claude-container tui [flags]
```

See **TUI DASHBOARD** below for key bindings.

### run

Create a session without a worktree, using the current directory. Name is
auto-generated (e.g. `myproject-calm-reef`) unless `--name` is given.

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
      --preset string               Seed proxy rules from a saved preset name
      --rm                          Auto-remove session when it exits
  -W, --workspace string            Named workspace from workspaces.json
      --yolo                        Skip permission prompts
```

### work

Create a session with its own git worktree for isolation. Name and
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
      --preset string               Seed proxy rules from a saved preset name
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
      --preset string               Seed proxy rules from a saved preset name
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
      --preset string               Seed proxy rules from a saved preset name
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

Stop a session. The branch is preserved for later resume via `attach`.

<!-- Generated from: claude-container stop --help -->

```
Usage:
  claude-container stop <session> [flags]
```

### rm

Remove a session completely: stops container, deletes branch, removes
session record.

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

Load the Docker image from the Nix-built tarball. This is done automatically
when creating sessions, but can be run manually to force a reload.

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

Log in to Claude Code by running an interactive authentication session
inside a container. If you have already authenticated Claude Code on the
host (credentials in `~/.claude/`), those credentials are automatically
mounted into containers -- no separate auth step needed.

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

### gc

Remove stopped containers, orphaned session records, and optionally
worktrees. By default, removes stopped containers and cleans up session
records whose containers no longer exist.

<!-- Generated from: claude-container gc --help -->

```
Usage:
  claude-container gc [flags]

Flags:
      --all    Also remove worktrees and branches for stopped sessions
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

Run `claude-container tui` to launch the dashboard. (Previously this was
the default behavior of bare `claude-container`; after the transparent-binary
refactor, bare invocation creates a session instead.)

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

Sandbox profiles (`--profile`) control Claude Code's internal permission rules.
Actual sandboxing is handled by rootless Docker (OS-level isolation) and the
network proxy (HTTP/HTTPS control), not by Claude's built-in sandbox.

Four built-in profiles:

    low        no permission prompts, unrestricted commands and filesystem
    default    same as low (yolo mode -- Docker + proxy enforce security)
    med        allow/deny lists enforced without prompts (dontAsk mode)
    high       restricted to /workspace, denies curl/wget, Anthropic API only

`--yolo` is equivalent to `--profile=low`.

Each profile also determines which domains are pre-allowed in the network
proxy (see **NETWORK PROXY** below).

### Customizing profiles

```sh
# Add allowed commands
claude-container run --allow-command='docker *' --allow-command='kubectl *'

# Deny specific commands
claude-container run --deny-command='rm -rf *'

# Deny read access to specific paths
claude-container run --deny-path='/etc/passwd'

# Add allowed domains to the proxy
claude-container run --profile=high --allow-domain=github.com
```

## NETWORK PROXY

Every session runs behind its own per-session HTTP/HTTPS proxy sidecar
(mitmproxy in transparent mode). The proxy intercepts ALL outbound TCP
traffic — not just HTTP — and enforces allow/deny rules. Unknown
requests are held until you approve or deny them via a web dashboard.

### How it works

1. A per-session proxy container starts on a dedicated Docker network
2. The Claude container joins that container's network namespace via
   `--network container:claude-proxy_<session>` — it has no NIC of its own
3. The proxy entrypoint installs an nftables ruleset in the shared netns:
   default-deny on output; REDIRECT every TCP connection (from any uid
   other than mitmproxy) to mitmproxy's transparent listener; drop QUIC
4. mitmproxy reads `SO_ORIGINAL_DST` to recover the real destination,
   MITMs HTTPS using its CA cert, and falls back to raw TCP for non-HTTP
   protocols (SSH, raw TCP, etc.)
5. HTTP rules match URLs; raw TCP rules match a synthetic `tcp://host:port`
6. Unknown flows are held; the dashboard shows them with browser
   notifications and you approve or deny them

`HTTP_PROXY` / `HTTPS_PROXY` env vars are NOT set inside the container.
Transparent mode makes them redundant and removes the only client opt-in
that previously allowed bypass. There is no escape hatch: every TCP
packet a process in the Claude container emits hits the nftables rule
first.

### Outbound UDP (Phase 2)

UDP packets from inside the container are also gated by the rule store —
the path uses NFQUEUE instead of mitmproxy because UDP has no
`SO_ORIGINAL_DST`. A small Go daemon (`udp-redir`) inside the proxy
container reads every outbound UDP datagram from netfilter, parses the
IP/UDP headers (and the DNS question for queries to UDP/53), and either:

- **ACCEPT** if the destination matches a UDP or `proto=any` allow rule
- **DROP** if a deny rule matches
- **HOLD** the packet in the kernel queue (up to 16 per `(dst, port)`
  tuple, 30-second TTL) and surface it on the dashboard's pending list
  alongside HTTP/TCP flows. When you approve, the held packet is
  released to the wire.

DNS queries display the queried hostname in the pending UI so you can
make an informed decision. DNS via the docker embedded resolver
(`127.0.0.11`) bypasses NFQUEUE entirely (it's loopback), so libc-based
`getaddrinfo` keeps working without prompting.

UDP/443 (QUIC) is dropped at the firewall before NFQUEUE — clients fall
back to TCP, which mitmproxy already gates.

### Default allowed domains by profile

The sandbox profile (`--profile`) determines which domains are pre-allowed
in the proxy when a session starts:

**low / default** -- all traffic allowed (proxy rule: `.*`)

**med** -- common development infrastructure:

    api.anthropic.com         statsig.anthropic.com     sentry.io
    github.com                *.github.com              *.npmjs.org
    registry.npmjs.org        registry.yarnpkg.com      pypi.org
    *.pypi.org                files.pythonhosted.org

**high** -- Anthropic API only:

    api.anthropic.com

Any domain not in the list is held by the proxy until you approve it.
Use `--allow-domain` to pre-allow additional domains:

```sh
claude-container run --profile=high --allow-domain=github.com --allow-domain=npmjs.org
```

### Per-session proxies and presets

Each session owns its own proxy sidecar, network namespace, and rules
file. They are not shared. When the session is removed (`claude-container
rm`), the proxy and its state are torn down with it.

The optional `--preset <name>` flag seeds a new session's proxy with
rules from a saved preset file. Sandbox-profile-derived rules are then
layered on top, so a preset and the profile defaults coexist.

```sh
# Start with a saved preset of allow/deny rules
claude-container new --preset work

# No preset — sandbox profile defaults only
claude-container run --profile=high
```

    --preset string             Seed proxy rules from a saved preset name
    --proxy-port int            Dashboard port on host (0 = auto-assign)

Preset files live at:

    ~/.config/claude-container/proxy-presets/<name>.json

You can save the live rules of any running session as a preset by
clicking **Export** in the dashboard's Rules tab — the file downloads
through the browser, and you drop it into `proxy-presets/` to make it
available as a `--preset` argument. **Import** in the same tab uploads a
JSON file and replaces the current session's rules with its contents
(replace-only; no merge in v1, no undo).

The mitmproxy CA cert is shared across all per-session proxies so
Claude containers don't need to re-trust a new cert per session:

    ~/.config/claude-container/proxy-shared/ca/mitmproxy-ca-cert.pem

Per-session live state lives at:

    ~/.config/claude-container/proxy-state/<session>/rules.json
    ~/.config/claude-container/proxy-state/<session>/dashboard-token

### Inbound port publishing

Each session reserves a contiguous host port range for inbound traffic
(default: 10 ports starting at 30000). The dashboard's **Published
Ports** tab lets you map a container port to a host port from that
range without restarting the session.

The reserved range is `127.0.0.1`-bound only — never reachable from
your LAN. Dev servers inside the container must bind to `0.0.0.0:PORT`
(not `127.0.0.1:PORT`) for the docker port forward to reach them.

Flags:

    --publish-range int         ports per session reserved for inbound publish (default 10)
    --publish-base int          first host port the inbound publish pool may use (default 30000)
    --publish-pool-size int     size of the inbound publish pool in ports (default 1000)

By default the pool is `30000-30999` (1000 ports), so up to 100
concurrent sessions of size 10 fit without collision. Allocation is
first-fit across a per-host JSON ledger at
`~/.config/claude-container/published-port-allocations.json`. Override
`--publish-base` if your firewall reserves the default range for
something else.

To publish a port: open the dashboard, go to **Published Ports**, enter
the container port + protocol (TCP or UDP), optionally a label, and
click Publish. The dashboard returns an assigned host port and shows
`http://127.0.0.1:<port>` you can open from your host browser. Click
**Unpublish** to close it. The proxy container's nftables firewall is
mutated live — no session restart needed.

## EXTRACTING CONVERSATIONS

`claude-container extract <session>` pulls a Claude Code conversation
out of a session into a file on the host. Three output formats:

    --format text      Plain markdown of the conversation. Rendered
                       locally by parsing the JSONL transcript; no LLM
                       involved. ANSI escapes in tool output are
                       stripped. Safe by default.
    --format summary   Short bulleted summary generated by a one-shot
                       claude running inside the session's container
                       (`docker exec ... claude -p --resume <id>`). Any
                       prompt injection in the transcript stays sandboxed
                       by the same proxy and permissions as the original
                       session.
    --format resume    Raw JSONL copy. HIGHEST RISK: resuming on the
                       host loads every prior tool output (web fetches,
                       file reads, command results) as prior context
                       into the host claude session. The command prompts
                       before writing and prints exact commands for
                       dropping the file into `~/.claude/projects/` so
                       `claude --resume` outside the container picks it
                       up. Pass --force to skip the prompt.

```sh
# Plain markdown to stdout
claude-container extract my-session

# Markdown to a file, including assistant thinking
claude-container extract my-session --output convo.md --include-thinking

# LLM-generated summary (requires the session container to be running)
claude-container extract my-session --format summary --output summary.md

# Raw JSONL for resume on host (reads warning, then prompts)
claude-container extract my-session --format resume --output convo.jsonl
```

The transcript file is already on your host at
`~/.config/claude-container/claude-config/projects/<encoded-cwd>/<id>.jsonl`;
the `extract` command just gives you a safer, format-aware way to get at
it without digging through encoded paths. The resume mode's risk warning
is the only gate between container tool output and your host model.

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

When a worktree session is started, the host repository is mounted
read-write into the container and `git worktree add` runs inside the
entrypoint to create the working copy at `/workspace`. This avoids
broken `.git` path references that would occur if the worktree were
created on the host. For multi-repo workspaces, each repository is
mounted separately and gets its own worktree under `/workspace/<name>`.

## DEPENDENCIES

Runtime: `docker`.

The nix package wraps the binary with `git` and `docker` on PATH and sets
`CLAUDE_CONTAINER_IMAGE_TARBALL` to the Nix-built image tarball.

## FILES

    ~/.config/claude-container/sessions.json        session metadata
    ~/.config/claude-container/workspaces.json        named workspace definitions
    ~/.config/claude-container/claude-config/        shared Claude Code config
    ~/.config/claude-container/claude-config/.credentials.json   auth credentials
    ~/.config/claude-container/loaded-image          image load marker
    ~/.config/claude-container/proxy-presets/<name>.json
                                                     saved rule presets (load with --preset)
    ~/.config/claude-container/proxy-state/<session>/
                                                     per-session live rules + dashboard token
    ~/.config/claude-container/proxy-shared/ca/      mitmproxy CA cert (shared across sessions)

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
claude-container tui

# Create a session with interactive wizard
claude-container new

# Create a session with flags (skip wizard)
claude-container new --name auth --worktree feature-auth

# Multi-folder workspace
claude-container workspace add my-work ~/code/repo-a ~/code/repo-b
claude-container run -W my-work

# Multi-repo workspace with worktrees (each repo gets its own branch)
claude-container workspace add my-repos ~/code/repo-a ~/code/repo-b
claude-container work -W my-repos

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

# Stop (branch preserved)
claude-container stop auth

# Resume a stopped session
claude-container attach auth

# Remove completely
claude-container rm auth

# Force reload the Docker image
claude-container build

# Clean up stopped containers
claude-container gc

# Clean up everything (containers + worktrees + session records)
claude-container gc --all

# Log out (remove credentials)
claude-container gc --auth

# Run with proxy seeded from a saved preset
claude-container run --preset=work

# Each session gets its own proxy; save a preset by clicking Export in the
# dashboard, then load it into a future session with --preset.
claude-container run --preset=work -p "another task"
```
