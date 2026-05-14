// Package identity wraps the identity-service Go SDK for use inside ttbot.
// It adds a process-local cache so the SDK isn't hit on every match lookup.
package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	identityclient "github.com/arseniisemenow/s21-identity-client-go"
)

// User is the local view of an identity record. Mirrors identityclient.User
// but adds a `Found` flag so callers can express "no nickname registered"
// without a sentinel error.
type User struct {
	TelegramID    int64
	Nickname      string
	CampusID      string
	CampusName    string
	CoalitionName string
	Found         bool // false when there is no record for telegram_id
}

// DisplayName returns the nickname when present, falling back to a numeric
// "Player N" placeholder so messages always render something.
func (u User) DisplayName() string {
	if u.Nickname != "" {
		return u.Nickname
	}
	if u.TelegramID != 0 {
		return ""
	}
	return ""
}

// Service is the ttbot-side wrapper around identityclient.Client.
type Service struct {
	cli   *identityclient.Client
	mu    sync.RWMutex
	cache map[int64]User
}

// New constructs a Service. The caller passes the identity-service base URL,
// S21 credentials (ttbot's stored admin creds), and the optional read-scope
// API key from the IDENTITY_API_KEY env var. When the API key is empty the
// service still works against an identity-service running in dry-run mode
// (the bootstrap window between issuance and the operator flipping to
// enforcing).
func New(baseURL, s21Login, s21Password, apiKey string) *Service {
	opts := []identityclient.Option{}
	if apiKey != "" {
		opts = append(opts, identityclient.WithAPIKey(apiKey))
	}
	return &Service{
		cli:   identityclient.New(baseURL, s21Login, s21Password, opts...),
		cache: map[int64]User{},
	}
}

// GetByTelegram fetches the identity record for a telegram_id, using cache
// when present.
func (s *Service) GetByTelegram(ctx context.Context, telegramID int64) (User, error) {
	if u, ok := s.cacheLookup(telegramID); ok {
		return u, nil
	}
	u, err := s.cli.GetUserByTelegram(ctx, telegramID)
	if errors.Is(err, identityclient.ErrNotFound) {
		miss := User{TelegramID: telegramID, Found: false}
		s.cacheStore(miss)
		return miss, nil
	}
	if err != nil {
		return User{}, err
	}
	out := User{
		TelegramID:    u.TelegramID,
		Nickname:      u.Nickname,
		CampusID:      u.CampusID,
		CampusName:    u.CampusName,
		CoalitionName: u.CoalitionName,
		Found:         true,
	}
	s.cacheStore(out)
	return out, nil
}

// GetUsersByNickname resolves an S21 nickname (bare token in /match) to a
// list of telegram_ids, sorted earliest first. Returned slice may be empty.
//
// Per Q4, the same nickname may be claimed by multiple telegram_ids. Callers
// in ttbot pick the earliest (first element).
func (s *Service) GetUsersByNickname(ctx context.Context, nickname string) ([]User, error) {
	users, err := s.cli.GetUsersByNickname(ctx, nickname)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(users))
	for _, u := range users {
		out = append(out, User{
			TelegramID:    u.TelegramID,
			Nickname:      u.Nickname,
			CampusID:      u.CampusID,
			CampusName:    u.CampusName,
			CoalitionName: u.CoalitionName,
			Found:         true,
		})
	}
	return out, nil
}

// Flush clears the local cache. Used by ttbot's /refresh_identity command
// after a nickname change in the identity bot.
func (s *Service) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = map[int64]User{}
}

func (s *Service) cacheLookup(tid int64) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.cache[tid]
	return u, ok
}

func (s *Service) cacheStore(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[u.TelegramID] = u
}

// Unused imports below — kept to surface signature drift early.
var (
	_ = strings.Contains
	_ = time.Now
)
