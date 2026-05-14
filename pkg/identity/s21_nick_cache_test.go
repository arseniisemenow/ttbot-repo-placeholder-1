package identity_test

import (
	"testing"
	"time"

	"github.com/arseniisemenow/ttbot-core/pkg/identity"
)

func TestS21NickCacheHitWithinTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := identity.NewS21NickCache(7*24*time.Hour, func() time.Time { return now })
	c.Put(100, identity.User{TelegramID: 100, Nickname: "alice_s21", Found: true})
	now = now.Add(6 * 24 * time.Hour) // still inside the 7-day window
	u, ok := c.Get(100)
	if !ok || u.Nickname != "alice_s21" {
		t.Errorf("expected fresh hit; got u=%+v ok=%v", u, ok)
	}
}

func TestS21NickCacheMissAfterTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := identity.NewS21NickCache(7*24*time.Hour, func() time.Time { return now })
	c.Put(100, identity.User{TelegramID: 100, Nickname: "alice_s21", Found: true})
	now = now.Add(8 * 24 * time.Hour) // expired
	if _, ok := c.Get(100); ok {
		t.Errorf("expected expiry miss")
	}
}

func TestS21NickCacheMissOnUnknown(t *testing.T) {
	c := identity.NewS21NickCache(time.Hour, func() time.Time { return time.Now() })
	if _, ok := c.Get(999); ok {
		t.Errorf("expected miss on unknown tid")
	}
}

func TestS21NickCacheCachesNotFound(t *testing.T) {
	// A cached "no S21 nickname registered" still counts as a fresh hit.
	c := identity.NewS21NickCache(time.Hour, func() time.Time { return time.Now() })
	c.Put(100, identity.User{TelegramID: 100, Found: false})
	u, ok := c.Get(100)
	if !ok {
		t.Errorf("expected cached not-found to be a hit")
	}
	if u.Found {
		t.Errorf("expected Found=false, got %+v", u)
	}
}

func TestS21NickCacheInvalidate(t *testing.T) {
	c := identity.NewS21NickCache(time.Hour, func() time.Time { return time.Now() })
	c.Put(100, identity.User{TelegramID: 100, Nickname: "x", Found: true})
	c.Invalidate(100)
	if _, ok := c.Get(100); ok {
		t.Errorf("expected miss after invalidate")
	}
}
