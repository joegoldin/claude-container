package sandbox

import (
	"encoding/json"
	"testing"
)

func TestProfileNames(t *testing.T) {
	for _, name := range []string{"low", "med", "high"} {
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

func TestLowProfileDisablesSandbox(t *testing.T) {
	p, _ := GetProfile("low")
	settings := p.ManagedSettings(nil, nil)
	sandbox, ok := settings["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("low profile should have sandbox key")
	}
	if enabled, _ := sandbox["enabled"].(bool); enabled {
		t.Error("low profile sandbox.enabled should be false")
	}
}

func TestMedProfileEnablesSandbox(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, nil)
	sandbox := settings["sandbox"].(map[string]any)
	if enabled, _ := sandbox["enabled"].(bool); !enabled {
		t.Error("med profile sandbox.enabled should be true")
	}
}

func TestHighProfileMinimalNetwork(t *testing.T) {
	p, _ := GetProfile("high")
	settings := p.ManagedSettings(nil, nil)
	sandbox := settings["sandbox"].(map[string]any)
	network := sandbox["network"].(map[string]any)
	domains := network["allowedDomains"].([]string)
	if len(domains) != 1 || domains[0] != "api.anthropic.com" {
		t.Errorf("high profile allowedDomains = %v, want [api.anthropic.com]", domains)
	}
}

func TestOverrideAddsDomain(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings([]string{"custom.api.com"}, nil)
	sandbox := settings["sandbox"].(map[string]any)
	network := sandbox["network"].(map[string]any)
	domains := network["allowedDomains"].([]string)
	found := false
	for _, d := range domains {
		if d == "custom.api.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom.api.com not in allowedDomains: %v", domains)
	}
}

func TestOverrideAddsDenyPath(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, []string{"/secret"})
	perms := settings["permissions"].(map[string]any)
	deny := perms["deny"].([]string)
	found := false
	for _, d := range deny {
		if d == "Read(/secret)" {
			found = true
		}
	}
	if !found {
		t.Errorf("/secret not in deny list: %v", deny)
	}
}

func TestManagedSettingsJSON(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, nil)
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty JSON output")
	}
}

func TestManagedSettingsUnrestrictedNoNetwork(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsUnrestricted(nil)
	sandbox := settings["sandbox"].(map[string]any)

	// Should NOT have a "network" key
	if _, hasNetwork := sandbox["network"]; hasNetwork {
		t.Error("unrestricted settings should not have network key")
	}

	// Sandbox should still be enabled
	if enabled, _ := sandbox["enabled"].(bool); !enabled {
		t.Error("sandbox should still be enabled in unrestricted mode")
	}
}

func TestManagedSettingsUnrestrictedKeepsDenyPaths(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettingsUnrestricted([]string{"/extra/secret"})
	perms := settings["permissions"].(map[string]any)
	deny := perms["deny"].([]string)

	// Should have original deny paths + extra
	foundOriginal := false
	foundExtra := false
	for _, d := range deny {
		if d == "Read(/etc/shadow)" {
			foundOriginal = true
		}
		if d == "Read(/extra/secret)" {
			foundExtra = true
		}
	}
	if !foundOriginal {
		t.Errorf("missing original deny path in %v", deny)
	}
	if !foundExtra {
		t.Errorf("missing extra deny path in %v", deny)
	}
}

func TestManagedSettingsUnrestrictedLowProfile(t *testing.T) {
	p, _ := GetProfile("low")
	settings := p.ManagedSettingsUnrestricted(nil)
	sandbox := settings["sandbox"].(map[string]any)

	// Low profile has sandbox disabled
	if enabled, _ := sandbox["enabled"].(bool); enabled {
		t.Error("low profile sandbox should be disabled even in unrestricted mode")
	}

	// Should not have network key
	if _, hasNetwork := sandbox["network"]; hasNetwork {
		t.Error("unrestricted settings should not have network key")
	}

	// Low profile has no deny paths, so no permissions key
	if _, hasPerms := settings["permissions"]; hasPerms {
		t.Error("low profile with no extra deny paths should not have permissions key")
	}
}

func TestManagedSettingsVsUnrestrictedComparison(t *testing.T) {
	p, _ := GetProfile("med")
	restricted := p.ManagedSettings(nil, nil)
	unrestricted := p.ManagedSettingsUnrestricted(nil)

	// Both should have sandbox enabled
	rSandbox := restricted["sandbox"].(map[string]any)
	uSandbox := unrestricted["sandbox"].(map[string]any)

	if rSandbox["enabled"] != uSandbox["enabled"] {
		t.Error("sandbox.enabled should match between restricted and unrestricted")
	}

	// Restricted should have network key, unrestricted should not
	if _, has := rSandbox["network"]; !has {
		t.Error("restricted should have network key")
	}
	if _, has := uSandbox["network"]; has {
		t.Error("unrestricted should NOT have network key")
	}
}
