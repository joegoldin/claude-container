package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/transcript"
	"github.com/spf13/cobra"
)

var (
	extractFormat          string
	extractOutput          string
	extractIncludeThinking bool
	extractForce           bool
)

// extractCmd pulls a Claude Code conversation out of a claude-container
// session into a file on the host. Three output shapes are supported; see
// the per-format documentation below for the prompt-injection posture of
// each.
var extractCmd = &cobra.Command{
	Use:   "extract <session>",
	Short: "Extract a Claude Code conversation from a session",
	Long: `Extract a Claude Code conversation from a claude-container session.

Three output formats are supported:

  text     Plain markdown of the conversation: user messages, assistant
           text, tool calls, and tool results. Rendered locally by parsing
           the session's JSONL transcript — no LLM involved. ANSI escapes
           in tool output are stripped before writing. Safe by default.

  summary  Short bulleted summary. Runs a one-shot claude process INSIDE
           the session's running container (via 'docker exec') with the
           transcript loaded via --resume. Any prompt injection in the
           transcript stays sandboxed by the container's existing proxy
           and permissions, same as the original session.

  resume   Raw JSONL copy. Writes the session's .jsonl file to a host
           path you choose so you can hand it to another tool or drop it
           into ~/.claude/projects/ to resume outside the container.
           HIGHEST RISK: resuming on the host loads every prior tool
           output — web fetches, file reads, command results — as prior
           context into the host claude session. If anything malicious
           landed in the container's transcript, that content is now
           driving the host model. Requires --force to skip the prompt.

Examples:
  claude-container extract my-session                        # text → stdout
  claude-container extract my-session --output convo.md      # text → file
  claude-container extract my-session --format summary       # summary → stdout
  claude-container extract my-session --format resume --output ./convo.jsonl
`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		store := config.NewStore(config.DefaultDir())
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		// Resolve the host path to the JSONL transcript. This works for
		// both running and stopped sessions because the config dir is
		// bind-mounted from the host — the file is already on disk.
		claudeCfg := store.ClaudeConfigDir()
		path, err := transcript.FindTranscript(transcript.FindOptions{
			ClaudeConfigDir: claudeCfg,
			ResumeID:        sess.ResumeID,
		})
		if err != nil {
			return fmt.Errorf("locate transcript: %w\n"+
				"hint: attach to the session at least once so claude-container can record its session id",
				err)
		}

		switch extractFormat {
		case "", "text":
			return runExtractText(path, extractOutput, extractIncludeThinking)
		case "summary":
			return runExtractSummary(cmd, name, sess)
		case "resume":
			return runExtractResume(cmd, path, extractOutput, sess, extractForce)
		default:
			return fmt.Errorf("unknown --format %q (want text, summary, or resume)", extractFormat)
		}
	},
}

// runExtractText renders the JSONL at path as markdown and writes it to
// output (or stdout when output is empty).
func runExtractText(path, output string, includeThinking bool) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer in.Close()

	var w io.Writer
	if output == "" {
		w = os.Stdout
	} else {
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("create output: %w", err)
		}
		defer f.Close()
		w = f
	}

	if err := transcript.RenderMarkdown(in, w, transcript.RenderOptions{
		IncludeThinking: includeThinking,
	}); err != nil {
		return fmt.Errorf("render transcript: %w", err)
	}
	if output != "" {
		fmt.Fprintf(os.Stderr, "wrote transcript to %s\n", output)
	}
	return nil
}

