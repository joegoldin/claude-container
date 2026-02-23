package sandbox

import (
	"fmt"
	"strings"
)

// Profile defines a sandbox security profile.
type Profile struct {
	Name           string
	Yolo           bool     // use --dangerously-skip-permissions
	AllowedDomains []string // for proxy rules
	AllowPerms     []string // permissions.allow rules
	DenyPerms      []string // permissions.deny rules
}

var profiles = map[string]Profile{
	"low": {
		Name:           "low",
		Yolo:           true,
		AllowedDomains: nil, // proxy: wildcard allow-all
		AllowPerms:     nil,
		DenyPerms:      nil,
	},
	"default": {
		Name: "default",
		Yolo: true,
		AllowedDomains: []string{
			"api.anthropic.com",
			"statsig.anthropic.com",
			"sentry.io",
			"github.com",
			"*.github.com",
			"*.npmjs.org",
			"registry.npmjs.org",
			"registry.yarnpkg.com",
			"pypi.org",
			"*.pypi.org",
			"files.pythonhosted.org",
		},
		AllowPerms: nil, // yolo — no permission rules
		DenyPerms:  nil,
	},
	"med": {
		Name: "med",
		Yolo: false,
		AllowedDomains: []string{
			"api.anthropic.com",
			"statsig.anthropic.com",
			"sentry.io",
			"github.com",
			"*.github.com",
			"*.npmjs.org",
			"registry.npmjs.org",
			"registry.yarnpkg.com",
			"pypi.org",
			"*.pypi.org",
			"files.pythonhosted.org",
		},
		AllowPerms: []string{
			"Bash(git *)", "Bash(npm *)", "Bash(npx *)",
			"Bash(pip *)", "Bash(python *)", "Bash(node *)",
			"Bash(cargo *)", "Bash(go *)", "Bash(make *)",
			"Bash(ls *)", "Bash(cat *)", "Bash(grep *)",
			"Bash(find *)", "Bash(curl *)", "Bash(wget *)",
			"Write(**)", "Edit(**)",
		},
		DenyPerms: []string{
			"Read(~/.ssh/**)", "Read(~/.aws/**)", "Read(~/.gnupg/**)",
			"Read(/etc/shadow)", "Read(/etc/passwd)",
		},
	},
	"high": {
		Name: "high",
		Yolo: false,
		AllowedDomains: []string{"api.anthropic.com"},
		AllowPerms: []string{
			"Bash(git status *)", "Bash(git diff *)", "Bash(git log *)",
			"Bash(ls *)", "Bash(cat *)",
			"Write(/workspace/**)", "Edit(/workspace/**)",
		},
		DenyPerms: []string{
			"Bash(curl *)", "Bash(wget *)",
			"Read(/etc/**)", "Read(~/.ssh/**)", "Read(~/.aws/**)",
			"Edit(//etc/**)", "Edit(//home/**)",
		},
	},
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

// ProxyRules converts the profile's AllowedDomains (plus any extraDomains)
// into proxy rule JSON dicts suitable for writing to a profile rules file.
// For profiles with no domains (e.g. "low"), a single wildcard allow-all rule
// is returned.
func (p Profile) ProxyRules(extraDomains []string) []map[string]any {
	domains := make([]string, len(p.AllowedDomains))
	copy(domains, p.AllowedDomains)
	domains = append(domains, extraDomains...)

	if len(domains) == 0 {
		return []map[string]any{{
			"rule_type": "allow",
			"pattern":   ".*",
			"label":     "allow-all",
			"source":    "profile",
		}}
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

		rules = append(rules, map[string]any{
			"rule_type": "allow",
			"pattern":   domainToRegex(d),
			"label":     base,
			"source":    "profile",
		})
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
func (p Profile) ManagedSettingsForProxy(httpProxyPort int, extraAllowPerms []string, extraDenyPerms []string) map[string]any {
	settings := map[string]any{
		"env": map[string]any{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_AUTOUPDATER":                      "1",
		},
		"cleanupPeriodDays":     14,
		"alwaysThinkingEnabled": true,
		"showTurnDuration":      true,
		"spinnerTipsEnabled":    false,
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
	}

	// Non-yolo profiles use dontAsk mode — permissions enforced by allow/deny
	// lists without interactive prompts.
	if !p.Yolo {
		settings["defaultMode"] = "dontAsk"
	}

	// Build permissions block.
	perms := map[string]any{}

	allow := make([]string, 0, len(p.AllowPerms)+len(extraAllowPerms))
	allow = append(allow, p.AllowPerms...)
	allow = append(allow, extraAllowPerms...)
	if len(allow) > 0 {
		perms["allow"] = allow
	}

	deny := make([]string, 0, len(p.DenyPerms)+len(extraDenyPerms))
	deny = append(deny, p.DenyPerms...)
	deny = append(deny, extraDenyPerms...)
	if len(deny) > 0 {
		perms["deny"] = deny
	}

	if len(perms) > 0 {
		settings["permissions"] = perms
	}

	return settings
}
