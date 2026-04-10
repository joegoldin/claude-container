package sandbox

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// Profile defines a sandbox security profile.
type Profile struct {
	Name           string
	Description    string
	Yolo           bool     // use --dangerously-skip-permissions
	AllowedDomains []string // for proxy rules
	AllowPerms     []string // permissions.allow rules
	DenyPerms      []string // permissions.deny rules
}

// ProfileOrder defines the display order of profiles from least to most restrictive.
var ProfileOrder = []string{"low", "default", "med", "high"}

// --- Domain allowlists ---

// anthropicDomains are always needed for the Claude API itself.
var anthropicDomains = []string{
	"api.anthropic.com",
	"platform.claude.com",
	"statsig.anthropic.com",
	"sentry.io",
}

// devEcosystemDomains covers GitHub, npm, PyPI, and Nix caches.
var devEcosystemDomains = []string{
	"github.com",
	"*.github.com",
	"*.githubusercontent.com",
	"*.npmjs.org",
	"registry.npmjs.org",
	"registry.yarnpkg.com",
	"pypi.org",
	"*.pypi.org",
	"files.pythonhosted.org",
	"cache.nixos.org",
	"*.cache.nixos.org",
	"channels.nixos.org",
	"releases.nixos.org",
	"devenv.cachix.org",
	"*.cachix.org",
}

// langDomains covers language-specific package registries beyond the basics.
var langDomains = []string{
	"static.rust-lang.org",
}

// standardDomains is anthropic + dev ecosystem (used by med profile).
var standardDomains = concatStrings(anthropicDomains, devEcosystemDomains)

// defaultDomains is standard + language-specific (used by default profile).
var defaultDomains = concatStrings(standardDomains, langDomains)

// --- Permission allowlists ---

// allToolsAllow is the complete set of Claude Code tool permissions that
// allows all tools without restriction. Used by profiles that run in dontAsk
// mode but should not block any tool use.
var allToolsAllow = []string{
	"Bash",
	"Read",
	"Edit",
	"Write",
	"WebFetch",
	"Grep",
	"Glob",
	"LS",
	"MultiEdit",
	"NotebookRead",
	"NotebookEdit",
	"TodoRead",
	"TodoWrite",
	"WebSearch",
	"Agent",
}

// devToolsAllow permits common development commands and file operations.
// More restrictive than allToolsAllow — only specific Bash commands are allowed.
var devToolsAllow = []string{
	"Bash(git *)", "Bash(gh *)", "Bash(npm *)",
	"Bash(pip *)", "Bash(cargo *)", "Bash(make *)",
	"Bash(nix *)", "Bash(devenv *)", "Bash(direnv *)", "Bash(cachix *)",
	"Bash(cd *)", "Bash(echo *)", "Bash(ls *)", "Bash(cat *)", "Bash(grep *)",
	"Bash(find *)", "Bash(touch *)", "Bash(curl *)", "Bash(wget *)",
	"Bash(mkdir *)", "Bash(rm *)", "Bash(cp *)", "Bash(mv *)", "Bash(sleep *)",
	"Read",
	"Edit",
	"Write",
	"WebFetch",
	"Grep",
	"Glob",
	"LS",
	"MultiEdit",
	"NotebookRead",
	"NotebookEdit",
	"TodoRead",
	"TodoWrite",
	"WebSearch",
	"Agent",
}

// ListProfiles returns all profiles in display order.
func ListProfiles() []Profile {
	result := make([]Profile, 0, len(ProfileOrder))
	for _, name := range ProfileOrder {
		result = append(result, profiles[name])
	}
	return result
}

var profiles = map[string]Profile{
	"low": {
		Name:           "low",
		Description:    "Full access. No network restrictions, no permission prompts.",
		Yolo:           true,
		AllowedDomains: nil, // proxy: wildcard allow-all
		AllowPerms:     allToolsAllow,
		DenyPerms:      nil,
	},
	"default": {
		Name:           "default",
		Description:    "Full tool access with network allowlist (GitHub, npm, PyPI, Nix, Rust).",
		Yolo:           true,
		AllowedDomains: defaultDomains,
		AllowPerms:     allToolsAllow,
		DenyPerms:      nil,
	},
	"med": {
		Name:           "med",
		Description:    "Dev tools only. Network allowlist. Sensitive paths denied.",
		Yolo:           false,
		AllowedDomains: standardDomains,
		AllowPerms:     devToolsAllow,
		DenyPerms: []string{
			"Read(~/.ssh/**)", "Read(~/.aws/**)", "Read(~/.gnupg/**)",
			"Read(/etc/shadow)", "Read(/etc/passwd)",
		},
	},
	"high": {
		Name:        "high",
		Description: "Strict lockdown. API-only network. Read-only git. Workspace-only writes.",
		Yolo:        false,
		AllowedDomains: anthropicDomains,
		AllowPerms: []string{
			"Bash(git status *)", "Bash(git diff *)", "Bash(git log *)",
			"Bash(cd *)", "Bash(echo *)", "Bash(ls *)", "Bash(cat *)",
			"Read(/tmp/**)", "Write(/workspace/**)", "Edit(/workspace/**)",
		},
		DenyPerms: []string{
			"Bash(curl *)", "Bash(wget *)",
			"Read(/etc/**)", "Read(~/.ssh/**)", "Read(~/.aws/**)",
			"Edit(//etc/**)", "Edit(//home/**)",
		},
	},
}

