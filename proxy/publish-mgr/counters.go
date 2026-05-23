package main

import (
	"regexp"
	"strconv"
	"strings"
)

// CounterEntry is one rule's counter data, returned by /counters.
// `Stmt` is the rule body with the "counter packets N bytes M"
// fragment elided, so the dashboard can match it against the
// nft_statement field in the rule store.
type CounterEntry struct {
	Handle  string `json:"handle"`
	Stmt    string `json:"stmt"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// counterLineRE matches a single rule line like:
//   "        ip daddr 8.8.8.8 udp dport 53 counter packets 7 bytes 462 accept # handle 12"
// and captures the body before counter, the packets/bytes values, the
// verdict after, and the handle.
var counterLineRE = regexp.MustCompile(
	`^\s+(.+?)\s+counter packets (\d+) bytes (\d+)\s+(.+?)\s+#\s+handle\s+(\d+)\s*$`,
)

// parseCounters reads the text output of `nft -a list chain ...` and
// returns one CounterEntry per rule that has a `counter` statement.
// Lines without `counter` are ignored.
func parseCounters(out string) []CounterEntry {
	var entries []CounterEntry
	for _, line := range strings.Split(out, "\n") {
		m := counterLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		body := strings.TrimSpace(m[1])
		verdict := strings.TrimSpace(m[4])
		pkts, _ := strconv.ParseUint(m[2], 10, 64)
		byts, _ := strconv.ParseUint(m[3], 10, 64)
		entries = append(entries, CounterEntry{
			Handle:  strings.TrimSpace(m[5]),
			Stmt:    body + " " + verdict,
			Packets: pkts,
			Bytes:   byts,
		})
	}
	return entries
}
