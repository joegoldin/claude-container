package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
)

// Bare-invoke flags. Declared on the root command's Flags() (not
// PersistentFlags) so they don't leak into subcommands. The union here
// matches what `run` and `work` accept today.
var (
	rootYolo          bool
	rootPrompt        string
	rootName          string
	rootBackground    bool
	rootAutoRemove    bool
	rootMounts        []string
	rootWorkspaceName string
	rootProfile       string
	rootAllowDomains  []string
	rootDenyPaths     []string
	rootAllowCommands []string
	rootDenyCommands  []string
	rootAllowPerms    []string
	rootDenyPerms     []string
	rootPackages      []string
	rootProxyPreset   string
	rootProxyPort     int
	rootFrom          string
	rootNoWorktree    bool
	rootResume        string
)

var rootCmd = &cobra.Command{
	Use:   "claude-container",
	Short: "Run Claude Code in an isolated, sandboxed container",
	Long: `Bare 'claude-container' creates a sandboxed Claude session in the current
directory and attaches to it. In a git repo it creates a worktree at
<repo>/.worktrees/<name>/; otherwise it pwd-mounts the directory.

Run 'claude-container tui' to open the dashboard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDefault(cmd.Context())
	},
}

func init() {
	f := rootCmd.Flags()
	f.BoolVar(&rootYolo, "yolo", false, "skip Claude Code permission prompts")
	f.StringVarP(&rootPrompt, "prompt", "p", "", "initial prompt to send")
	f.StringVar(&rootName, "name", "", "session name (auto-generated if empty)")
	f.BoolVarP(&rootBackground, "background", "b", false, "run detached without attaching")
	f.BoolVar(&rootAutoRemove, "rm", false, "remove container on exit")
	f.StringArrayVarP(&rootMounts, "mount", "w", nil, "extra host path to mount (repeatable)")
	f.StringVarP(&rootWorkspaceName, "workspace", "W", "", "named workspace (set up with 'claude-container workspace')")
	f.StringVar(&rootProfile, "profile", "default", "sandbox profile (low|default|med|high)")
	f.StringArrayVar(&rootAllowDomains, "allow-domain", nil, "domain the proxy should allow (repeatable)")
	f.StringArrayVar(&rootDenyPaths, "deny-path", nil, "filesystem path to deny (repeatable)")
	f.StringArrayVar(&rootAllowCommands, "allow-command", nil, "shell command pattern to allow (repeatable)")
	f.StringArrayVar(&rootDenyCommands, "deny-command", nil, "shell command pattern to deny (repeatable)")
	f.StringArrayVar(&rootAllowPerms, "allow-perm", nil, "raw permission rule to allow (repeatable)")
	f.StringArrayVar(&rootDenyPerms, "deny-perm", nil, "raw permission rule to deny (repeatable)")
	f.StringArrayVar(&rootPackages, "packages", nil, "extra nixpkgs to install at start (comma- or repeat-separated)")
	f.StringVar(&rootProxyPreset, "preset", "", "proxy seed preset")
	f.IntVar(&rootProxyPort, "proxy-port", 0, "host port for the proxy dashboard")
	f.StringVar(&rootFrom, "from", "", "base branch/ref for the new worktree")
	f.BoolVar(&rootNoWorktree, "no-worktree", false, "pwd passthrough even in a git repo")
	f.StringVar(&rootResume, "resume", "", "resume mode (picker, last, or session id)")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// requireAuth returns an error if the user has not authenticated yet.
func requireAuth(store *config.Store) error {
	if !store.IsAuthenticated() {
		return fmt.Errorf("not authenticated; run 'claude-container auth' first")
	}
	return nil
}

// runDefault is what bare 'claude-container' does: create a session in the
// current directory and attach to it (or run it in the background).
func runDefault(ctx context.Context) error {
	store := config.NewStore(config.DefaultDir())
	if err := requireAuth(store); err != nil {
		return err
	}

	maybePrintBareInvokeNotice(store, config.DefaultDir(), os.Stderr)

	opts := session.Opts{
		Name:            rootName,
		Mode:            session.ModeTTY,
		WorktreeMode:    session.WorktreeAuto,
		NoWorktree:      rootNoWorktree,
		From:            rootFrom,
		Profile:         rootProfile,
		Yolo:            rootYolo,
		AllowDomains:    rootAllowDomains,
		DenyPaths:       rootDenyPaths,
		AllowCommands:   rootAllowCommands,
		DenyCommands:    rootDenyCommands,
		AllowPerms:      rootAllowPerms,
		DenyPerms:       rootDenyPerms,
		Mounts:          rootMounts,
		WorkspaceName:   rootWorkspaceName,
		AutoRemove:      rootAutoRemove,
		Background:      rootBackground,
		Prompt:          rootPrompt,
		Resume:          rootResume,
		Packages:        splitCSV(rootPackages),
		ProxySeedPreset: rootProxyPreset,
		ProxyPort:       rootProxyPort,
	}

	h, err := session.Launch(ctx, store, opts)
	if err != nil {
		return err
	}
	if opts.Background {
		return h.RunBackground()
	}
	attachErr := h.AttachTTY()
	saveResumeID(store, h.Name)
	return attachErr
}

// bareInvokeNoticeFlagFile is the path of the flag file that suppresses
// the migration notice after the first display.
const bareInvokeNoticeFlagFile = "migrated-bare-invoke"

// maybePrintBareInvokeNotice prints a one-line stderr notice the first
// time a user runs bare claude-container after upgrading. It writes a
// flag file in configDir so the notice doesn't repeat.
//
// Suppressed by CLAUDE_CONTAINER_QUIET=1 and (silently) when there are
// no prior sessions.
func maybePrintBareInvokeNotice(store *config.Store, configDir string, out io.Writer) {
	if os.Getenv("CLAUDE_CONTAINER_QUIET") != "" {
		return
	}
	flagPath := filepath.Join(configDir, bareInvokeNoticeFlagFile)
	if _, err := os.Stat(flagPath); err == nil {
		return
	}
	sessions := store.List()
	if len(sessions) == 0 {
		// Nothing to migrate; mark seen so the notice never fires.
		_ = os.MkdirAll(configDir, 0o755)
		_ = os.WriteFile(flagPath, nil, 0o644)
		return
	}
	fmt.Fprintf(out,
		"note: bare claude-container now creates a session; run 'claude-container tui' for the dashboard (existing sessions: %d)\n",
		len(sessions),
	)
	_ = os.MkdirAll(configDir, 0o755)
	_ = os.WriteFile(flagPath, nil, 0o644)
}

// splitCSV expands any comma-separated values inside a string slice.
// Existing entries are passed through, multi-value entries split apart.
func splitCSV(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	var out []string
	for _, v := range in {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
