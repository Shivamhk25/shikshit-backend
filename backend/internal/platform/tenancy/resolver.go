package tenancy

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier is the part of a pgx pool or connection the Postgres resolver needs.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PgResolver resolves tenants with the resolve_tenant() database function, which works before login because
// it is SECURITY DEFINER and returns only public branding fields.
type PgResolver struct{ DB Querier }

// Resolve implements Resolver.
func (r PgResolver) Resolve(ctx context.Context, key string) (Tenant, error) {
	var t Tenant
	err := r.DB.QueryRow(ctx, `SELECT id::text, code::text, name, status, brand FROM resolve_tenant($1)`, key).
		Scan(&t.ID, &t.Code, &t.Name, &t.Status, &t.Brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	return t, err
}

// CachedResolver remembers lookups briefly so hot paths do not hit the database on every request.
// The key space is attacker-controlled (Host header), so the cache is bounded and misses are cached only
// for a short time.
type CachedResolver struct {
	Next        Resolver
	TTL         time.Duration // default 30s
	NegativeTTL time.Duration // default 5s
	MaxEntries  int           // default 10000; the cache is reset when exceeded
	Now         func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	tenant  Tenant
	err     error
	expires time.Time
}

// Resolve implements Resolver.
func (c *CachedResolver) Resolve(ctx context.Context, key string) (Tenant, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now().Before(e.expires) {
		c.mu.Unlock()
		return e.tenant, e.err
	}
	c.mu.Unlock()

	t, err := c.Next.Resolve(ctx, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return t, err // never cache infrastructure failures
	}

	ttl, negTTL, max := c.TTL, c.NegativeTTL, c.MaxEntries
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if negTTL <= 0 {
		negTTL = 5 * time.Second
	}
	if max <= 0 {
		max = 10000
	}
	exp := ttl
	if err != nil {
		exp = negTTL
	}
	c.mu.Lock()
	if c.entries == nil || len(c.entries) >= max {
		c.entries = make(map[string]cacheEntry)
	}
	c.entries[key] = cacheEntry{tenant: t, err: err, expires: now().Add(exp)}
	c.mu.Unlock()
	return t, err
}

// Invalidate drops one key (call after suspending, renaming or re-domaining an institute).
func (c *CachedResolver) Invalidate(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}
