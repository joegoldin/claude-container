# Auth Refresh Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `claude-container auth refresh` command that re-copies host credentials into all running containers.

**Architecture:** A new `RefreshAuth` function in the docker package runs `docker exec` with a shell script that copies credentials from the existing read-only mounts (`/mnt/claude-host/`, `/mnt/claude-host-json`) into `/claude/`. A new Cobra subcommand iterates all running sessions and calls it.

**Tech Stack:** Go, Cobra, Docker exec

---

### Task 1: Add `RefreshAuth` to docker package

**Files:**
- Modify: `internal/docker/docker.go` (after `ExecGitDiff` at line ~496)
- Test: `internal/docker/docker_test.go`

**Step 1: Write the failing test**

Add to `internal/docker/docker_test.go`:

```go
func TestRefreshAuthArgs(t *testing.T) {
	cmd := RefreshAuthCmd("test-session")
	args := cmd.Args

	if args[0] != "docker" {
		t.Errorf("first arg = %q, want docker", args[0])
	}
	if args[1] != "exec" {
		t.Errorf("second arg = %q, want exec", args[1])
	}
	if args[2] != ContainerName("test-session") {
		t.Errorf("third arg = %q, want container name", args[2])
	}
	if args[3] != "sh" || args[4] != "-c" {
		t.Errorf("args[3:5] = %v, want [sh -c]", args[3:5])
	}

	script := args[5]
	// Must copy the three credential files from /mnt/claude-host/.
	for _, f := range []string{".credentials.json", "settings.json", ".claude.json"} {
		if !strings.Contains(script, f) {
			t.Errorf("refresh script missing %q", f)
		}
	}
	// Must handle /mnt/claude-host-json.
	if !strings.Contains(script, "/mnt/claude-host-json") {
		t.Errorf("refresh script missing /mnt/claude-host-json copy")
	}
	// Must set permissions to 600.
	if !strings.Contains(script, "chmod 600") {
		t.Errorf("refresh script missing chmod 600")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/docker/ -run TestRefreshAuthArgs -v`
Expected: FAIL — `RefreshAuthCmd` not defined

**Step 3: Write minimal implementation**

Add to `internal/docker/docker.go` after the `ExecGitDiff` function:

```go
// refreshAuthScript is the shell snippet that copies host credentials
// from the read-only mounts into /claude/. Mirrors the entrypoint logic
// in nix/image.nix.
const refreshAuthScript = `
if [ -d /mnt/claude-host ]; then
  for f in .credentials.json settings.json .claude.json; do
    if [ -f "/mnt/claude-host/$f" ]; then
      cp -L "/mnt/claude-host/$f" "/claude/$f" && chmod 600 "/claude/$f"
    fi
  done
fi
if [ -f /mnt/claude-host-json ]; then
  cp -L /mnt/claude-host-json /claude/.claude.json && chmod 600 /claude/.claude.json
fi`

// RefreshAuthCmd returns a prepared *exec.Cmd that re-copies host
// credentials from the read-only mounts into /claude/ inside the
// container. The caller is responsible for running the command.
func RefreshAuthCmd(session string) *exec.Cmd {
	name := ContainerName(session)
	return exec.Command("docker", "exec", name, "sh", "-c", refreshAuthScript)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/docker/ -run TestRefreshAuthArgs -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat: add RefreshAuthCmd to docker package"
```

---

### Task 2: Add `auth refresh` Cobra subcommand

**Files:**
- Modify: `cmd/auth.go`

**Step 1: Write the subcommand**

Add to `cmd/auth.go` after the `authStatusCmd` variable (after line 45):

```go
var authRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Re-copy host credentials into running containers",
	Long:  `Re-copies host credentials from ~/.claude/ into all running containers. Use after re-authenticating on the host.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())

		sessions := store.List()
		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}

		refreshed := 0
		for _, sess := range sessions {
			if !docker.IsRunning(sess.Name) {
				continue
			}
			c := docker.RefreshAuthCmd(sess.Name)
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				fmt.Printf("  %s: failed (%v)\n", sess.Name, err)
				continue
			}
			fmt.Printf("  %s: refreshed\n", sess.Name)
			refreshed++
		}

		if refreshed == 0 {
			fmt.Println("No running containers found.")
		} else {
			fmt.Printf("Refreshed credentials in %d container(s).\n", refreshed)
		}
		return nil
	},
}
```

Update the `init()` function to register the new subcommand. Change:

```go
func init() {
	authCmd.AddCommand(authStatusCmd)
	rootCmd.AddCommand(authCmd)
}
```

To:

```go
func init() {
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authRefreshCmd)
	rootCmd.AddCommand(authCmd)
}
```

**Step 2: Verify it compiles**

Run: `go build ./...`
Expected: Success

**Step 3: Run all tests**

Run: `go test ./...`
Expected: All pass

**Step 4: Commit**

```bash
git add cmd/auth.go
git commit -m "feat: add 'auth refresh' command to re-copy host credentials"
```
