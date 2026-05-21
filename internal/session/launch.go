package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	"github.com/joegoldin/claude-container/internal/proxy"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
)

// Launch creates and starts a Claude Code container with all the requested
// scaffolding (workspace, proxy, config dir, session record) and returns a
// Handle whose method the caller invokes based on opts.Mode.
func Launch(ctx context.Context, store *config.Store, opts Opts) (*Handle, error) {
	opts.ApplyDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	// Step 1: image readiness.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return nil, err
	}

	// Step 2: workspace resolution and session name.
	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}
	}
	if opts.Name == "" {
		opts.Name = config.GenerateName(cwd)
	}
	ws, err := ResolveWorkspace(cwd, opts)
	if err != nil {
		return nil, err
	}
	repoRoot := ws.RepoPath
	if repoRoot == "" {
		repoRoot = cwd
	}

	// Step 3: per-repo + per-session config dirs.
	if err := os.MkdirAll(store.RepoConfigDir(repoRoot), 0o755); err != nil {
		return nil, fmt.Errorf("create repo config dir: %w", err)
	}
	if err := store.UpsertRepo(repoRoot); err != nil {
		return nil, fmt.Errorf("update repo index: %w", err)
	}
	claudeConfigDir, err := store.PrepareSessionConfig(opts.Name, repoRoot, opts.Resume)
	if err != nil {
		return nil, fmt.Errorf("prepare session config: %w", err)
	}

	// Step 4: write managed-settings for the chosen profile.
	prof, err := sandboxPkg.GetProfile(opts.Profile)
	if err != nil {
		return nil, err
	}

	// Build extra allow perms: allow-commands (wrapped) + raw allow-perms.
	var extraAllowPerms []string
	extraAllowPerms = append(extraAllowPerms, wrapCommandPerms(opts.AllowCommands)...)
	extraAllowPerms = append(extraAllowPerms, opts.AllowPerms...)

	// Build extra deny perms: deny-paths as Read() rules + deny-commands
	// (wrapped) + raw deny-perms.
	var extraDenyPerms []string
	for _, p := range opts.DenyPaths {
		extraDenyPerms = append(extraDenyPerms, fmt.Sprintf("Read(%s)", p))
	}
	extraDenyPerms = append(extraDenyPerms, wrapCommandPerms(opts.DenyCommands)...)
	extraDenyPerms = append(extraDenyPerms, opts.DenyPerms...)

	settingsJSON, err := json.MarshalIndent(
		prof.ManagedSettingsForProxy(8080, extraAllowPerms, extraDenyPerms, opts.Packages),
		"", "  ",
	)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(claudeConfigDir, "managed-settings.json"), settingsJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write managed-settings: %w", err)
	}

	// Step 5: per-session proxy.
	if !httpproxy.ImageExists() {
		tarball := os.Getenv("CLAUDE_PROXY_IMAGE_TARBALL")
		if tarball == "" {
			return nil, fmt.Errorf("proxy image %q not loaded; set CLAUDE_PROXY_IMAGE_TARBALL or run claude-container build", httpproxy.ImageTag())
		}
		load := exec.Command("docker", "load", "-i", tarball)
		load.Stdout = os.Stdout
		load.Stderr = os.Stderr
		if err := load.Run(); err != nil {
			return nil, fmt.Errorf("load proxy image: %w", err)
		}
	}

	// When packages are requested, auto-allow nix domains through the proxy.
	proxyAllow := append([]string(nil), opts.AllowDomains...)
	if len(opts.Packages) > 0 {
		proxyAllow = append(proxyAllow,
			"cache.nixos.org", "*.cache.nixos.org",
			"channels.nixos.org", "releases.nixos.org",
			"github.com", "*.github.com", "*.githubusercontent.com",
		)
	}
	rulesJSON, err := json.MarshalIndent(prof.ProxyRules(proxyAllow), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := httpproxy.EnsureSessionRules(config.DefaultDir(), opts.Name, opts.ProxySeedPreset); err != nil {
		return nil, fmt.Errorf("seed proxy rules: %w", err)
	}
	if err := httpproxy.AppendSessionRules(config.DefaultDir(), opts.Name, rulesJSON); err != nil {
		return nil, fmt.Errorf("append proxy rules: %w", err)
	}
	_, resolvedPort, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Session:       opts.Name,
		ConfigDir:     config.DefaultDir(),
		DashboardPort: opts.ProxyPort,
		ForceRestart:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("start proxy: %w", err)
	}
	if err := httpproxy.WaitForDashboardToken(config.DefaultDir(), opts.Name, 30*time.Second); err != nil {
		return nil, err
	}
	if err := httpproxy.WaitForCACert(config.DefaultDir()); err != nil {
		return nil, err
	}

	// Step 6: resolve extra mounts (-w paths and -W named workspace).
	extraWorkspaces, worktreeRepos, err := resolveMounts(opts, ws.Worktree)
	if err != nil {
		return nil, err
	}

	// Step 7: build docker.RunOpts and start the container.
	workspace := ws.HostPath
	if ws.Worktree {
		workspace = ""
	}
	if len(extraWorkspaces) > 0 || len(worktreeRepos) > 0 {
		// When extra workspaces are present, don't mount cwd as primary workspace.
		workspace = ""
	}

	runOpts := docker.RunOpts{
		Name:               opts.Name,
		Workspace:          workspace,
		ConfigDir:          claudeConfigDir,
		HostClaudeFiles:    config.HostClaudeCredentialFiles(),
		UID:                docker.ContainerUID(),
		GID:                docker.ContainerGID(),
		Yolo:               prof.Yolo,
		Prompt:             opts.Prompt,
		Resume:             opts.Resume,
		Continue:           opts.Continue && opts.Resume == "",
		ExtraWorkspaces:    extraWorkspaces,
		WorktreeRepos:      worktreeRepos,
		ProxyEnabled:       true,
		ProxyCACertDir:     httpproxy.CACertDir(config.DefaultDir()),
		ProxyDashboardPort: resolvedPort,
		Packages:           opts.Packages,
		Mode:               string(opts.Mode),
	}
	if ws.Worktree {
		runOpts.WorktreeBranch = ws.Branch
		runOpts.WorktreeFrom = opts.From
		if len(worktreeRepos) > 0 {
			// Multi-repo: don't mount primary repo at /mnt/repo.
		} else {
			runOpts.RepoPath = repoRoot
		}
	}

	var dockerArgs []string
	switch opts.Mode {
	case ModeTask:
		dockerArgs = docker.TaskRunArgs(runOpts, "", 0)
	default:
		dockerArgs = docker.RunArgs(runOpts, true)
	}

	startCmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return nil, fmt.Errorf("docker run: %w", err)
	}

	// Step 8: persist session record.
	worktreePath := ws.HostPath
	if ws.Worktree {
		worktreePath = ""
	}
	sess := &config.Session{
		Name:            opts.Name,
		Branch:          ws.Branch,
		WorktreePath:    worktreePath,
		RepoPath:        repoRoot,
		ContainerName:   docker.ContainerName(opts.Name),
		Yolo:            prof.Yolo,
		AutoRemove:      opts.AutoRemove,
		CreatedAt:       time.Now(),
		Profile:         opts.Profile,
		ExtraWorkspaces: extraWorkspaces,
		WorktreeRepos:   worktreeRepos,
		AllowDomains:    opts.AllowDomains,
		DenyPaths:       opts.DenyPaths,
		AllowCommands:   opts.AllowCommands,
		DenyCommands:    opts.DenyCommands,
		AllowPerms:      opts.AllowPerms,
		DenyPerms:       opts.DenyPerms,
		Packages:        opts.Packages,
		ProxySeedPreset: opts.ProxySeedPreset,
		ProxyPort:       resolvedPort,
		Mode:            string(opts.Mode),
	}
	if err := store.Save(sess); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}

	cleanup := func() {
		if opts.AutoRemove {
			_ = docker.Stop(opts.Name)
			_ = docker.Remove(opts.Name)
			_ = httpproxy.Stop(opts.Name)
			_ = httpproxy.RemoveSessionState(config.DefaultDir(), opts.Name)
			_ = httpproxy.RemoveNetwork(opts.Name)
			_ = store.Delete(opts.Name)
		}
		_ = store.SaveNewConversations(opts.Name, repoRoot)
	}

	return &Handle{
		Name:      opts.Name,
		Container: docker.ContainerName(opts.Name),
		Repo:      repoRoot,
		Branch:    ws.Branch,
		ProxyPort: resolvedPort,
		StatusBar: proxy.StatusBarInfo{
			Name:      opts.Name,
			Branch:    ws.Branch,
			Yolo:      prof.Yolo,
			ProxyPort: resolvedPort,
		},
		cleanup: cleanup,
	}, nil
}

