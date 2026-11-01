// Package state keeps the latest value of every state UUID the Miniserver publishes, and lets
// readers wait for the next change.
package state

import (
	"sync"
	"time"
)

// Value is the latest value of one state UUID: a number from a value event, or a text from a
// text event.
type Value struct {
	Number  float64
	Text    string
	IsText  bool
	Updated time.Time
}

// Store is safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	vals    map[string]Value
	changed chan struct{}
	now     func() time.Time
}

// New returns an empty store. now may be nil for time.Now.
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{vals: map[string]Value{}, changed: make(chan struct{}), now: now}
}

// SetNumber records a value event.
func (s *Store) SetNumber(uuid string, v float64) {
	s.set(uuid, Value{Number: v})
}

// SetText records a text event.
func (s *Store) SetText(uuid, text string) {
	s.set(uuid, Value{Text: text, IsText: true})
}

func (s *Store) set(uuid string, v Value) {
	v.Updated = s.now()
	s.mu.Lock()
	s.vals[uuid] = v
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

// Get returns the value of a state UUID.
func (s *Store) Get(uuid string) (Value, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vals[uuid]
	return v, ok
}

// Len is the number of known state UUIDs.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.vals)
}

// Changed returns a channel that is closed at the next update.
func (s *Store) Changed() <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.changed
}
