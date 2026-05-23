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