// concatStrings concatenates multiple string slices into one.
func concatStrings(slices ...[]string) []string {
	n := 0
	for _, s := range slices {
		n += len(s)
	}
	out := make([]string, 0, n)
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

// GetProfile returns the profile with the given name, or an error if not found.
func GetProfile(name string) (Profile, error) {
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown sandbox profile %q (valid: low, default, med, high)", name)
	}
	return p, nil
}

// domainToRegex converts a domain (possibly with *. prefix) into a regex
// pattern that matches HTTP/HTTPS URLs to that domain. For example:
//
//	"github.com"    → `^https?://([^/]*\.)?github\.com(/.*)?$`
//	"*.github.com"  → `^https?://([^/]*\.)?github\.com(/.*)?$`
func domainToRegex(domain string) string {
	// Strip wildcard prefix — both "*.x.com" and "x.com" produce the same
	// regex that allows subdomains.
	base := strings.TrimPrefix(domain, "*.")
	// Escape dots for regex.
	escaped := strings.ReplaceAll(base, ".", `\.`)
	return fmt.Sprintf(`^https?://([^/]*\.)?%s(/.*)?$`, escaped)
}

// newRuleID returns a random UUID v4 string for use as a proxy rule ID.
func newRuleID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// proxyRule builds a single proxy rule dict with all fields required by the
// proxy sidecar (id, rule_type, pattern, label, created_at, source).
func proxyRule(ruleType, pattern, label string) map[string]any {
	return map[string]any{
		"id":         newRuleID(),
		"rule_type":  ruleType,
		"pattern":    pattern,
		"label":      label,
		"created_at": float64(time.Now().Unix()),
		"source":     "profile",
	}
}

// ProxyRules converts the profile's AllowedDomains (plus any extraDomains)
// into proxy rule JSON dicts suitable for writing to a profile rules file.
// For profiles with no domains (e.g. "low"), a single wildcard allow-all rule
// is returned.
func (p Profile) ProxyRules(extraDomains []string) []map[string]any {
	domains := make([]string, len(p.AllowedDomains))
	copy(domains, p.AllowedDomains)
	domains = append(domains, extraDomains...)

	if len(domains) == 0 {
		return []map[string]any{proxyRule("allow", ".*", "allow-all")}
	}

	// Dedup: if both "x.com" and "*.x.com" exist, they produce the same
	// regex. Track base domains to avoid duplicates.
	seen := make(map[string]bool)
	var rules []map[string]any

	for _, d := range domains {
		base := strings.TrimPrefix(d, "*.")
		if seen[base] {
			continue
		}
		seen[base] = true
		rules = append(rules, proxyRule("allow", domainToRegex(d), base))
	}

	return rules
}

// ManagedSettingsForProxy generates settings for use with an external HTTP proxy.
// The sandbox is enabled with enableWeakerNestedSandbox for Docker environments
// where full bubblewrap sandboxing is unavailable. allowUnsandboxedCommands is
// true so commands still run if the weaker sandbox also fails. Network access
// control is handled by the proxy sidecar (allowedDomains: * with httpProxyPort).
// Permission allow/deny rules from the profile are merged with extraAllowPerms
// and extraDenyPerms. Non-yolo profiles get defaultMode "dontAsk" so permissions
// are enforced via the allow/deny lists without interactive prompts.
// defaultPackageNames are the tools available in every container by default.
var defaultPackageNames = []string{
	"bash", "coreutils", "git", "jq", "curl", "findutils", "grep", "sed",
	"gawk", "ripgrep", "fd", "tree", "diffutils", "tar", "gzip", "less",
	"file", "which", "python3", "nix", "devenv", "cachix", "direnv",
}

