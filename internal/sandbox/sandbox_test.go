package sandbox

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestProfileNames(t *testing.T) {
	for _, name := range []string{"low", "default", "med", "high"} {
		if _, err := GetProfile(name); err != nil {
			t.Errorf("GetProfile(%q) error: %v", name, err)
		}
	}
}

func TestInvalidProfile(t *testing.T) {
	_, err := GetProfile("nonexistent")
	if err == nil {
		t.Fatal("GetProfile with invalid name should error")
	}
}

func TestManagedSettingsJSON(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty JSON output")
	}
}

func TestOverrideAddsDenyPerm(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsForProxy(8080, nil, []string{"Bash(rm *)"})
	perms := settings["permissions"].(map[string]any)
	deny := perms["deny"].([]string)
	found := false
	for _, d := range deny {
		if d == "Bash(rm *)" {
			found = true
		}
	}
	if !found {
		t.Errorf("Bash(rm *) not in deny list: %v", deny)
	}
}

// --- Yolo field tests ---

func TestYoloProfiles(t *testing.T) {
	for _, name := range []string{"low", "default"} {
		p, _ := GetProfile(name)
		if !p.Yolo {
			t.Errorf("profile %q should have Yolo=true", name)
		}
	}
	for _, name := range []string{"med", "high"} {
		p, _ := GetProfile(name)
		if p.Yolo {
			t.Errorf("profile %q should have Yolo=false", name)
		}
	}
}

// --- dontAsk mode tests ---

func TestDontAskModeForNonYolo(t *testing.T) {
	for _, name := range []string{"med", "high"} {
		p, _ := GetProfile(name)
		settings := p.ManagedSettingsForProxy(8080, nil, nil)
		mode, ok := settings["defaultMode"].(string)
		if !ok || mode != "dontAsk" {
			t.Errorf("profile %q: defaultMode = %v, want dontAsk", name, settings["defaultMode"])
		}
	}
}

func TestNoDontAskModeForYolo(t *testing.T) {
	for _, name := range []string{"low", "default"} {
		p, _ := GetProfile(name)
		settings := p.ManagedSettingsForProxy(8080, nil, nil)
		if _, has := settings["defaultMode"]; has {
			t.Errorf("profile %q: should not have defaultMode (yolo)", name)
		}
	}
}

// --- ManagedSettingsForProxy tests ---

func TestManagedSettingsForProxyWildcardDomains(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	sandbox := settings["sandbox"].(map[string]any)

	network, ok := sandbox["network"].(map[string]any)
	if !ok {
		t.Fatal("proxy settings should have network key")
	}

	domains := network["allowedDomains"].([]string)
	if len(domains) != 1 || domains[0] != "*" {
		t.Errorf("proxy settings allowedDomains = %v, want [*]", domains)
	}

	port, ok := network["httpProxyPort"].(int)
	if !ok || port != 8080 {
		t.Errorf("proxy settings httpProxyPort = %v, want 8080", network["httpProxyPort"])
	}

	if enabled, _ := sandbox["enabled"].(bool); !enabled {
		t.Error("sandbox should be enabled (weaker nested sandbox for Docker)")
	}
}

func TestManagedSettingsForProxyKeepsDenyPerms(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsForProxy(8080, nil, []string{"Bash(rm -rf *)"})
	perms := settings["permissions"].(map[string]any)
	deny := perms["deny"].([]string)

	foundOriginal := false
	foundExtra := false
	for _, d := range deny {
		if d == "Read(/etc/shadow)" {
			foundOriginal = true
		}
		if d == "Bash(rm -rf *)" {
			foundExtra = true
		}
	}
	if !foundOriginal {
		t.Errorf("missing original deny perm in %v", deny)
	}
	if !foundExtra {
		t.Errorf("missing extra deny perm in %v", deny)
	}
}

func TestManagedSettingsForProxyLowProfile(t *testing.T) {
	p, _ := GetProfile("low")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	sandbox := settings["sandbox"].(map[string]any)

	if enabled, _ := sandbox["enabled"].(bool); !enabled {
		t.Error("low profile sandbox should be enabled")
	}

	network := sandbox["network"].(map[string]any)
	if port, _ := network["httpProxyPort"].(int); port != 8080 {
		t.Errorf("proxy port = %d, want 8080", port)
	}

	if _, hasPerms := settings["permissions"]; hasPerms {
		t.Error("low profile with no extra deny perms should not have permissions key")
	}
}

