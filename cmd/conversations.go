package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

func findRepoByName(repos map[string]config.RepoEntry, name string) (string, config.RepoEntry, bool) {
	for id, entry := range repos {
		if entry.Name == name {
			return id, entry, true
		}
	}
	return "", config.RepoEntry{}, false
}

func conversationDir(store *config.Store, repoPath string) string {
	return filepath.Join(store.RepoConfigDir(repoPath), "projects", "-workspace")
}

func listConversationFiles(dir string) ([]os.FileInfo, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var infos []os.FileInfo
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		infos = append(infos, info)
	}
	return infos, nil
}

var conversationsCmd = &cobra.Command{
	Use:     "conversations",
	Aliases: []string{"convos"},
	Short:   "Manage conversation history across repos",
}

var conversationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List conversation history for all tracked repos",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		repos, err := store.ListRepos()
		if err != nil {
			return fmt.Errorf("loading repos: %w", err)
		}
		if len(repos) == 0 {
			fmt.Println("No conversation history found.")
			return nil
		}

		type row struct {
			entry config.RepoEntry
			count int
			last  string
		}

		var rows []row
		for _, entry := range repos {
			dir := conversationDir(store, entry.Path)
			files, _ := listConversationFiles(dir)
			last := "-"
			if len(files) > 0 {
				// find most recent mod time
				latest := files[0].ModTime()
				for _, f := range files[1:] {
					if f.ModTime().After(latest) {
						latest = f.ModTime()
					}
				}
				last = latest.Format("2006-01-02 15:04")
			}
			rows = append(rows, row{entry: entry, count: len(files), last: last})
		}

		sort.Slice(rows, func(i, j int) bool {
			return rows[i].entry.LastUsed.After(rows[j].entry.LastUsed)
		})

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPATH\tCONVERSATIONS\tLAST MODIFIED")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", r.entry.Name, r.entry.Path, r.count, r.last)
		}
		return w.Flush()
	},
}

var conversationsCopyCmd = &cobra.Command{
	Use:   "copy <source> <target>",
	Short: "Copy conversation files from one repo to another",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		repos, err := store.ListRepos()
		if err != nil {
			return fmt.Errorf("loading repos: %w", err)
		}

		srcName, tgtName := args[0], args[1]
		_, srcEntry, ok := findRepoByName(repos, srcName)
		if !ok {
			return fmt.Errorf("source repo %q not found", srcName)
		}
		_, tgtEntry, ok := findRepoByName(repos, tgtName)
		if !ok {
			return fmt.Errorf("target repo %q not found", tgtName)
		}

		srcDir := conversationDir(store, srcEntry.Path)
		tgtDir := conversationDir(store, tgtEntry.Path)

		convoFlag, _ := cmd.Flags().GetString("conversation")

		var filesToCopy []string
		if convoFlag != "" {
			name := convoFlag
			if !strings.HasSuffix(name, ".jsonl") {
				name += ".jsonl"
			}
			path := filepath.Join(srcDir, name)
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("conversation %q not found in %s", convoFlag, srcName)
			}
			filesToCopy = append(filesToCopy, path)
		} else {
			matches, err := filepath.Glob(filepath.Join(srcDir, "*.jsonl"))
			if err != nil {
				return fmt.Errorf("scanning source: %w", err)
			}
			filesToCopy = matches
		}

		if len(filesToCopy) == 0 {
			fmt.Println("No conversations to copy.")
			return nil
		}

		if err := os.MkdirAll(tgtDir, 0o755); err != nil {
			return fmt.Errorf("creating target directory: %w", err)
		}

		copied := 0
		for _, src := range filesToCopy {
			dst := filepath.Join(tgtDir, filepath.Base(src))
			if err := copyConversationFile(src, dst); err != nil {
				return fmt.Errorf("copying %s: %w", filepath.Base(src), err)
			}
			copied++
		}

		fmt.Printf("Copied %d conversation(s) from %s to %s.\n", copied, srcName, tgtName)
		return nil
	},
}

func copyConversationFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

var conversationsRmCmd = &cobra.Command{
	Use:   "rm <repo-name>",
	Short: "Remove conversation history for a repo",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		repos, err := store.ListRepos()
		if err != nil {
			return fmt.Errorf("loading repos: %w", err)
		}

		repoName := args[0]
		repoID, entry, ok := findRepoByName(repos, repoName)
		if !ok {
			return fmt.Errorf("repo %q not found", repoName)
		}

		convoFlag, _ := cmd.Flags().GetString("conversation")
		force, _ := cmd.Flags().GetBool("force")
		dir := conversationDir(store, entry.Path)

		if convoFlag != "" {
			name := convoFlag
			if !strings.HasSuffix(name, ".jsonl") {
				name += ".jsonl"
			}
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("conversation %q not found in %s", convoFlag, repoName)
			}

			if !force {
				fmt.Printf("Delete 1 conversation from %s? [y/N] ", repoName)
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			if err := os.Remove(path); err != nil {
				return fmt.Errorf("deleting conversation: %w", err)
			}
			fmt.Println("Deleted 1 conversation.")
			return nil
		}

		// Delete all conversations
		files, _ := listConversationFiles(dir)
		count := len(files)

		if count == 0 {
			fmt.Println("No conversations to delete.")
			return nil
		}

		if !force {
			fmt.Printf("Delete %d conversations from %s? [y/N] ", count, repoName)
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		for _, f := range files {
			path := filepath.Join(dir, f.Name())
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(os.Stderr, "warning: removing %s: %v\n", f.Name(), err)
			}
		}

		if err := store.DeleteRepo(repoID); err != nil {
			return fmt.Errorf("removing repo from index: %w", err)
		}

		fmt.Printf("Deleted %d conversations from %s.\n", count, repoName)
		return nil
	},
}

func init() {
	conversationsCopyCmd.Flags().String("conversation", "", "copy only a specific conversation by UUID")
	conversationsRmCmd.Flags().String("conversation", "", "delete only a specific conversation by UUID")
	conversationsRmCmd.Flags().Bool("force", false, "skip confirmation prompt")

	conversationsCmd.AddCommand(conversationsListCmd)
	conversationsCmd.AddCommand(conversationsCopyCmd)
	conversationsCmd.AddCommand(conversationsRmCmd)
	rootCmd.AddCommand(conversationsCmd)
}