// containerInstructions generates the apiInstructions string that tells
// Claude what tools and packages are available in the container.
func containerInstructions(extraPackages []string) string {
	var b strings.Builder
	b.WriteString("## Container Environment\n")
	b.WriteString("You are running inside a Docker container managed by claude-container.\n\n")

	b.WriteString("### Available Tools\n")
	b.WriteString("Pre-installed: ")
	b.WriteString(strings.Join(defaultPackageNames, ", "))
	b.WriteString("\n")

	if len(extraPackages) > 0 {
		b.WriteString("Extra packages installed: ")
		b.WriteString(strings.Join(extraPackages, ", "))
		b.WriteString("\n")
	}

	b.WriteString("\n### Installing More Software\n")
	b.WriteString("This container uses Nix for package management:\n")
	b.WriteString("- `nix profile install nixpkgs#<package>` to install (e.g., nixpkgs#rustc nixpkgs#cargo)\n")
	b.WriteString("- `nix search nixpkgs <query>` to find packages\n")
	b.WriteString("- `nix profile list` to see installed packages\n")
	b.WriteString("- `nix profile remove <index>` to remove\n")
	b.WriteString("Do not use apt-get, yum, brew, or other package managers — they are not available.\n")
	b.WriteString("\n### Network Proxy and Held Requests\n")
	b.WriteString("All outbound network traffic goes through an HTTP/HTTPS proxy that enforces a domain allowlist.\n")
	b.WriteString("If a request to a domain is not in the allowlist, the proxy holds it until the user approves it via the dashboard in their browser.\n")
	b.WriteString("When a Bash or WebFetch tool call fails, a hook checks for held requests. If any are pending, the failure context will include a message starting with \"The proxy is holding the following request(s)\".\n")
	b.WriteString("When you see that message: STOP, surface the message to the user verbatim including the dashboard URL, and wait for them to approve in the browser before retrying. Do NOT attempt to approve, resolve, or curl the proxy yourself — you do not have the required auth token and any such attempt will fail and will be treated as a security issue.\n")
	b.WriteString("\n### Devenv Support\n")
	b.WriteString("devenv is pre-installed for declarative dev environments. If the workspace has a devenv.nix:\n")
	b.WriteString("- `devenv shell` to enter the dev environment\n")
	b.WriteString("- `devenv up` to start processes defined in devenv.nix\n")
	b.WriteString("- direnv is configured to auto-activate .envrc files in /workspace\n")

	return b.String()
}

func (p Profile) ManagedSettingsForProxy(httpProxyPort int, extraAllowPerms []string, extraDenyPerms []string, extraPackages []string) map[string]any {
	settings := map[string]any{
		"env": map[string]any{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_AUTOUPDATER":                      "1",
			// Bash tool timeout (default is 60s) — give the proxy time to
			// hold pending requests until the user approves them.
			"BASH_DEFAULT_TIMEOUT_MS": "120000",
			"BASH_MAX_TIMEOUT_MS":     "600000",
		},
		"cleanupPeriodDays":     14,
		"alwaysThinkingEnabled": true,
		"showTurnDuration":      true,
		"spinnerTipsEnabled":    false,
		"apiInstructions":       containerInstructions(extraPackages),
		"sandbox": map[string]any{
			"enabled":                   true,
			"autoAllowBashIfSandboxed":  true,
			"enableWeakerNestedSandbox": true,
			"allowUnsandboxedCommands":  true,
			"excludedCommands":          []string{"git"},
			"network": map[string]any{
				"allowedDomains": []string{"*"},
				"httpProxyPort":  httpProxyPort,
			},
		},
		// Hooks block: when a Bash or WebFetch tool call fails, check the
		// proxy for pending (held) requests and surface them to the user
		// via additionalContext. The hook never blocks and never approves;
		// it only tells the user to open the dashboard in a browser.
		"hooks": map[string]any{
			"PostToolUseFailure": []map[string]any{
				{
					"matcher": "Bash|WebFetch",
					"hooks": []map[string]any{
						{
							"type":    "command",
							"command": "/etc/claude-code/hooks/proxy-pending-hook.sh",
							"timeout": 10,
						},
					},
				},
			},
		},
	}

	// dontAsk mode enforces permissions via allow/deny lists without
	// interactive prompts. Required for all profiles in rootless Docker
	// where --dangerously-skip-permissions cannot be used (Claude refuses
	// it as root). Also correct for non-yolo profiles in standard Docker.
	settings["defaultMode"] = "dontAsk"

	// Build permissions block.
	perms := map[string]any{}

	allow := make([]string, 0, len(p.AllowPerms)+len(extraAllowPerms))
	allow = append(allow, p.AllowPerms...)
	allow = append(allow, extraAllowPerms...)
	if len(allow) > 0 {
		perms["allow"] = allow
	}

	deny := make([]string, 0, len(p.DenyPerms)+len(extraDenyPerms)+4)
	deny = append(deny, p.DenyPerms...)
	deny = append(deny, extraDenyPerms...)
	// Always block any attempt to talk directly to the proxy dashboard or
	// its resolve API. Claude must never approve its own held flows. The
	// dashboard is also auth-token-gated, but the deny rule short-circuits
	// the obvious shell paths so failures surface as permission errors
	// instead of silently authenticating.
	// Block any attempt to talk to the dashboard from inside the container.
	// In shared-netns mode the dashboard lives on loopback, so the deny
	// patterns target localhost rather than the old proxy container hostname.
	// The dashboard is also auth-token-gated; this just makes shell-level
	// failures surface as permission errors instead of silent rejects.
	deny = append(deny,
		"Bash(*127.0.0.1:8081*)",
		"Bash(*localhost:8081*)",
		"Bash(*api/resolve*)",
		"Bash(*api/rules*)",
		"Bash(*api/import*)",
	)
	perms["deny"] = deny

	if len(perms) > 0 {
		settings["permissions"] = perms
	}

	return settings
}
