package remote

import (
	"context"
	"sync"
	"time"
)

// State keeps what signed values can't carry themselves: which of them were
// revoked or already used. Production passes the database, so revocations
// survive a restart. A nil State keeps them in memory, for tests.
type State interface {
	// RevokedBefore is the time before which every session and token is void.
	RevokedBefore(ctx context.Context) (time.Time, error)
	// RevokeAll voids every session and token issued until now.
	RevokeAll(ctx context.Context) error
	// Mark records id until exp. It reports whether this call recorded it, and
	// when it was first recorded.
	Mark(ctx context.Context, id string, exp time.Time) (first bool, at time.Time, err error)
	// Marked reports whether id is recorded.
	Marked(ctx context.Context, id string) (bool, error)
}

type memState struct {
	mu    sync.Mutex
	cut   time.Time
	marks map[string][2]time.Time // first marked, expires
}

func newMemState() *memState { return &memState{marks: map[string][2]time.Time{}} }

func (m *memState) RevokedBefore(context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cut, nil
}

func (m *memState) RevokeAll(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cut = time.Now()
	return nil
}

func (m *memState) Mark(_ context.Context, id string, exp time.Time) (bool, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, v := range m.marks {
		if now.After(v[1]) {
			delete(m.marks, k)
		}
	}
	if v, ok := m.marks[id]; ok {
		return false, v[0], nil
	}
	m.marks[id] = [2]time.Time{now, exp}
	return true, now, nil
}

func (m *memState) Marked(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.marks[id]
	return ok && time.Now().Before(v[1]), nil
}
