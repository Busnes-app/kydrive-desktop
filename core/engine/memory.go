package engine

import (
	"context"
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

func (m *Memory) due(workspace, id string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return !time.Now().Before(m.next[workspace+"/"+id])
}

// tried backs off each retry in ids still waiting after its attempt and forgets the rest.
// Each completed entry updates/removes its wait and next together before cancellation yields.
func (m *Memory) tried(ctx context.Context, db *DB, workspace string, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || len(ids) == 0 {
		return nil
	}
	left, err := db.Retries(workspace)
	if err != nil {
		return err
	}
	remaining := make(map[string]bool, len(left))
	for _, f := range left {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining[f.ID] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := workspace + "/" + id
		if !remaining[id] {
			delete(m.next, key)
			delete(m.wait, key)
			continue
		}
		m.wait[key] = min(max(2*m.wait[key], 45*time.Second), 30*time.Minute)
		m.next[key] = time.Now().Add(m.wait[key])
	}
	return ctx.Err()
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
