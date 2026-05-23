package main

import (
	"encoding/binary"
	"fmt"
	"net"
)

// UDPDatagram describes the 5-tuple and payload of a parsed IPv4 UDP packet.
type UDPDatagram struct {
	SrcIP   net.IP
	DstIP   net.IP
	SrcPort uint16
	DstPort uint16
	Payload []byte
}

// parseUDP4 parses an IPv4 packet carrying UDP, returning the 5-tuple
// and the UDP payload. Returns an error for non-UDP, IPv6, or truncated
// input. Header checksums are not verified (the kernel already did).
func parseUDP4(pkt []byte) (*UDPDatagram, error) {
	if len(pkt) < 20 {
		return nil, fmt.Errorf("packet too short for IPv4 header: %d bytes", len(pkt))
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || ihl > len(pkt) {
		return nil, fmt.Errorf("bad IHL %d in %d-byte packet", ihl, len(pkt))
	}
	if pkt[9] != 17 {
		return nil, fmt.Errorf("not UDP (proto=%d)", pkt[9])
	}
	if len(pkt) < ihl+8 {
		return nil, fmt.Errorf("packet too short for UDP header")
	}
	src := net.IPv4(pkt[12], pkt[13], pkt[14], pkt[15])
	dst := net.IPv4(pkt[16], pkt[17], pkt[18], pkt[19])
	udp := pkt[ihl:]
	sport := binary.BigEndian.Uint16(udp[0:2])
	dport := binary.BigEndian.Uint16(udp[2:4])
	udpLen := int(binary.BigEndian.Uint16(udp[4:6]))
	if udpLen < 8 || ihl+udpLen > len(pkt) {
		return nil, fmt.Errorf("bad UDP length %d", udpLen)
	}
	payload := udp[8:udpLen]
	return &UDPDatagram{
		SrcIP:   src,
		DstIP:   dst,
		SrcPort: sport,
		DstPort: dport,
		Payload: payload,
	}, nil
}
