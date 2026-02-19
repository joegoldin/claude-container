# Nix-Based Docker Image Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the Alpine/npm Dockerfile with a Nix-built Docker image via `dockerTools.buildLayeredImage`, and expose `lib.mkClaudeContainer` for consumer customization.

**Architecture:** The flake exposes `lib.mkClaudeContainer` which accepts `claude-code`, `settings`, `managedSettings`, and `extraPackages`. It builds a Docker image via `dockerTools.buildLayeredImage` and wraps the Go CLI binary with image references. The Go binary uses `docker load` (not `docker build`) to load the Nix-built tarball. Auth flow is unchanged.

**Tech Stack:** Nix (dockerTools, buildLayeredImage, writeShellScript), Go (cobra CLI), llm-agents.nix (claude-code package)

---

### Task 1: Create Nix image module files

**Files:**
- Create: `nix/managed-settings.nix`
- Create: `nix/image.nix`

**Step 1: Create `nix/managed-settings.nix`**

This extracts the current `docker/managed-settings.json` into a Nix expression.

```nix
# nix/managed-settings.nix
{
  sandbox = {
    enabled = true;
    autoAllowBashIfSandboxed = true;
    enableWeakerNestedSandbox = true;
    allowUnsandboxedCommands = false;
    excludedCommands = [ "git" ];
    network.allowedDomains = [
      "api.anthropic.com"
      "statsig.anthropic.com"
      "sentry.io"
      "github.com"
      "*.github.com"
      "*.npmjs.org"
      "registry.npmjs.org"
      "registry.yarnpkg.com"
      "pypi.org"
      "*.pypi.org"
      "files.pythonhosted.org"
    ];
  };
  permissions.deny = [
    "Read(/etc/shadow)"
    "Read(/etc/passwd)"
    "Read(~/.ssh/**)"
    "Read(~/.aws/**)"
    "Read(~/.gnupg/**)"
  ];
}
```

**Step 2: Create `nix/image.nix`**

This is the core: entrypoint script + `dockerTools.buildLayeredImage`.

```nix
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
  getent = "${pkgs.glibc.bin}/bin/getent";
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
```

**Step 3: Verify files parse**

Run: `cd /home/joe/Development/claude-container && nix eval --raw '(builtins.toJSON (import ./nix/managed-settings.nix))' 2>&1 | head -1`
Expected: JSON output of the managed settings

**Step 4: Commit**

```bash
git add nix/managed-settings.nix nix/image.nix
git commit -m "feat: add Nix image module (managed-settings + dockerTools image)"
```

---

### Task 2: Update flake.nix

**Files:**
- Modify: `flake.nix`

**Step 1: Replace flake.nix with new structure**

Key changes:
- Add `llm-agents` input
- Add `lib.mkClaudeContainer` function
- Split Go build into `claude-container-unwrapped` (no image-specific wrapping)
- Default packages use `mkClaudeContainer` with `llm-agents.claude-code`
- Remove `docker` context copy + `CLAUDE_CONTAINER_DOCKER_CONTEXT`

