package engine

import (
	"sync"
	"time"
)

// Memory is what the daemon keeps between passes: when each retry may run again (doubling
// from a pass to 30 minutes, so a failing 2 GB download is not fetched every 45 s) and which
// upload failures were announced. A nil Memory retries every pass and announces every time.
type Memory struct {
	mu     sync.Mutex
	next   map[string]time.Time
	wait   map[string]time.Duration
	warned map[string]bool
}

func NewMemory() *Memory {
	return &Memory{next: map[string]time.Time{}, wait: map[string]time.Duration{}, warned: map[string]bool{}}
}

func (m *Memory) due(id string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return !time.Now().Before(m.next[id])
}

// tried backs off each retry in ids still waiting after its attempt and forgets the rest.
func (m *Memory) tried(db *DB, workspace string, ids []string) error {
	if m == nil || len(ids) == 0 {
		return nil
	}
	left, err := db.Retries(workspace)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		still := false
		for _, f := range left {
			still = still || f.ID == id
		}
		if !still {
			delete(m.next, id)
			delete(m.wait, id)
			continue
		}
		m.wait[id] = min(max(2*m.wait[id], 45*time.Second), 30*time.Minute)
		m.next[id] = time.Now().Add(m.wait[id])
	}
	return nil
}

// once reports whether key is announced for the first time; clear forgets it.
func (m *Memory) once(key string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	first := !m.warned[key]
	m.warned[key] = true
	return first
}

func (m *Memory) clear(key string) {
	if m != nil {
		m.mu.Lock()
		delete(m.warned, key)
		m.mu.Unlock()
	}
}
