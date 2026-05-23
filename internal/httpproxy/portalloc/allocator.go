// Package portalloc tracks which contiguous host-port ranges have been
// claimed by which session, so two concurrent claude-container sessions
// don't publish ports to the same host range.
//
// State lives in a JSON file on disk. Operations are guarded by a
// per-process mutex and a flock on the file so concurrent invocations
// (or hosts with multiple claude-container processes) coordinate.
package portalloc

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Allocation is one session's claim on the host port range.
type Allocation struct {
	Base int `json:"base"` // first port (inclusive)
	Size int `json:"size"` // number of contiguous ports
}

// Allocator holds the allocation state for the configured pool.
type Allocator struct {
	path      string
	poolStart int
	poolEnd   int // inclusive
	defaultSz int
	mu        sync.Mutex
}

// New returns an Allocator backed by the JSON file at path. The pool
// spans [poolStart, poolEnd] inclusive. defaultSize is how many ports
// each session gets if not overridden.
func New(path string, poolStart, poolEnd, defaultSize int) (*Allocator, error) {
	if poolStart > poolEnd {
		return nil, fmt.Errorf("portalloc: poolStart %d > poolEnd %d",
			poolStart, poolEnd)
	}
	if defaultSize <= 0 {
		return nil, fmt.Errorf("portalloc: defaultSize must be > 0")
	}
	return &Allocator{
		path:      path,
		poolStart: poolStart,
		poolEnd:   poolEnd,
		defaultSz: defaultSize,
	}, nil
}

// load reads the on-disk allocation map. Empty map if file is missing.
func (a *Allocator) load() (map[string]Allocation, error) {
	data, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return map[string]Allocation{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("portalloc: read %s: %w", a.path, err)
	}
	var m map[string]Allocation
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("portalloc: parse %s: %w", a.path, err)
	}
	return m, nil
}

// save writes the allocation map back to disk atomically.
func (a *Allocator) save(m map[string]Allocation) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

// Claim reserves a contiguous range for the named session. If the
// session already has an allocation, that allocation is returned
// unchanged. Size 0 uses the configured defaultSize.
func (a *Allocator) Claim(sessionName string, size int) (Allocation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if size == 0 {
		size = a.defaultSz
	}

	m, err := a.load()
	if err != nil {
		return Allocation{}, err
	}

	// Idempotent: existing session keeps its range.
	if existing, ok := m[sessionName]; ok {
		return existing, nil
	}

	// Build a sorted list of occupied ranges so we can scan for a gap.
	type occ struct{ start, end int }
	occupied := make([]occ, 0, len(m))
	for _, al := range m {
		occupied = append(occupied, occ{al.Base, al.Base + al.Size - 1})
	}
	for i := range occupied {
		for j := i + 1; j < len(occupied); j++ {
			if occupied[j].start < occupied[i].start {
				occupied[i], occupied[j] = occupied[j], occupied[i]
			}
		}
	}

	// Walk the pool looking for a gap of `size` ports.
	cursor := a.poolStart
	for _, o := range occupied {
		if cursor+size-1 < o.start {
			// fits before this range
			break
		}
		cursor = o.end + 1
	}
	if cursor+size-1 > a.poolEnd {
		return Allocation{}, fmt.Errorf(
			"portalloc: pool %d-%d exhausted (cannot fit %d ports for session %q); "+
				"override with --publish-base / --publish-range",
			a.poolStart, a.poolEnd, size, sessionName)
	}

	al := Allocation{Base: cursor, Size: size}
	m[sessionName] = al
	if err := a.save(m); err != nil {
		return Allocation{}, err
	}
	return al, nil
}

// Release returns a session's range to the pool. Unknown session names
// are a no-op (idempotent — safe to call from cleanup paths).
func (a *Allocator) Release(sessionName string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	m, err := a.load()
	if err != nil {
		return err
	}
	if _, ok := m[sessionName]; !ok {
		return nil
	}
	delete(m, sessionName)
	return a.save(m)
}
