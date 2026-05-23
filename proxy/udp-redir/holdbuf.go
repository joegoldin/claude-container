package main

import (
	"sync"
	"time"
)

// flowKey identifies the (dst, dns_name) tuple a packet is held under.
// For non-DNS flows DNSName is empty; equal-by-value tuples are equal as
// map keys.
type flowKey struct {
	DstIP   string
	DstPort uint16
	DNSName string
}

// HeldPacket is one queued kernel packet awaiting verdict, plus its
// expiration deadline.
type HeldPacket struct {
	ID        uint32
	ExpiresAt time.Time
}

// HoldBuf stores deferred-verdict packet IDs per flowKey with a per-tuple
// max size (FIFO eviction) and a wall-clock TTL.
type HoldBuf struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	pending map[flowKey][]HeldPacket
}

func newHoldBuf(max int, ttl time.Duration) *HoldBuf {
	return &HoldBuf{
		max:     max,
		ttl:     ttl,
		pending: make(map[flowKey][]HeldPacket),
	}
}

// Add stores a packet under key. Returns the ID of an evicted packet
// (FIFO) if the per-tuple max was already reached, otherwise 0.
func (b *HoldBuf) Add(key flowKey, id uint32) uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue := b.pending[key]
	var dropped uint32
	if len(queue) >= b.max {
		dropped = queue[0].ID
		queue = queue[1:]
	}
	queue = append(queue, HeldPacket{ID: id, ExpiresAt: time.Now().Add(b.ttl)})
	b.pending[key] = queue
	return dropped
}

// Drain removes and returns every held packet ID for the given key.
func (b *HoldBuf) Drain(key flowKey) []uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue := b.pending[key]
	delete(b.pending, key)
	ids := make([]uint32, len(queue))
	for i, p := range queue {
		ids[i] = p.ID
	}
	return ids
}

// EvictExpired removes every packet whose ExpiresAt has passed and
// returns the evicted entries. Caller is responsible for issuing a DROP
// verdict on each.
func (b *HoldBuf) EvictExpired() []HeldPacket {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	var expired []HeldPacket
	for key, queue := range b.pending {
		keep := queue[:0]
		for _, p := range queue {
			if now.After(p.ExpiresAt) {
				expired = append(expired, p)
			} else {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			delete(b.pending, key)
		} else {
			b.pending[key] = keep
		}
	}
	return expired
}

// List returns a snapshot of all held flowKeys (one entry per non-empty
// tuple, regardless of how many packets it holds). Used by /pending.
func (b *HoldBuf) List() []flowKey {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]flowKey, 0, len(b.pending))
	for key := range b.pending {
		out = append(out, key)
	}
	return out
}