// runExtractSummary runs a one-shot claude inside the session's container
// with --resume, asking for a concise summary. Requires the container to
// be running and a known resume id.
func runExtractSummary(cmd *cobra.Command, name string, sess *config.Session) error {
	if !docker.IsRunning(name) {
		return fmt.Errorf("session %q container is not running — start it with 'claude-container attach %s' first", name, name)
	}
	if sess.ResumeID == "" {
		return fmt.Errorf("session %q has no recorded resume id; attach to the session first so it gets written to disk", name)
	}

	const prompt = "Summarize this conversation in concise markdown: " +
		"a one-sentence TL;DR, then bullet points for the key decisions, " +
		"what was implemented or changed, and any open questions. " +
		"Do not run any tool calls — text output only."

	containerName := docker.ContainerName(name)
	// docker exec with no -t so we get clean stdout. Claude's -p / print
	// mode emits plain text and exits.
	execCmd := exec.Command("docker", "exec", containerName,
		"claude", "-p", "--resume", sess.ResumeID, prompt)

	var out strings.Builder
	execCmd.Stdout = &out
	execCmd.Stderr = os.Stderr
	if err := execCmd.Run(); err != nil {
		return fmt.Errorf("run summary: %w", err)
	}

	summary := out.String()
	if extractOutput == "" {
		fmt.Print(summary)
		return nil
	}
	if err := os.WriteFile(extractOutput, []byte(summary), 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote summary to %s\n", extractOutput)
	return nil
}

// runExtractResume copies the raw JSONL transcript to a host path after
// confirming the user understands the prompt-injection risk. It also
// prints instructions for dropping the file into ~/.claude/projects/ so
// `claude --resume` outside the container picks it up.
func runExtractResume(cmd *cobra.Command, srcPath, output string, sess *config.Session, force bool) error {
	if output == "" {
		output = fmt.Sprintf("./%s.jsonl", sess.Name)
	}

	warning := strings.Join([]string{
		"WARNING: resuming this conversation outside the container exposes",
		"your HOST claude session to ALL prior tool output — web fetches,",
		"file reads, command output — as prior context. If anything",
		"malicious or prompt-injected landed in the transcript, that content",
		"will drive the host model when you resume.",
		"",
		"Only continue if you trust everything that ran in this container.",
	}, "\n")
	fmt.Fprintln(os.Stderr, warning)

	if !force {
		fmt.Fprint(os.Stderr, "\nContinue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("aborted")
		}
	}

	if err := copyFile(srcPath, output); err != nil {
		return fmt.Errorf("copy transcript: %w", err)
	}
	abs, _ := filepath.Abs(output)
	fmt.Fprintf(os.Stderr, "wrote %s\n", abs)

	// Print the exact commands the user would run to drop it into place
	// for `claude --resume`. We encode the current host cwd the same way
	// Claude Code does (slashes → dashes).
	cwd, _ := os.Getwd()
	encoded := transcript.EncodeHostCwd(cwd)
	home, _ := os.UserHomeDir()
	targetDir := filepath.Join(home, ".claude", "projects", encoded)
	targetFile := filepath.Join(targetDir, sess.ResumeID+".jsonl")

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "To resume on the host from this cwd:")
	fmt.Fprintf(os.Stderr, "  mkdir -p %s\n", shellQuote(targetDir))
	fmt.Fprintf(os.Stderr, "  cp %s %s\n", shellQuote(abs), shellQuote(targetFile))
	fmt.Fprintf(os.Stderr, "  claude --resume %s\n", sess.ResumeID)
	return nil
}

// copyFile is a small file copy used by extract. Refuses to clobber an
// existing destination unless --force is set (checked by the caller).
func copyFile(src, dst string) error {
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
	return out.Sync()
}

// shellQuote wraps s in single quotes for safe pasting into a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	extractCmd.Flags().StringVarP(&extractFormat, "format", "f", "text",
		"Output format: text, summary, or resume")
	extractCmd.Flags().StringVarP(&extractOutput, "output", "o", "",
		"Output file path (default: stdout for text/summary; ./<session>.jsonl for resume)")
	extractCmd.Flags().BoolVar(&extractIncludeThinking, "include-thinking", false,
		"Include assistant thinking blocks in text output")
	extractCmd.Flags().BoolVar(&extractForce, "force", false,
		"Skip the prompt-injection risk confirmation (resume mode only)")
	rootCmd.AddCommand(extractCmd)
}