```nix
{
  description = "Run multiple Claude Code instances in isolated containers";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    llm-agents = {
      url = "github:numtide/llm-agents.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      llm-agents,
    }:
    {
      lib.mkClaudeContainer =
        {
          pkgs,
          claude-code,
          settings ? { },
          managedSettings ? import ./nix/managed-settings.nix,
          extraPackages ? [ ],
        }:
        let
          system = pkgs.stdenv.hostPlatform.system;

          image = pkgs.callPackage ./nix/image.nix {
            inherit
              claude-code
              settings
              managedSettings
              extraPackages
              ;
          };

          cli = pkgs.symlinkJoin {
            name = "claude-container";
            paths = [ self.packages.${system}.claude-container-unwrapped ];
            nativeBuildInputs = [ pkgs.makeWrapper ];
            postBuild = ''
              wrapProgram $out/bin/claude-container \
                --prefix PATH : ${
                  pkgs.lib.makeBinPath (
                    with pkgs;
                    [
                      git
                      docker
                    ]
                  )
                } \
                --set CLAUDE_CONTAINER_IMAGE_TARBALL "${image}" \
                --set CLAUDE_CONTAINER_IMAGE_TAG "claude-code:nix"

              # Create yacc alias pointing to wrapped binary
              ln -sf $out/bin/claude-container $out/bin/yacc
            '';
          };
        in
        { inherit image cli; };
    }
    // flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ llm-agents.overlays.default ];
        };
        vendorHash = "sha256-t6Hbr5aArRL70MotJuhZZL0JEmXZztTbevzMayHDXcs=";

        defaultContainer = self.lib.mkClaudeContainer {
          inherit pkgs;
          claude-code = pkgs.llm-agents.claude-code;
        };
      in
      {
        packages.default = defaultContainer.cli;
        packages.claude-container = defaultContainer.cli;
        packages.claude-container-image = defaultContainer.image;

        packages.claude-container-unwrapped = pkgs.buildGoModule {
          pname = "claude-container";
          version = "0.1.0";
          src = ./.;
          inherit vendorHash;
          doCheck = false;

          nativeBuildInputs = with pkgs; [ installShellFiles ];

          postInstall = ''
            # Generate shell completions
            $out/bin/claude-container completion bash > claude-container.bash
            $out/bin/claude-container completion fish > claude-container.fish
            $out/bin/claude-container completion zsh > _claude-container
            installShellCompletion claude-container.{bash,fish} _claude-container
          '';

          meta = with pkgs.lib; {
            description = "Run multiple Claude Code instances in isolated containers";
            homepage = "https://github.com/joegoldin/claude-container";
            license = licenses.mit;
            mainProgram = "claude-container";
          };
        };

        checks.default = pkgs.buildGoModule {
          pname = "claude-container-tests";
          version = "0.1.0";
          src = ./.;
          inherit vendorHash;
          nativeBuildInputs = [ pkgs.git ];
          doCheck = true;
          preCheck = ''
            export HOME=/tmp/claude-container-test-home
            mkdir -p $HOME
            git config --global user.email "test@test.com"
            git config --global user.name "Test"
            git config --global init.defaultBranch main
          '';
          installPhase = ''
            touch $out
          '';
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            git
            docker
          ];
        };
      }
    )
    // {
      overlays.default = final: prev: {
        claude-container = self.packages.${prev.system}.claude-container;
      };
    };
}
```

**Step 2: Update flake.lock**

Run: `cd /home/joe/Development/claude-container && nix flake lock --update-input llm-agents`
Expected: `flake.lock` updated with `llm-agents` input

**Step 3: Commit**

```bash
git add flake.nix flake.lock
git commit -m "feat: add lib.mkClaudeContainer and llm-agents input"
```

---

### Task 3: Update Go docker package

**Files:**
- Modify: `internal/docker/docker.go:36-44,46-92,96-115,126-132`
- Modify: `internal/docker/docker_test.go`

**Step 1: Update `internal/docker/docker.go`**

Changes:
- Remove `BuildArgs()` and `Build()` functions
- Add `ImageTag()`, `LoadImage()`, `EnsureImage()` functions
- Update `ImageExists()` to use `ImageTag()`
- Update `RunArgs()` to use `ImageTag()` instead of `ImageName`
- Update `ShellArgs()` to use `ImageTag()` instead of `ImageName`
- Remove `HostClaudeDir` and `HostClaudeJSON` from `RunOpts` (keep them — still needed for auth mount)
- Add `"path/filepath"` import

Add to imports: `"path/filepath"`

Remove `BuildArgs` function (lines 37-44):
```go
// DELETE: func BuildArgs(contextDir string) []string { ... }
```

Remove `Build` function (lines 197-205):
```go
// DELETE: func Build(contextDir string) *exec.Cmd { ... }
```

Add these new functions after `ContainerName`:

```go
// ImageTag returns the full Docker image reference (name:tag).
// When CLAUDE_CONTAINER_IMAGE_TAG is set (by the Nix wrapper), it is used.
// Otherwise falls back to the legacy image name.
func ImageTag() string {
	if tag := os.Getenv("CLAUDE_CONTAINER_IMAGE_TAG"); tag != "" {
		return tag
	}
	return ImageName
}

// LoadImage loads the Docker image from the Nix-built tarball.
func LoadImage() error {
	tarball := os.Getenv("CLAUDE_CONTAINER_IMAGE_TARBALL")
	if tarball == "" {
		return fmt.Errorf("CLAUDE_CONTAINER_IMAGE_TARBALL is not set")
	}
	fmt.Println("Loading Docker image...")
	cmd := exec.Command("docker", "load", "-i", tarball)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// EnsureImage ensures the Docker image is available, loading it from the
// Nix-built tarball if necessary. The configDir is used to store a marker
// file tracking which tarball was last loaded.
func EnsureImage(configDir string) error {
	tarball := os.Getenv("CLAUDE_CONTAINER_IMAGE_TARBALL")
	if tarball == "" {
		if ImageExists() {
			return nil
		}
		return fmt.Errorf("image %q not found and CLAUDE_CONTAINER_IMAGE_TARBALL not set", ImageTag())
	}

	// Check marker to see if we already loaded this tarball.
	markerPath := filepath.Join(configDir, "loaded-image")
	if data, err := os.ReadFile(markerPath); err == nil {
		if string(bytes.TrimSpace(data)) == tarball && ImageExists() {
			return nil
		}
	}

	if err := LoadImage(); err != nil {
		return fmt.Errorf("load image: %w", err)
	}

	// Update marker.
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(markerPath, []byte(tarball), 0o644)
	return nil
}
```

