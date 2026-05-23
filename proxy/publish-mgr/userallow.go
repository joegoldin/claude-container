package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// userAllowBlacklist is the set of nft keywords that must NEVER appear
// in a user-supplied statement. Each maps to a word-boundary regex so
// "drops" in a hostname doesn't falsely match "drop".
var userAllowBlacklist = []string{
	"table", "flush", "delete", "chain", "policy", "drop", "reject",
}

// validateUserAllowStmtKeywordsOnly performs the cheap pass: only the
// keyword check. Tests can target this directly without needing the
// nftables binary in the environment. The full validator
// (validateUserAllowStmt) also pipes through `nft --check`.
func validateUserAllowStmtKeywordsOnly(stmt string) error {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return fmt.Errorf("user-allow stmt is empty")
	}
	for _, kw := range userAllowBlacklist {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(kw) + `\b`)
		if re.MatchString(stmt) {
			return fmt.Errorf("user-allow stmt contains forbidden keyword %q", kw)
		}
	}
	return nil
}

// validateUserAllowStmt runs the keyword check, then pipes the wrapped
// statement through `nft --check -f -` so malformed syntax is rejected
// before commit.
func validateUserAllowStmt(stmt string) error {
	if err := validateUserAllowStmtKeywordsOnly(stmt); err != nil {
		return err
	}
	wrapped := fmt.Sprintf("add rule inet claude_proxy_fw user_allow %s\n", stmt)
	cmd := exec.Command("nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(wrapped)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft --check rejected: %v: %s", err, out)
	}
	return nil
}

// nftAddUserAllow appends the validated statement to the user_allow
// chain. The validator must run BEFORE this — the helper does not
// re-validate (callers should fail fast on validation errors).
func nftAddUserAllow(stmt string) error {
	// Build argv by splitting the statement on whitespace.
	args := []string{"add", "rule", "inet", "claude_proxy_fw", "user_allow"}
	args = append(args, strings.Fields(stmt)...)
	cmd := exec.Command("nft", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft add user_allow rule failed: %v: %s", err, out)
	}
	return nil
}

// nftDelUserAllow finds the handle for a rule matching stmt and deletes
// it. Mirrors nftDelInputAccept's pattern. The needle is the trimmed
// statement followed by " # handle " to avoid false-positive matches.
func nftDelUserAllow(stmt string) error {
	out, err := exec.Command("nft", "-a", "list", "chain", "inet",
		"claude_proxy_fw", "user_allow").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft list user_allow: %v: %s", err, out)
	}
	needle := strings.TrimSpace(stmt) + " # handle "
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		i := strings.LastIndex(line, "handle ")
		if i < 0 {
			continue
		}
		h := strings.TrimSpace(line[i+len("handle "):])
		delCmd := exec.Command("nft", "delete", "rule", "inet",
			"claude_proxy_fw", "user_allow", "handle", h)
		if err := delCmd.Run(); err != nil {
			return fmt.Errorf("nft delete handle %s: %w", h, err)
		}
		return nil
	}
	return fmt.Errorf("no matching user_allow rule for %q", stmt)
}
