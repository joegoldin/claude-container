# Workspaces and Sandbox Profiles Design

## Goal

Add multi-folder workspaces (named and ad-hoc) and tiered sandbox profiles (low/med/high) to claude-container, so users can start sessions with multiple repos mounted and fine-grained security controls.

## Multi-Folder Workspaces

### Data Model

Named workspaces are stored in `~/.config/claude-container/workspaces.json`:

```json
{
  "my-work": ["/home/joe/code/repo-a", "/home/joe/code/repo-b"],
  "infra": ["/home/joe/infra/terraform", "/home/joe/infra/ansible"]
}
```

Simple map of name to list of absolute paths.

### Container Layout

All folders mount as subdirectories of `/workspace/`:

```
/workspace/
  repo-a/      <- from ~/code/repo-a
  repo-b/      <- from ~/code/repo-b
```

Directory names are the basename of each path. If two paths share a basename, error out at session creation.

### Worktree Mode

`claude-container work -w ~/code/a -w ~/code/b` creates a git worktree for each folder:

- Worktrees stored under `~/.config/claude-container/worktrees/{session-name}/`
- Layout: `worktrees/{session-name}/repo-a/`, `worktrees/{session-name}/repo-b/`
- Each worktree gets its own branch: `{session-name}/{basename}`
- Non-git directories error in worktree mode

### Backward Compatibility

No `-w` and no `-W` = current behavior (cwd mounted at `/workspace`). When `-w` is used, cwd is NOT auto-added.

## Sandbox Profiles

### Three Hardcoded Profiles

| | `low` | `med` (default) | `high` |
|---|---|---|---|
| Sandbox | OFF | ON | ON |
| Network | unrestricted | allowlisted (anthropic, github, npm, pypi, yarn) | api.anthropic.com only |
| Filesystem | full access | deny ~/.ssh, ~/.aws, ~/.gnupg, /etc/shadow | /workspace only, deny everything else |
| Use case | trusted code, yolo | normal dev | untrusted code, auditing |

### Profile Application

Profiles generate a managed-settings JSON overlay written into the container's config dir at session start, overriding the baked-in managed-settings.nix defaults.

`low` profile is equivalent to `--yolo`.

### Runtime Overrides

Additive flags that modify the selected profile:

- `--allow-domain=example.com` -- adds to `sandbox.network.allowedDomains`
- `--deny-path=/some/dir` -- adds to `permissions.deny`

### Default

If `--profile` is omitted, `med` is used (matches current behavior). `--yolo` is a shorthand for `--profile=low`.

## CLI Changes

### Existing Commands Get New Flags

```
claude-container run [flags]
  -w, --mount stringArray       Additional folders to mount (repeatable)
  -W, --workspace string        Named workspace from workspaces.json
      --profile string          Sandbox profile: low, med, high (default "med")
      --allow-domain stringArray  Add domain to sandbox allowlist
      --deny-path stringArray     Add path to sandbox deny list

claude-container work [flags]
  (same new flags as run, plus existing --from flag)
```

### New Workspace Subcommand

```
claude-container workspace add <name> <path>...   # create or append paths
claude-container workspace list                    # list all workspace names
claude-container workspace show <name>             # show paths in a workspace
claude-container workspace rm <name>               # delete workspace definition
```

### Interaction Rules

- `--yolo` and `--profile=low` are equivalent. Both given = error.
- `-w` and `-W` can combine (workspace paths + ad-hoc paths merged).
- `-w` with `work` creates worktrees for all paths. Non-git dirs error.
- `-w` with `run` mounts directories directly. No git requirement.

## Internal Changes

### New Package: `internal/sandbox`

Profile definitions and JSON generation. Each profile is a struct that produces a managed-settings JSON blob. Profiles are a Go map, not config files.

### Modified: `internal/docker`

- `RunOpts` gets `ExtraWorkspaces []string` for additional mount paths.
- `RunArgs()` generates additional `-v path:/workspace/basename` entries.

### Modified: `internal/config`

- New `WorkspaceStore` type reading/writing `workspaces.json`.
- Methods: `AddWorkspace`, `GetWorkspace`, `ListWorkspaces`, `RemoveWorkspace`.

### Modified: `cmd/run.go`, `cmd/work.go`, `cmd/new.go`

- `createSession()` resolves `-W` workspace name to paths, merges with `-w` flags.
- Passes merged paths to `RunOpts.ExtraWorkspaces`.
- Passes profile + overrides to sandbox package for JSON generation.

### Modified: `config.Session`

- Stores profile name and any overrides for resume/reattach.
