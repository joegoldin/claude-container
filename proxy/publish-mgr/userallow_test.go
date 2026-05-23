package main

import (
	"strings"
	"testing"
)

func TestValidateUserAllowStmt_RejectsBlacklistedKeywords(t *testing.T) {
	// Each case appends an `accept` verdict so the keyword check is
	// what fires — not the new accept-required guard. Verifies every
	// blacklisted keyword is detected when present as a whole word.
	cases := []struct {
		stmt    string
		wantErr string
	}{
		{"ip daddr 1.2.3.4 drop accept", "drop"},
		{"ip daddr 1.2.3.4 reject accept", "reject"},
		{"add chain inet foo accept", "chain"},
		{"delete rule inet claude_proxy_fw user_allow handle 1 accept", "delete"},
		{"flush chain inet claude_proxy_fw user_allow accept", "flush"},
		{"table inet evil ip daddr 1.2.3.4 accept", "table"},
		{"policy accept accept", "policy"},
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

// Adversarial: a `;` in the middle of a statement could be interpreted
// by `nft -f -` as a statement separator, chaining a second command
// (e.g. `insert rule inet claude_proxy_fw input ...`) past the keyword
// blacklist. We reject statements containing `;` outright.
func TestValidateUserAllowStmt_RejectsSemicolonInjection(t *testing.T) {
	cases := []string{
		"ip daddr 1.2.3.4 accept; insert rule inet claude_proxy_fw input ip daddr 0.0.0.0/0 accept",
		"ip daddr 1.2.3.4 accept ; insert rule inet claude_proxy_fw input accept",
		"ip daddr 1.2.3.4 accept;rename chain inet foo bar",
		// Semicolon at the end is also rejected for consistency.
		"ip daddr 1.2.3.4 accept;",
	}
	for _, stmt := range cases {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err == nil {
			t.Errorf("stmt %q passed validation but contains semicolon injection", stmt)
		}
	}
}

// Adversarial: same as semicolons but using newline as the separator.
// `nft -f -` reads multiple commands separated by newlines.
func TestValidateUserAllowStmt_RejectsNewlineInjection(t *testing.T) {
	cases := []string{
		"ip daddr 1.2.3.4 accept\ninsert rule inet claude_proxy_fw input ip daddr 0.0.0.0/0 accept",
		"ip daddr 1.2.3.4 accept\nrename rule ...",
		"ip daddr 1.2.3.4 accept\r\nflush ruleset",
	}
	for _, stmt := range cases {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err == nil {
			t.Errorf("stmt %q passed validation but contains newline injection", stmt)
		}
	}
}

// The user_allow chain is accept-only. Verdicts other than `accept`
// (jump/goto/return/log/queue) either redirect evaluation in ways the
// validator can't reason about (jump to a chain we don't control), or
// are useless inside an accept-only chain. Reject anything not ending
// in " accept".
func TestValidateUserAllowStmt_RequiresAcceptVerdict(t *testing.T) {
	cases := []string{
		"ip daddr 1.2.3.4 jump publish_in",
		"ip daddr 1.2.3.4 goto output",
		"ip daddr 1.2.3.4 return",
		"ip daddr 1.2.3.4 log",
		"ip daddr 1.2.3.4 queue num 0",
		"ip daddr 1.2.3.4", // no verdict at all
		"ip daddr 1.2.3.4 accepted", // doesn't end in word "accept" — ends in "accepted"
	}
	for _, stmt := range cases {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err == nil {
			t.Errorf("stmt %q passed validation but doesn't end in accept", stmt)
		}
	}
}

// Adversarial: non-printable bytes (NUL, ESC, etc.) are not valid nft
// syntax and could be used to confuse parsers. Reject.
func TestValidateUserAllowStmt_RejectsNonPrintable(t *testing.T) {
	cases := []string{
		"ip daddr 1.2.3.4 accept\x00",
		"ip\tdaddr\t1.2.3.4\taccept", // tab is non-printable in our policy
		"ip daddr 1.2.3.4 \x1baccept",
	}
	for _, stmt := range cases {
		if err := validateUserAllowStmtKeywordsOnly(stmt); err == nil {
			t.Errorf("stmt %q passed validation but contains non-printable bytes", stmt)
		}
	}
}
