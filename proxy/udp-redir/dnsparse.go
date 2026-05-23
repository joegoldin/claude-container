package main

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// parseDNSQuestion reads the QNAME and QTYPE of the first question in a
// DNS query payload. Only RFC 1035 wire format (no compression in the
// question section — which the RFC actually forbids anyway). Returns
// the dotted name (e.g. "example.com") and the QTYPE.
func parseDNSQuestion(payload []byte) (string, uint16, error) {
	if len(payload) < 12 {
		return "", 0, fmt.Errorf("payload too short for DNS header: %d", len(payload))
	}
	qdcount := binary.BigEndian.Uint16(payload[4:6])
	if qdcount == 0 {
		return "", 0, fmt.Errorf("no questions in DNS payload")
	}
	// Skip the 12-byte header, then walk labels.
	i := 12
	var labels []string
	for i < len(payload) {
		n := int(payload[i])
		if n == 0 {
			i++
			break
		}
		// RFC 1035 §4.1.4: compression pointers MAY appear in answers/
		// authority/additional but MUST NOT appear in questions. We
		// reject pointer bytes here to keep the parser tight.
		if n&0xc0 != 0 {
			return "", 0, fmt.Errorf("unexpected compression pointer in question at offset %d", i)
		}
		if i+1+n > len(payload) {
			return "", 0, fmt.Errorf("label overrun at offset %d", i)
		}
		labels = append(labels, string(payload[i+1:i+1+n]))
		i += 1 + n
	}
	if i+4 > len(payload) {
		return "", 0, fmt.Errorf("truncated qtype/qclass at offset %d", i)
	}
	qtype := binary.BigEndian.Uint16(payload[i : i+2])
	return strings.Join(labels, "."), qtype, nil
}
