package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRules(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	return p
}

func TestLoadRules_ParsesNewShape(t *testing.T) {
	dir := t.TempDir()
	p := writeRules(t, dir, `[
		{"id":"1","direction":"out","proto":"udp","match":{"host":"1.1.1.1"},"action":"allow"}
	]`)
	rs, err := loadRules(p)
	if err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("len=%d, want 1", len(rs))
	}
	if rs[0].Action != "allow" || rs[0].Proto != "udp" || rs[0].Match["host"] != "1.1.1.1" {
		t.Errorf("unexpected rule: %+v", rs[0])
	}
}

func TestMatchUDP_ProtoExact(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
	}
	got := matchUDP(rs, "1.1.1.1", 53, "")
	if got != "allow" {
		t.Errorf("got %q, want allow", got)
	}
	// TCP-only rule must NOT match UDP.
	rs[0].Proto = "tcp"
	if got := matchUDP(rs, "1.1.1.1", 53, ""); got != "" {
		t.Errorf("tcp-only rule matched UDP: %q", got)
	}
}

func TestMatchUDP_AnyProto(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "any", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
	}
	if got := matchUDP(rs, "1.1.1.1", 53, ""); got != "allow" {
		t.Errorf("got %q, want allow", got)
	}
}

func TestMatchUDP_DenyBeatsAllow(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "deny"},
	}
	if got := matchUDP(rs, "1.1.1.1", 53, ""); got != "deny" {
		t.Errorf("got %q, want deny", got)
	}
}

func TestMatchUDP_DNSName(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"dns_name": "example.com"}, Action: "allow"},
	}
	if got := matchUDP(rs, "1.1.1.1", 53, "example.com"); got != "allow" {
		t.Errorf("got %q, want allow", got)
	}
	if got := matchUDP(rs, "1.1.1.1", 53, "evil.com"); got != "" {
		t.Errorf("got %q, want empty (no match)", got)
	}
}

func TestMatchUDP_NoMatchReturnsEmpty(t *testing.T) {
	rs := []Rule{
		{Direction: "out", Proto: "udp", Match: map[string]any{"host": "1.1.1.1"}, Action: "allow"},
	}
	if got := matchUDP(rs, "8.8.8.8", 53, ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
