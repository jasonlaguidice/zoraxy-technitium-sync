// Package status holds the small, transient "last reconcile cycle" snapshot
// shown by the plugin's UI. It has no persistence of its own: on restart it
// starts empty and is repopulated by the first reconcile cycle.
package status

import (
	"sync"
	"time"
)

// Snapshot is one point-in-time view of the reconcile loop's health.
type Snapshot struct {
	LastPollTime     time.Time `json:"last_poll_time"`
	LastSuccess      bool      `json:"last_success"`
	LastError        string    `json:"last_error,omitempty"`
	ManagedCount     int       `json:"managed_count"`
	Created          []string  `json:"created"`
	Updated          []string  `json:"updated"`
	Deleted          []string  `json:"deleted"`
	Skipped          []string  `json:"skipped"`
	DeletionsSkipped bool      `json:"deletions_skipped"`
}

// Store is a thread-safe holder for the latest Snapshot.
type Store struct {
	mu   sync.RWMutex
	snap Snapshot
}

func (s *Store) Set(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

func (s *Store) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}
