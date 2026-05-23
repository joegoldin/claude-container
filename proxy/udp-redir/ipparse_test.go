package main

import (
	"net"
	"testing"
)

// Hand-built IPv4 packet: 20-byte IPv4 header + 8-byte UDP header + 4 bytes payload.
//
// IPv4: version=4, IHL=5, total length 32, proto=17 (UDP),
//       src 10.0.0.5, dst 8.8.8.8
// UDP : sport 5555, dport 53, length 12 (8 header + 4 payload), no csum
// Payload: "ping"
func sampleDNSish() []byte {
	pkt := []byte{
		0x45, 0x00, 0x00, 0x20, // ver/ihl, dscp, total length 32
		0x00, 0x00, 0x00, 0x00, // id, flags/frag
		0x40, 0x11, 0x00, 0x00, // ttl=64, proto=17 (UDP), header csum (ignored)
		10, 0, 0, 5, // src
		8, 8, 8, 8, // dst
		// UDP
		0x15, 0xb3, // sport 5555
		0x00, 0x35, // dport 53
		0x00, 0x0c, // length 12
		0x00, 0x00, // csum (ignored)
		'p', 'i', 'n', 'g',
	}
	return pkt
}

func TestParseUDP4_ExtractsTuple(t *testing.T) {
	got, err := parseUDP4(sampleDNSish())
	if err != nil {
		t.Fatalf("parseUDP4: %v", err)
	}
	if !got.SrcIP.Equal(net.IPv4(10, 0, 0, 5)) {
		t.Errorf("SrcIP=%v, want 10.0.0.5", got.SrcIP)
	}
	if !got.DstIP.Equal(net.IPv4(8, 8, 8, 8)) {
		t.Errorf("DstIP=%v, want 8.8.8.8", got.DstIP)
	}
	if got.SrcPort != 5555 {
		t.Errorf("SrcPort=%d, want 5555", got.SrcPort)
	}
	if got.DstPort != 53 {
		t.Errorf("DstPort=%d, want 53", got.DstPort)
	}
	if string(got.Payload) != "ping" {
		t.Errorf("Payload=%q, want ping", got.Payload)
	}
}

func TestParseUDP4_RejectsNonUDP(t *testing.T) {
	pkt := sampleDNSish()
	pkt[9] = 6 // proto = TCP
	if _, err := parseUDP4(pkt); err == nil {
		t.Errorf("expected error for non-UDP proto, got nil")
	}
}

func TestParseUDP4_RejectsShortPacket(t *testing.T) {
	if _, err := parseUDP4([]byte{0x45, 0, 0, 0}); err == nil {
		t.Errorf("expected error for short packet, got nil")
	}
}
