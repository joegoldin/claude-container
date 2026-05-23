package main

import (
	"strings"
	"testing"
)

func TestValidateUserAllowStmt_RejectsBlacklistedKeywords(t *testing.T) {
	cases := []struct {
		stmt    string
		wantErr string
	}{
		{"ip daddr 1.2.3.4 drop", "drop"},
		{"ip daddr 1.2.3.4 reject", "reject"},
		{"add chain inet foo bar", "chain"},
		{"delete rule inet claude_proxy_fw user_allow handle 1", "delete"},
		{"flush chain inet claude_proxy_fw user_allow", "flush"},
		{"table inet evil { chain x { policy drop; } }", "table"},
		{"policy accept", "policy"},
	}
	for _, c := range cases {
		err := validateUserAllowStmtKeywordsOnly(c.stmt)
		if err == nil {
			t.Errorf("stmt %q: expected error containing %q, got nil", c.stmt, c.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("stmt %q: error %q does not mention %q", c.stmt, err, c.wantErr)
		}
	}
}

func TestValidateUserAllowStmt_AcceptsSafeStatements(t *testing.T) {
	ok := []string{
		"ip daddr 192.168.1.0/24 icmp type echo-request accept",
		"ip saddr 10.0.0.0/8 tcp dport 22 accept",
		"ip daddr 8.8.8.8 udp dport 53 accept",
		"ip daddr 1.1.1.1 accept",
	}
	for _, stmt := range ok {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err != nil {
			t.Errorf("stmt %q rejected unexpectedly: %v", stmt, err)
		}
	}
}

func TestValidateUserAllowStmt_WordBoundary(t *testing.T) {
	// "accept" is not on the blacklist; "drops" should not falsely
	// match "drop"; "tablespoon" should not falsely match "table".
	cases := []string{
		"ip daddr 1.2.3.4 accept",          // "accept" appears, fine
		"ip daddr drops.example.com accept", // hostname containing "drops"
	}
	for _, stmt := range cases {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err != nil {
			t.Errorf("stmt %q rejected unexpectedly: %v", stmt, err)
		}
	}
}

func TestValidateUserAllowStmt_EmptyRejected(t *testing.T) {
	if err := validateUserAllowStmtKeywordsOnly(""); err == nil {
		t.Errorf("empty stmt should be rejected")
	}
	if err := validateUserAllowStmtKeywordsOnly("   "); err == nil {
		t.Errorf("whitespace-only stmt should be rejected")
	}
}
