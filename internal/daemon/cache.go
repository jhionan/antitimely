package daemon

import (
	"sync/atomic"

	"github.com/rian/antitimely/internal/domain"
)

// CacheSnapshot is the immutable in-memory view of the allowlist and rules
// that the polling loop reads on each tick. Swapped atomically by mutation
// RPC handlers.
type CacheSnapshot struct {
	AllowedBundles   map[string]bool
	AllowedBinaries  map[string]bool
	Rules            []domain.RuleSpec
	PausedProjectIDs map[int64]bool // project_id -> paused
}

// Cache holds the current snapshot with lock-free read access.
type Cache struct {
	ptr atomic.Pointer[CacheSnapshot]
}

// NewCache returns a Cache containing an empty snapshot.
func NewCache() *Cache {
	c := &Cache{}
	c.ptr.Store(&CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{},
		Rules:            nil,
		PausedProjectIDs: map[int64]bool{},
	})
	return c
}

func (c *Cache) Snapshot() *CacheSnapshot { return c.ptr.Load() }
func (c *Cache) Store(s *CacheSnapshot)   { c.ptr.Store(s) }
