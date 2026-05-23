package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
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

// validateUserAllowStmtKeywordsOnly performs the cheap pass: separator
// rejection, accept-verdict requirement, and the keyword check. Tests
// can target this directly without needing the nftables binary in the
// environment. The full validator (validateUserAllowStmt) also pipes
// through `nft --check`.
//
// The separator check is a defense-in-depth guard: nft's `-f -` parser
// treats `;` and newlines as statement separators, which would let a
// crafted statement chain commands past the keyword blacklist (e.g.
// "... accept ; insert rule inet claude_proxy_fw input ip daddr 0/0
// accept"). The argv apply path doesn't honor `;` as a separator
// today, but we reject these upstream so the apply path's accidental
// safety isn't load-bearing.
//
// Statements MUST end in " accept" — the user_allow chain is
// accept-only by design, and any other verdict (jump/goto/return/log)
// is either useless or a way to redirect evaluation in unintended
// ways. The compile_template helper always emits "... accept"; raw
// mode users must do the same.
func validateUserAllowStmtKeywordsOnly(stmt string) error {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return fmt.Errorf("user-allow stmt is empty")
	}
	if strings.ContainsAny(stmt, ";\n\r") {
		return fmt.Errorf("user-allow stmt contains illegal separator (; or newline)")
	}
	// Reject any non-printable ASCII; valid nft syntax is plain text.
	for _, r := range stmt {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("user-allow stmt contains non-printable byte %q", r)
		}
	}
	if !strings.HasSuffix(stmt, " accept") {
		return fmt.Errorf("user-allow stmt must end in 'accept' verdict")
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
	// Insert "counter" before the final "accept" verdict so every
	// user_allow rule accumulates its own packet+byte counter. If the
	// statement doesn't end in `accept` (defensive — the validator
	// should have rejected it), fall through and let nft reject it.
	stmt = strings.TrimSpace(stmt)
	if strings.HasSuffix(stmt, " accept") {
		stmt = strings.TrimSuffix(stmt, " accept") + " counter accept"
	}
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
	// Strip the trailing "accept" so the needle tolerates "counter
	// packets N bytes M accept" between the rule body and "# handle".
	stmtNoAccept := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stmt), " accept"))
	handleMarker := " # handle "
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, stmtNoAccept) || !strings.Contains(line, handleMarker) {
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

// rulesFileRule is the minimal subset of the rules.json schema that
// startup replay needs. Phase 0's RuleStore.to_dict writes more fields,
// but Go's json.Decoder ignores extras silently.
type rulesFileRule struct {
	ID    string         `json:"id"`
	Proto string         `json:"proto"`
	Match map[string]any `json:"match"`
	Label string         `json:"label"`
}

// replayUserAllowFromRules reads rules.json at the given path and runs
// nftAddUserAllow for every entry that has match.nft_statement set.
// Errors are logged but do not abort startup — a single broken rule
// shouldn't take the proxy down.
//
// Returns the (id → entry) map for the caller to seed the manager.
func replayUserAllowFromRules(path string) (map[string]userAllowEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]userAllowEntry{}, nil
		}
		return nil, fmt.Errorf("publish-mgr: read %s: %w", path, err)
	}
	var rs []rulesFileRule
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("publish-mgr: parse %s: %w", path, err)
	}
	out := make(map[string]userAllowEntry)
	for _, r := range rs {
		stmt, _ := r.Match["nft_statement"].(string)
		if stmt == "" {
			continue
		}
		if err := validateUserAllowStmt(stmt); err != nil {
			log.Printf("publish-mgr: replay skip rule %s: %v", r.ID, err)
			continue
		}
		if err := nftAddUserAllow(stmt); err != nil {
			log.Printf("publish-mgr: replay apply rule %s: %v", r.ID, err)
			continue
		}
		out[r.ID] = userAllowEntry{
			ID:    r.ID,
			Stmt:  strings.TrimSpace(stmt),
			Label: r.Label,
		}
	}
	return out, nil
}
