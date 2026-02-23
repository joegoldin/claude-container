package cmd

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/proxy"
	"github.com/joegoldin/claude-container/internal/tui"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "claude-container",
	Short: "Run multiple Claude Code instances in isolated containers",
	Long:  `A CLI tool for running multiple Claude Code instances in isolated, sandboxed Docker containers with git worktree separation and a TUI dashboard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}

		for {
			m := tui.NewDashboard(store)
			p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())

			result, err := p.Run()
			if err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}

			dm, ok := result.(tui.DashboardModel)
			if !ok {
				return nil
			}

			if dm.Attached() != "" {
				attachName := dm.Attached()
				sess, _ := store.Get(attachName)
				containerName := docker.ContainerName(attachName)
				if docker.IsRunning(attachName) {
					// Already running, just attach.
				} else if docker.Exists(attachName) {
					// Stopped, restart it.
					if err := docker.Start(attachName); err != nil {
						fmt.Fprintf(os.Stderr, "error: start container: %v\n", err)
						continue
					}
				} else if sess != nil {
					// Container gone — recreate with resume or continue.
					runOpts := docker.RunOpts{
						Name:           attachName,
						Workspace:      sess.WorktreePath,
						ConfigDir:      store.ClaudeConfigDir(),
						HostClaudeDir:  config.HostClaudeDir(),
						HostClaudeJSON: config.HostClaudeJSON(),
						UID:            docker.ContainerUID(),
						GID:            docker.ContainerGID(),
						Yolo:           sess.Yolo,
						Resume:         sess.ResumeID,
						Continue:       sess.ResumeID == "",
					}
					startCmd := exec.Command("docker", docker.RunArgs(runOpts, true)...)
					startCmd.Stderr = os.Stderr
					if err := startCmd.Run(); err != nil {
						fmt.Fprintf(os.Stderr, "error: recreate container: %v\n", err)
						continue
					}
				} else {
					continue
				}
				branch := ""
				yolo := false
				autoRemove := false
				if sess != nil {
					branch = sess.Branch
					yolo = sess.Yolo
					autoRemove = sess.AutoRemove
				}
				_ = proxy.Run(proxy.Opts{
					DockerArgs:    []string{"attach", containerName},
					ContainerName: containerName,
					StatusBar:     proxy.StatusBarInfo{Name: attachName, Branch: branch, Yolo: yolo},
					AutoRemove:    autoRemove,
					Cleanup:       func(_ string) { removeSession(store, attachName) },
				})
				saveResumeID(store, attachName)
				continue
			}

			if dm.Creating() {
				dir := dm.CreatingDir()
				if dir == "" {
					dir, _ = os.Getwd()
				}
				repoPath, _ := gitpkg.RepoRoot(dir)

				wiz := tui.NewWizard(repoPath, dir)
				wp := tea.NewProgram(wiz, tea.WithAltScreen())
				wResult, err := wp.Run()
				if err != nil {
					fmt.Fprintln(os.Stderr, "wizard error:", err)
					continue
				}
				res := wResult.(tui.WizardModel).Result()
				if res.Cancelled {
					continue
				}
				// From dashboard, always create in background mode so we
			// return to the dashboard loop. Then auto-attach unless
			// the user pressed ctrl+b in the wizard.
			if err := createSession(createOpts{
					name:       res.Name,
					worktree:   res.Worktree,
					from:       res.From,
					noWorktree: res.NoWorktree,
					yolo:       res.Yolo,
					prompt:     res.Prompt,
					background: true, // dashboard manages attach
				}); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					continue
				}
				if !res.Background {
					cn := docker.ContainerName(res.Name)
					_ = proxy.Run(proxy.Opts{
						DockerArgs:    []string{"attach", cn},
						ContainerName: cn,
						StatusBar:     proxy.StatusBarInfo{Name: res.Name, Yolo: res.Yolo},
						Cleanup:       func(_ string) { removeSession(store, res.Name) },
					})
					saveResumeID(store, res.Name)
				}
				continue
			}

			// User quit.
			return nil
		}
	},
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
