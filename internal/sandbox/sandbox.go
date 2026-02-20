package sandbox

import "fmt"

// Profile defines a sandbox security profile.
type Profile struct {
	Name           string
	SandboxEnabled bool
	AllowedDomains []string
	DenyPaths      []string
}

var profiles = map[string]Profile{
	"low": {
		Name:           "low",
		SandboxEnabled: false,
		AllowedDomains: nil,
		DenyPaths:      nil,
	},
	"med": {
		Name:           "med",
		SandboxEnabled: true,
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
		DenyPaths: []string{
			"Read(/etc/shadow)",
			"Read(/etc/passwd)",
			"Read(~/.ssh/**)",
			"Read(~/.aws/**)",
			"Read(~/.gnupg/**)",
		},
	},
	"high": {
		Name:           "high",
		SandboxEnabled: true,
		AllowedDomains: []string{
			"api.anthropic.com",
		},
		DenyPaths: []string{
			"Read(/etc/**)",
			"Read(/home/**)",
			"Read(/root/**)",
			"Read(/tmp/**)",
		},
	},
}

// GetProfile returns the profile with the given name, or an error if not found.
func GetProfile(name string) (Profile, error) {
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown sandbox profile %q (valid: low, med, high)", name)
	}
	return p, nil
}

// ManagedSettings generates a managed-settings map for this profile, with
// optional runtime overrides. extraDomains are added to allowedDomains.
// extraDenyPaths are wrapped as "Read(<path>)" and added to permissions.deny.
func (p Profile) ManagedSettings(extraDomains []string, extraDenyPaths []string) map[string]any {
	domains := make([]string, len(p.AllowedDomains))
	copy(domains, p.AllowedDomains)
	domains = append(domains, extraDomains...)

	deny := make([]string, len(p.DenyPaths))
	copy(deny, p.DenyPaths)
	for _, path := range extraDenyPaths {
		deny = append(deny, fmt.Sprintf("Read(%s)", path))
	}

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
			"enabled":                   p.SandboxEnabled,
			"autoAllowBashIfSandboxed":  true,
			"enableWeakerNestedSandbox": true,
			"allowUnsandboxedCommands":  false,
			"excludedCommands":          []string{"git"},
			"network": map[string]any{
				"allowedDomains": domains,
			},
		},
	}

	if len(deny) > 0 {
		settings["permissions"] = map[string]any{
			"deny": deny,
		}
	}

	return settings
}
