package main

import "testing"

// A real DNS query for "example.com", QTYPE=A, QCLASS=IN.
// Captured from `dig example.com @1.1.1.1`.
func sampleDNSQuery() []byte {
	return []byte{
		0x12, 0x34, // ID
		0x01, 0x00, // flags: standard query, RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
		// QNAME: 7 "example" 3 "com" 0
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm',
		0x00,
		// QTYPE A, QCLASS IN
		0x00, 0x01,
		0x00, 0x01,
	}
}

func TestParseDNSQuestion_ReadsName(t *testing.T) {
	name, qtype, err := parseDNSQuestion(sampleDNSQuery())
	if err != nil {
		t.Fatalf("parseDNSQuestion: %v", err)
	}
	if name != "example.com" {
		t.Errorf("name=%q, want example.com", name)
	}
	if qtype != 1 {
		t.Errorf("qtype=%d, want 1 (A)", qtype)
	}
}

func TestParseDNSQuestion_RejectsShortPayload(t *testing.T) {
	if _, _, err := parseDNSQuestion([]byte{0x12, 0x34}); err == nil {
		t.Errorf("expected error for short payload, got nil")
	}
}

func TestParseDNSQuestion_RejectsZeroQuestions(t *testing.T) {
	pkt := sampleDNSQuery()
	pkt[4] = 0 // QDCOUNT high byte
	pkt[5] = 0 // QDCOUNT low byte → 0 questions
	if _, _, err := parseDNSQuestion(pkt); err == nil {
		t.Errorf("expected error when QDCOUNT=0, got nil")
	}
}
