package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Rule mirrors the Phase 0 canonical schema written by RuleStore.to_dict
// in proxy/claude_proxy/rules.py. We only care about the fields used by
// the UDP matcher; extra fields are tolerated by the json decoder.
type Rule struct {
	ID        string         `json:"id"`
	Direction string         `json:"direction"`
	Proto     string         `json:"proto"`
	Match     map[string]any `json:"match"`
	Action    string         `json:"action"`
	Label     string         `json:"label"`
}

// loadRules reads a rules.json file and returns the parsed list.
func loadRules(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("udp-redir: read %s: %w", path, err)
	}
	var rs []Rule
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("udp-redir: parse %s: %w", path, err)
	}
	return rs, nil
}

// matchUDP evaluates the rule list against a UDP request descriptor.
// Mirrors RuleStore.match_request: deny rules first, then allow rules,
// "any" proto wildcards, direction must be "out". Returns "allow",
// "deny", or "" if no rule matches.
func matchUDP(rs []Rule, dstHost string, dstPort uint16, dnsName string) string {
	check := func(action string) string {
		for _, r := range rs {
			if r.Direction != "out" {
				continue
			}
			if r.Proto != "any" && r.Proto != "udp" {
				continue
			}
			if r.Action != action {
				continue
			}
			if !udpMatchFields(r.Match, dstHost, dstPort, dnsName) {
				continue
			}
			return action
		}
		return ""
	}
	if v := check("deny"); v != "" {
		return v
	}
	if v := check("allow"); v != "" {
		return v
	}
	return ""
}

// udpMatchFields evaluates the match object for a UDP request. Returns
// true only if every constraint present in m is satisfied. An empty m
// matches everything.
func udpMatchFields(m map[string]any, dstHost string, dstPort uint16, dnsName string) bool {
	if h, ok := m["host"].(string); ok && h != "" {
		if !strings.EqualFold(h, dstHost) {
			return false
		}
	}
	if hr, ok := m["host_regex"].(string); ok && hr != "" {
		re, err := regexp.Compile(hr)
		if err != nil {
			return false
		}
		if !re.MatchString(dstHost) {
			return false
		}
	}
	if p, ok := m["port"].(float64); ok && p != 0 {
		if uint16(p) != dstPort {
			return false
		}
	}
	if d, ok := m["dns_name"].(string); ok && d != "" {
		if !strings.EqualFold(d, dnsName) {
			return false
		}
	}
	return true
}