func TestManagedSettingsForProxyDefaultProfile(t *testing.T) {
	p, _ := GetProfile("default")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	sandbox := settings["sandbox"].(map[string]any)

	if enabled, _ := sandbox["enabled"].(bool); !enabled {
		t.Error("default profile sandbox should be enabled")
	}

	// Default profile is yolo — no permissions key.
	if _, hasPerms := settings["permissions"]; hasPerms {
		t.Error("default profile (yolo) should not have permissions key")
	}

	// Should not have defaultMode (yolo profiles skip it).
	if _, has := settings["defaultMode"]; has {
		t.Error("default profile should not have defaultMode")
	}
}

// --- ProxyRules tests ---

func TestProxyRulesLow(t *testing.T) {
	p, _ := GetProfile("low")
	rules := p.ProxyRules(nil)

	if len(rules) != 1 {
		t.Fatalf("low profile: got %d rules, want 1", len(rules))
	}
	r := rules[0]
	if r["pattern"] != ".*" {
		t.Errorf("low profile pattern = %q, want .*", r["pattern"])
	}
	if r["label"] != "allow-all" {
		t.Errorf("low profile label = %q, want allow-all", r["label"])
	}
}

func TestProxyRulesDefault(t *testing.T) {
	p, _ := GetProfile("default")
	rules := p.ProxyRules(nil)

	// Default has the same domains as med — should produce domain rules, not wildcard.
	if len(rules) == 0 {
		t.Fatal("default profile should have proxy rules")
	}
	// Verify it's not a wildcard rule.
	if len(rules) == 1 && rules[0]["pattern"] == ".*" {
		t.Error("default profile should have domain-specific rules, not wildcard")
	}
}

func TestProxyRulesMed(t *testing.T) {
	p, _ := GetProfile("med")
	rules := p.ProxyRules(nil)

	if len(rules) == 0 {
		t.Fatal("med profile should have proxy rules")
	}

	for _, tc := range []struct {
		url   string
		match bool
	}{
		{"https://github.com/user/repo", true},
		{"https://api.github.com/repos", true},
		{"https://registry.npmjs.org/pkg", true},
		{"https://pypi.org/simple/", true},
		{"https://evil.com/steal", false},
	} {
		matched := false
		for _, r := range rules {
			pattern := r["pattern"].(string)
			re := regexp.MustCompile(pattern)
			if re.MatchString(tc.url) {
				matched = true
				break
			}
		}
		if matched != tc.match {
			t.Errorf("url %q: matched=%v, want %v", tc.url, matched, tc.match)
		}
	}
}

func TestProxyRulesHigh(t *testing.T) {
	p, _ := GetProfile("high")
	rules := p.ProxyRules(nil)

	if len(rules) != 1 {
		t.Fatalf("high profile: got %d rules, want 1", len(rules))
	}
	re := regexp.MustCompile(rules[0]["pattern"].(string))
	if !re.MatchString("https://api.anthropic.com/v1/messages") {
		t.Error("high profile rule should match api.anthropic.com")
	}
	if re.MatchString("https://github.com/") {
		t.Error("high profile rule should not match github.com")
	}
}

func TestProxyRulesExtraDomains(t *testing.T) {
	p, _ := GetProfile("high")
	rules := p.ProxyRules([]string{"custom.api.com"})

	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2 (anthropic + custom)", len(rules))
	}

	matched := false
	for _, r := range rules {
		re := regexp.MustCompile(r["pattern"].(string))
		if re.MatchString("https://custom.api.com/endpoint") {
			matched = true
		}
	}
	if !matched {
		t.Error("extra domain custom.api.com should match")
	}
}

// --- domainToRegex tests ---

func TestDomainToRegex(t *testing.T) {
	tests := []struct {
		domain string
		match  []string
		miss   []string
	}{
		{
			domain: "github.com",
			match:  []string{"https://github.com/", "https://api.github.com/repos", "http://github.com"},
			miss:   []string{"https://notgithub.com/", "https://evil.com/github.com"},
		},
		{
			domain: "*.github.com",
			match:  []string{"https://github.com/", "https://api.github.com/repos"},
			miss:   []string{"https://notgithub.com/"},
		},
		{
			domain: "api.anthropic.com",
			match:  []string{"https://api.anthropic.com/v1/messages"},
			miss:   []string{"https://anthropic.com/", "https://fake-api.anthropic.com.evil.com/"},
		},
	}

	for _, tc := range tests {
		pattern := domainToRegex(tc.domain)
		re := regexp.MustCompile(pattern)

		for _, url := range tc.match {
			if !re.MatchString(url) {
				t.Errorf("domainToRegex(%q) = %q: should match %q", tc.domain, pattern, url)
			}
		}
		for _, url := range tc.miss {
			if re.MatchString(url) {
				t.Errorf("domainToRegex(%q) = %q: should NOT match %q", tc.domain, pattern, url)
			}
		}
	}
}

