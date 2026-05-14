package identity

import (
	"sync"
	"time"
)

// S21NickCache is a process-local cache of S21 nickname records keyed by
// Telegram ID. Entries are valid for `ttl` from the moment they were
// inserted; older entries are treated as cache misses (the next lookup
// fetches fresh from the identity service).
//
// The "S21" prefix in every name in this file is intentional: ttbot deals
// with two unrelated kinds of "nickname" — S21 nicknames (the school's user
// identifier, looked up via the identity service) and Telegram @usernames
// (cached in the per-group participants table). This cache only covers the
// former. Anything named "Nick"/"Nickname" in this file means an S21
// nickname; a Telegram username goes by its own name elsewhere.
type S21NickCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	now     func() time.Time
	entries map[int64]s21NickEntry
}

type s21NickEntry struct {
	user      User
	fetchedAt time.Time
}

// NewS21NickCache constructs a cache. `now` is injectable for tests; pass
// time.Now in production.
func NewS21NickCache(ttl time.Duration, now func() time.Time) *S21NickCache {
	if now == nil {
		now = time.Now
	}
	return &S21NickCache{
		ttl:     ttl,
		now:     now,
		entries: map[int64]s21NickEntry{},
	}
}

// Get returns the cached identity.User for tid if present and still fresh.
// The bool is false on miss or on expiry — callers should then fetch + Put.
// A cached "not found" record (User.Found == false) is treated as a fresh
// hit too: identity service has already told us this telegram_id has no
// S21 nickname, no reason to ask again until the TTL passes.
func (c *S21NickCache) Get(tid int64) (User, bool) {
	c.mu.RLock()
	e, ok := c.entries[tid]
	c.mu.RUnlock()
	if !ok {
		return User{}, false
	}
	if c.now().Sub(e.fetchedAt) > c.ttl {
		return User{}, false
	}
	return e.user, true
}

// Put stores u in the cache, stamping fetchedAt to now.
func (c *S21NickCache) Put(tid int64, u User) {
	c.mu.Lock()
	c.entries[tid] = s21NickEntry{user: u, fetchedAt: c.now()}
	c.mu.Unlock()
}

// Invalidate removes a single entry. Reserved for the future case where the
// bot gets a strong signal a record changed (e.g. a webhook from the
// identity bot announcing a /provide_nickname). Not currently invoked.
func (c *S21NickCache) Invalidate(tid int64) {
	c.mu.Lock()
	delete(c.entries, tid)
	c.mu.Unlock()
}

// Size returns the number of entries currently held. Used by tests; not
// part of the production hot path.
func (c *S21NickCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
