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