Update `ImageExists` (line 127-132):

```go
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageTag())
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}
```

Update `RunArgs` — change `ImageName` to `ImageTag()` at line 76:

```go
	// was: args = append(args, ImageName, "claude")
	args = append(args, ImageTag(), "claude")
```

Update `ShellArgs` — change `ImageName` to `ImageTag()` at line 113:

```go
	// was: args = append(args, ImageName, "/bin/bash")
	args = append(args, ImageTag(), "/bin/bash")
```

**Step 2: Update `internal/docker/docker_test.go`**

Remove `TestBuildArgs` entirely.

Update `TestRunArgs` — change `ImageName` check to `ImageTag()`:

```go
	// was: if !slices.Contains(args, ImageName) {
	if !slices.Contains(args, ImageTag()) {
		t.Errorf("RunArgs missing image tag %q in %v", ImageTag(), args)
	}
```

Add test for `ImageTag`:

```go
func TestImageTag(t *testing.T) {
	// Default: returns ImageName when env var not set.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "")
	if got := ImageTag(); got != ImageName {
		t.Errorf("ImageTag() = %q, want %q (default)", got, ImageName)
	}

	// With env var set.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "claude-code:nix")
	if got := ImageTag(); got != "claude-code:nix" {
		t.Errorf("ImageTag() = %q, want %q", got, "claude-code:nix")
	}
}
```

Add test for `EnsureImage` marker logic:

```go
func TestEnsureImageMarker(t *testing.T) {
	// When no tarball env and image doesn't exist, should error.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TARBALL", "")
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "nonexistent:test")
	err := EnsureImage(t.TempDir())
	if err == nil {
		t.Error("EnsureImage should error when no tarball and image missing")
	}
}
```

**Step 3: Run tests**

Run: `nix develop /home/joe/Development/claude-container -c go test ./internal/docker/ -v`
Expected: All tests pass (TestBuildArgs removed, new tests pass)

**Step 4: Commit**

```bash
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat: replace docker build with docker load for Nix-built images"
```

---

### Task 4: Update Go commands

**Files:**
- Modify: `cmd/new.go:132-142`
- Modify: `cmd/auth.go:65-67,78`
- Modify: `cmd/build.go` (full rewrite)
- Modify: `cmd/shell.go` (add EnsureImage call)

**Step 1: Update `cmd/new.go`**

Replace lines 132-142 (the docker build auto-build block) with:

```go
	// d. Ensure docker image is loaded.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return err
	}
```

Remove the `"os/exec"` import if it's no longer used (check — it's still used by `exec.Command` in `createSession` for starting detached containers, so keep it).

Actually, `cmd/new.go` imports `"os"` which is still needed. And `exec.Command` is used at line 225 for starting the detached container. So no import changes needed.

**Step 2: Update `cmd/auth.go`**

Replace lines 65-67 (image exists check):

```go
	// was:
	// if !docker.ImageExists() {
	//     return fmt.Errorf(...)
	// }

	// Ensure docker image is loaded.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return err
	}
```

Replace `docker.ImageName` with `docker.ImageTag()` at line 78:

```go
	// was: docker.ImageName,
	docker.ImageTag(),
```

**Step 3: Update `cmd/build.go`**

Replace entire file:

```go
package cmd

import (
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Load the Claude Code Docker image",
	Long:  `Load the Docker image from the Nix-built tarball. This happens automatically when creating sessions, but you can use this command to force a reload.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return docker.LoadImage()
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
```

**Step 4: Update `cmd/shell.go`**

Add `EnsureImage` call before `ShellArgs`. Insert after the `MkdirAll` block (after line 29):

```go
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return err
	}