// --- Permissions tests ---

func TestMedPermissionsAllow(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	perms := settings["permissions"].(map[string]any)

	allow, ok := perms["allow"].([]string)
	if !ok {
		t.Fatal("med profile should have permissions.allow")
	}

	wantRules := []string{"Bash(git *)", "Bash(npm *)", "Bash(curl *)", "Write(**)", "Edit(**)"}
	for _, want := range wantRules {
		found := false
		for _, a := range allow {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in permissions.allow: %v", want, allow)
		}
	}
}

func TestHighPermissionsDeny(t *testing.T) {
	p, _ := GetProfile("high")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	perms := settings["permissions"].(map[string]any)

	deny, ok := perms["deny"].([]string)
	if !ok {
		t.Fatal("high profile should have permissions.deny")
	}

	wantRules := []string{"Bash(curl *)", "Bash(wget *)", "Read(/etc/**)", "Edit(//etc/**)"}
	for _, want := range wantRules {
		found := false
		for _, d := range deny {
			if d == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in permissions.deny: %v", want, deny)
		}
	}
}

func TestHighPermissionsAllow(t *testing.T) {
	p, _ := GetProfile("high")
	settings := p.ManagedSettingsForProxy(8080, nil, nil)
	perms := settings["permissions"].(map[string]any)

	allow, ok := perms["allow"].([]string)
	if !ok {
		t.Fatal("high profile should have permissions.allow")
	}

	wantRules := []string{
		"Bash(git status *)", "Bash(ls *)", "Bash(cat *)",
		"Write(/workspace/**)", "Edit(/workspace/**)",
	}
	for _, want := range wantRules {
		found := false
		for _, a := range allow {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in permissions.allow: %v", want, allow)
		}
	}

	for _, a := range allow {
		if a == "Bash(git *)" {
			t.Error("high profile should not have broad Bash(git *)")
		}
	}
}

func TestExtraAllowPermsAdded(t *testing.T) {
	p, _ := GetProfile("med")
	extra := []string{"Bash(docker *)", "Bash(kubectl *)"}
	settings := p.ManagedSettingsForProxy(8080, extra, nil)
	perms := settings["permissions"].(map[string]any)
	allow := perms["allow"].([]string)

	for _, want := range extra {
		found := false
		for _, a := range allow {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in permissions.allow: %v", want, allow)
		}
	}
	// Original perms should still be present.
	for _, want := range []string{"Bash(git *)", "Bash(npm *)"} {
		found := false
		for _, a := range allow {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing original %q in permissions.allow: %v", want, allow)
		}
	}
}

func TestExtraAllowAndDenyPerms(t *testing.T) {
	p, _ := GetProfile("med")
	extraAllow := []string{"Bash(docker *)"}
	extraDeny := []string{"Bash(rm -rf *)"}
	settings := p.ManagedSettingsForProxy(8080, extraAllow, extraDeny)
	perms := settings["permissions"].(map[string]any)

	allow := perms["allow"].([]string)
	foundAllow := false
	for _, a := range allow {
		if a == "Bash(docker *)" {
			foundAllow = true
		}
	}
	if !foundAllow {
		t.Errorf("missing Bash(docker *) in allow: %v", allow)
	}

	deny := perms["deny"].([]string)
	foundDeny := false
	for _, d := range deny {
		if d == "Bash(rm -rf *)" {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Errorf("missing Bash(rm -rf *) in deny: %v", deny)
	}
}

func TestExtraAllowPermsOnYoloProfile(t *testing.T) {
	p, _ := GetProfile("default")
	extra := []string{"Bash(docker *)"}
	settings := p.ManagedSettingsForProxy(8080, extra, nil)

	// Yolo profiles normally have no permissions block, but adding extra
	// allow perms should create one.
	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("yolo profile with extra allow perms should have permissions block")
	}
	allow := perms["allow"].([]string)
	if len(allow) != 1 || allow[0] != "Bash(docker *)" {
		t.Errorf("allow = %v, want [Bash(docker *)]", allow)
	}
}

func TestProxyRulesDedup(t *testing.T) {
	p, _ := GetProfile("med")
	rules := p.ProxyRules(nil)

	githubCount := 0
	for _, r := range rules {
		label := r["label"].(string)
		if strings.Contains(label, "github.com") {
			githubCount++
		}
	}
	if githubCount != 1 {
		t.Errorf("expected 1 github.com rule after dedup, got %d", githubCount)
	}
}
