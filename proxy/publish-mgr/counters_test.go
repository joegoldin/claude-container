package main

import "testing"

// Sample output from `nft -a list chain inet claude_proxy_fw user_allow`,
// trimmed to the chain body.
const sampleUserAllowChain = `table inet claude_proxy_fw {
	chain user_allow { # handle 9
		ip daddr 8.8.8.8 udp dport 53 counter packets 7 bytes 462 accept # handle 12
		ip daddr 1.1.1.1 tcp dport 443 counter packets 0 bytes 0 accept # handle 13
		ip daddr 10.0.0.0/8 icmp type echo-request counter packets 3 bytes 252 accept # handle 14
	}
}`

func TestParseCounters_ExtractsAllRules(t *testing.T) {
	got := parseCounters(sampleUserAllowChain)
	if len(got) != 3 {
		t.Fatalf("got %d counters, want 3: %+v", len(got), got)
	}
	if got[0].Handle != "12" {
		t.Errorf("got[0].Handle=%q, want 12", got[0].Handle)
	}
	if got[0].Packets != 7 || got[0].Bytes != 462 {
		t.Errorf("got[0]=%+v, want packets=7 bytes=462", got[0])
	}
	if got[1].Handle != "13" {
		t.Errorf("got[1].Handle=%q, want 13", got[1].Handle)
	}
	if got[1].Packets != 0 || got[1].Bytes != 0 {
		t.Errorf("got[1] packets/bytes not zero: %+v", got[1])
	}
}

func TestParseCounters_ExtractsStatement(t *testing.T) {
	got := parseCounters(sampleUserAllowChain)
	if got[0].Stmt != "ip daddr 8.8.8.8 udp dport 53 accept" {
		t.Errorf("got[0].Stmt=%q, want canonical form without counter", got[0].Stmt)
	}
}

func TestParseCounters_IgnoresLinesWithoutCounter(t *testing.T) {
	chain := `table inet claude_proxy_fw {
	chain other { # handle 1
		ip daddr 1.2.3.4 accept # handle 2
	}
}`
	if got := parseCounters(chain); len(got) != 0 {
		t.Errorf("expected 0 (no counter on these lines), got %+v", got)
	}
}

func TestParseCounters_EmptyInput(t *testing.T) {
	if got := parseCounters(""); len(got) != 0 {
		t.Errorf("expected 0, got %+v", got)
	}
}
