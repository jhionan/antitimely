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
	ArmedProjects    map[int64]bool // project_id -> armed (needs focus before agent ticks count)

	// CwdPrefixes is the deduplicated set of cwd prefixes drawn from Rules
	// (trailing slash stripped). Built once per ReloadCache so the agent
	// pipeline can widen the binary allowlist by tracked-directory match
	// without rewalking Rules every tick.
	CwdPrefixes []string
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
		ArmedProjects:    map[int64]bool{},
	})
	return c
}

func (c *Cache) Snapshot() *CacheSnapshot { return c.ptr.Load() }
func (c *Cache) Store(s *CacheSnapshot)   { c.ptr.Store(s) }

// MarkProjectActive atomically swaps in a snapshot with projectID removed
// from PausedProjectIDs. No-op when the project isn't currently paused.
// Used by the pipeline's auto-resume path so a paused project's first
// detected agent signal both flips the DB flag and clears the in-memory
// pause set without a full ReloadCache.
func (c *Cache) MarkProjectActive(projectID int64) {
	for {
		cur := c.ptr.Load()
		if !cur.PausedProjectIDs[projectID] {
			return
		}
		next := *cur
		next.PausedProjectIDs = make(map[int64]bool, len(cur.PausedProjectIDs)-1)
		for k, v := range cur.PausedProjectIDs {
			if k != projectID {
				next.PausedProjectIDs[k] = v
			}
		}
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// ArmAllProjects atomically installs a fresh ArmedProjects map containing
// every supplied ID. An empty or nil slice is a no-op — used by the
// ProjectResumeAll RPC handler whose ListProjects call might degenerately
// return zero rows; silently disarming the world is exactly the failure
// mode this feature exists to prevent.
func (c *Cache) ArmAllProjects(ids []int64) {
	if len(ids) == 0 {
		return
	}
	for {
		cur := c.ptr.Load()
		next := *cur
		next.ArmedProjects = make(map[int64]bool, len(ids))
		for _, id := range ids {
			next.ArmedProjects[id] = true
		}
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// ArmProject atomically sets ArmedProjects[id]=true. Idempotent.
func (c *Cache) ArmProject(id int64) {
	for {
		cur := c.ptr.Load()
		if cur.ArmedProjects[id] {
			return
		}
		next := *cur
		next.ArmedProjects = make(map[int64]bool, len(cur.ArmedProjects)+1)
		for k, v := range cur.ArmedProjects {
			next.ArmedProjects[k] = v
		}
		next.ArmedProjects[id] = true
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// DisarmProject atomically removes id from ArmedProjects. No-op if absent.
func (c *Cache) DisarmProject(id int64) {
	for {
		cur := c.ptr.Load()
		if !cur.ArmedProjects[id] {
			return
		}
		next := *cur
		next.ArmedProjects = make(map[int64]bool, len(cur.ArmedProjects)-1)
		for k, v := range cur.ArmedProjects {
			if k != id {
				next.ArmedProjects[k] = v
			}
		}
		if c.ptr.CompareAndSwap(cur, &next) {
			return
		}
	}
}
