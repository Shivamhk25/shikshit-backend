package authz

import (
	"context"
	"sync"
	"time"

	"github.com/pie360/backend/internal/platform/tenancy"
)

// Cache remembers resolved access so a permission check on every request does not cost five queries.
//
// The key includes the user's access_version, which the database bumps on every role, grant, override, scope or module
// change, so a change takes effect on the very next request. The TTL covers what a version bump cannot: a role or
// override that expires with time.
type Cache struct {
	Next Service
	TTL  time.Duration // default 60s
	Max  int           // default 10000 entries; the cache is reset when exceeded
	Now  func() time.Time

	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
}

type cacheKey struct {
	tenant, user string
	version      int64
}

type cacheEntry struct {
	access  Access
	expires time.Time
}

// Resolve always reads fresh data (used by /me/access, which must show the truth).
func (c *Cache) Resolve(ctx context.Context, userID string) (Access, error) {
	return c.Next.Resolve(ctx, userID)
}

// ResolveAt returns the access for the given version, from memory when it is still valid.
func (c *Cache) ResolveAt(ctx context.Context, userID string, version int64) (Access, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Access{}, tenancy.ErrNoTenant
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	key := cacheKey{t.ID, userID, version}
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now().Before(e.expires) {
		c.mu.Unlock()
		return e.access, nil
	}
	c.mu.Unlock()

	a, err := c.Next.Resolve(ctx, userID)
	if err != nil {
		return Access{}, err // never cache failures
	}
	ttl, max := c.TTL, c.Max
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	if max <= 0 {
		max = 10000
	}
	c.mu.Lock()
	if c.entries == nil || len(c.entries) >= max {
		c.entries = make(map[cacheKey]cacheEntry)
	}
	// Cache under the version we actually read, so a bump between authentication and this load is not masked.
	c.entries[cacheKey{t.ID, userID, a.Version}] = cacheEntry{a, now().Add(ttl)}
	c.mu.Unlock()
	return a, nil
}