// resolveMounts merges -W (named workspace) and -w (ad-hoc paths) into
// either extraWorkspaces (subdir mounts under /workspace) or worktreeRepos
// (per-repo worktrees when worktree mode is on and the path is a git repo).
func resolveMounts(opts Opts, worktreeMode bool) (extraWorkspaces, worktreeRepos []string, err error) {
	var paths []string
	if opts.WorkspaceName != "" {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		wsPaths, e := ws.Get(opts.WorkspaceName)
		if e != nil {
			return nil, nil, e
		}
		paths = append(paths, wsPaths...)
	}
	for _, m := range opts.Mounts {
		abs, e := filepath.Abs(m)
		if e != nil {
			return nil, nil, fmt.Errorf("resolve mount %q: %w", m, e)
		}
		paths = append(paths, abs)
	}
	if len(paths) == 0 {
		return nil, nil, nil
	}

	seen := make(map[string]string)
	for _, p := range paths {
		if _, e := os.Stat(p); e != nil {
			return nil, nil, fmt.Errorf("mount %q does not exist", p)
		}
		base := filepath.Base(p)
		if existing, ok := seen[base]; ok {
			return nil, nil, fmt.Errorf("basename collision: %q and %q both have basename %q", existing, p, base)
		}
		seen[base] = p
	}

	if worktreeMode {
		for _, p := range paths {
			if _, e := gitpkg.RepoRoot(p); e != nil {
				return nil, nil, fmt.Errorf("worktree mode: %q is not a git repository", p)
			}
			worktreeRepos = append(worktreeRepos, p)
		}
		return nil, worktreeRepos, nil
	}
	return paths, nil, nil
}

// wrapCommandPerms wraps bare command patterns as Bash() permission rules.
// Example: "docker *" → "Bash(docker *)".
func wrapCommandPerms(commands []string) []string {
	if len(commands) == 0 {
		return nil
	}
	perms := make([]string, len(commands))
	for i, cmd := range commands {
		perms[i] = fmt.Sprintf("Bash(%s)", cmd)
	}
	return perms
}