```

**Step 5: Run all Go tests**

Run: `nix develop /home/joe/Development/claude-container -c go test ./... -v`
Expected: All tests pass

**Step 6: Verify build**

Run: `nix develop /home/joe/Development/claude-container -c go build ./...`
Expected: Compiles with no errors

**Step 7: Commit**

```bash
git add cmd/new.go cmd/auth.go cmd/build.go cmd/shell.go
git commit -m "feat: update commands to use docker load instead of docker build"
```

---

### Task 5: Remove old Docker files

**Files:**
- Delete: `docker/Dockerfile`
- Delete: `docker/entrypoint.sh`
- Delete: `docker/managed-settings.json`

**Step 1: Remove docker/ directory**

Run: `rm -rf /home/joe/Development/claude-container/docker/`

**Step 2: Verify Go still compiles**

Run: `nix develop /home/joe/Development/claude-container -c go build ./...`
Expected: Compiles (Go doesn't reference docker/ files)

**Step 3: Commit**

```bash
git add -A docker/
git commit -m "chore: remove old Dockerfile, replaced by Nix dockerTools image"
```

---

### Task 6: Verify Nix build

**Step 1: Build the image**

Run: `cd /home/joe/Development/claude-container && nix build .#claude-container-image --print-out-paths`
Expected: Produces a tarball path like `/nix/store/xxx-docker-image-claude-code.tar.gz`

If this fails, debug the error. Common issues:
- `su-exec` package name (try `pkgs.su-exec` or check with `nix search nixpkgs su-exec`)
- `glibc.bin` path (try `pkgs.glibc.bin` or `pkgs.glibcLocalesUtf8`)
- NSS module resolution (may need `pkgs.glibc` in contents)

**Step 2: Load the image**

Run: `docker load -i $(nix build .#claude-container-image --print-out-paths --no-link)`
Expected: `Loaded image: claude-code:nix`

**Step 3: Test the entrypoint**

Run: `docker run --rm -it -e USER_UID=$(id -u) -e USER_GID=$(id -g) claude-code:nix /bin/bash -c 'whoami && id'`
Expected: Shows a user matching your UID/GID

**Step 4: Build the CLI**

Run: `nix build .#claude-container --print-out-paths`
Expected: Produces a path containing `bin/claude-container` and `bin/yacc`

**Step 5: Verify wrapper env vars**

Run: `cat $(nix build .#claude-container --print-out-paths --no-link)/bin/claude-container | grep CLAUDE_CONTAINER`
Expected: Shows `CLAUDE_CONTAINER_IMAGE_TARBALL` and `CLAUDE_CONTAINER_IMAGE_TAG` set

**Step 6: Run Go checks**

Run: `nix build .#checks.x86_64-linux`
Expected: All Go tests pass

---

### Task 7: End-to-end verification

**Step 1: Test `claude-container build`**

Run the built CLI:
```bash
result=$(nix build .#claude-container --print-out-paths --no-link)
$result/bin/claude-container build
```
Expected: "Loading Docker image..." then success

**Step 2: Test `claude-container auth status`**

```bash
$result/bin/claude-container auth status
```
Expected: Shows authentication status (host credentials or not authenticated)

**Step 3: Test `claude-container shell`**

```bash
$result/bin/claude-container shell /tmp
```
Expected: Drops into a bash shell inside the container. Verify:
- `whoami` shows correct user
- `claude --version` works
- `git --version` works
- Exit with `exit`

**Step 4: Test `claude-container new --no-worktree --yolo --rm -b --name nix-test`**

```bash
cd /tmp && $result/bin/claude-container new --no-worktree --yolo --rm -b --name nix-test
```
Expected: Creates a session in background mode

**Step 5: Verify yacc alias**

```bash
$result/bin/yacc --version
```
Expected: Same output as `claude-container --version`

---

## Consumer Integration (for dotfiles)

After this is merged, the dotfiles integration looks like:

```nix
# dotfiles/flake.nix — add follows for llm-agents
claude-container = {
  url = "git+file:///home/joe/Development/claude-container";
  inputs.nixpkgs.follows = "nixpkgs";
  inputs.llm-agents.follows = "llm-agents";
};

# dotfiles/hosts/common/home/packages.nix or claude/default.nix
let
  container = inputs.claude-container.lib.mkClaudeContainer {
    inherit pkgs;
    claude-code = claudeWrapped; # from claude-nix mkClaude with plugins
    settings = settingsContent;
    extraPackages = with pkgs; [ ripgrep fd ];
  };
in {
  home.packages = [ container.cli ];
}
```

This gives you a `claude-container` (and `yacc`) binary that:
- Contains a Docker image with your plugins, settings, and extra packages baked in
- Auto-loads the image via `docker load` on first use
- Uses `claude-nix` plugins inside the container (same as host)
