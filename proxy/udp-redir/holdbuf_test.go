package main

import (
	"testing"
	"time"
)

func TestHoldBuf_StoresAndDrainsTuple(t *testing.T) {
	b := newHoldBuf(16, 30*time.Second)
	key := flowKey{DstIP: "1.1.1.1", DstPort: 53, DNSName: "example.com"}
	b.Add(key, 42)
	b.Add(key, 43)
	ids := b.Drain(key)
	if len(ids) != 2 || ids[0] != 42 || ids[1] != 43 {
		t.Errorf("Drain returned %v, want [42 43]", ids)
	}
	// After draining the tuple should be empty.
	if rest := b.Drain(key); len(rest) != 0 {
		t.Errorf("second Drain returned %v, want empty", rest)
	}
}

func TestHoldBuf_CapsAtMaxPerTuple(t *testing.T) {
	b := newHoldBuf(2, 30*time.Second)
	key := flowKey{DstIP: "1.1.1.1", DstPort: 53}
	dropped := b.Add(key, 1)
	if dropped != 0 {
		t.Errorf("first Add dropped %d, want 0", dropped)
	}
	b.Add(key, 2)
	dropped = b.Add(key, 3)
	if dropped != 1 {
		t.Errorf("third Add dropped %d, want 1 (FIFO eviction id=1)", dropped)
	}
	ids := b.Drain(key)
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Errorf("after eviction got %v, want [2 3]", ids)
	}
}

func TestHoldBuf_EvictExpired(t *testing.T) {
	b := newHoldBuf(16, 50*time.Millisecond)
	key := flowKey{DstIP: "1.1.1.1", DstPort: 53}
	b.Add(key, 7)
	time.Sleep(80 * time.Millisecond)
	expired := b.EvictExpired()
	if len(expired) != 1 || expired[0].ID != 7 {
		t.Errorf("EvictExpired returned %v, want [{ID:7}]", expired)
	}
	if rest := b.Drain(key); len(rest) != 0 {
		t.Errorf("after expiration, Drain returned %v", rest)
	}
}

func TestHoldBuf_ListSnapshot(t *testing.T) {
	b := newHoldBuf(16, 30*time.Second)
	b.Add(flowKey{DstIP: "1.1.1.1", DstPort: 53, DNSName: "example.com"}, 1)
	b.Add(flowKey{DstIP: "8.8.8.8", DstPort: 53}, 2)
	entries := b.List()
	if len(entries) != 2 {
		t.Fatalf("len=%d, want 2", len(entries))
	}
}
