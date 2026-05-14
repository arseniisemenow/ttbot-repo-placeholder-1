// Package memstore is an in-memory implementation of the store.Store interface.
// It is the test-time backing used by the testkit and unit tests of handlers.
//
// Concurrency: every operation takes the global mutex. Sufficient for tests.
package memstore

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	s21account "github.com/arseniisemenow/s21-account-go"

	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// Store is the in-memory store.Store.
type Store struct {
	mu sync.Mutex

	participants  map[participantKey]models.Participant
	admins        map[int64]models.Admin
	s21Accounts   map[int64]s21account.S21Account
	groups        map[int64]models.Group
	matches       map[matchKey]models.Match
	confirmations map[confirmKey]models.MatchConfirmation
	undos         map[undoKey]models.UndoCommand
	settings      map[string]models.BotSetting
	matchCounters map[int64]uint64
}

type participantKey struct{ Group, User int64 }
type matchKey struct {
	Group int64
	Match uint64
}
type confirmKey struct {
	Group int64
	Match uint64
	User  int64
}
type undoKey struct {
	Group int64
	Match uint64
	User  int64
}

// New returns an empty memstore.
func New() *Store {
	return &Store{
		participants:  map[participantKey]models.Participant{},
		admins:        map[int64]models.Admin{},
		s21Accounts:   map[int64]s21account.S21Account{},
		groups:        map[int64]models.Group{},
		matches:       map[matchKey]models.Match{},
		confirmations: map[confirmKey]models.MatchConfirmation{},
		undos:         map[undoKey]models.UndoCommand{},
		settings:      map[string]models.BotSetting{},
		matchCounters: map[int64]uint64{},
	}
}

// Close is a no-op.
func (s *Store) Close() error { return nil }

func (s *Store) Participants() store.ParticipantRepo              { return participantRepo{s} }
func (s *Store) Admins() store.AdminRepo                          { return adminRepo{s} }
func (s *Store) S21Accounts() store.S21AccountRepo                { return s21AccountRepo{s} }
func (s *Store) Groups() store.GroupRepo                          { return groupRepo{s} }
func (s *Store) Matches() store.MatchRepo                         { return matchRepo{s} }
func (s *Store) MatchConfirmations() store.MatchConfirmationRepo  { return confirmRepo{s} }
func (s *Store) UndoCommands() store.UndoRepo                     { return undoRepo{s} }
func (s *Store) Settings() store.SettingsRepo                     { return settingsRepo{s} }

// AllocateAndInsertMatch allocates the next match_id for the group, asks
// `build` to populate the row, and inserts atomically.
func (s *Store) AllocateAndInsertMatch(ctx context.Context, groupID int64, build func(matchID uint64) models.Match) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.matchCounters[groupID] + 1
	row := build(next)
	row.MatchID = next
	s.matches[matchKey{row.GroupID, row.MatchID}] = row
	s.matchCounters[groupID] = next
	return next, nil
}

// ---------- Participants --------------------------------------------------

type participantRepo struct{ s *Store }

func (r participantRepo) Get(_ context.Context, gid, uid int64) (models.Participant, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	p, ok := r.s.participants[participantKey{gid, uid}]
	if !ok {
		return models.Participant{}, store.ErrNotFound
	}
	return p, nil
}

func (r participantRepo) GetByUsername(_ context.Context, gid int64, username string) (models.Participant, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for k, p := range r.s.participants {
		if k.Group != gid {
			continue
		}
		if strings.EqualFold(p.TelegramUsername, username) {
			return p, nil
		}
	}
	return models.Participant{}, store.ErrNotFound
}

func (r participantRepo) ListByGroup(_ context.Context, gid int64) ([]models.Participant, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []models.Participant
	for k, p := range r.s.participants {
		if k.Group == gid {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r participantRepo) Upsert(_ context.Context, p models.Participant) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.participants[participantKey{p.GroupID, p.TelegramID}] = p
	return nil
}

// ---------- Admins --------------------------------------------------------

type adminRepo struct{ s *Store }

func (r adminRepo) Get(ctx context.Context, id int64) (models.Admin, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	a, ok := r.s.admins[id]
	if !ok {
		return models.Admin{}, store.ErrNotFound
	}
	return a, nil
}

func (r adminRepo) GetByCampus(ctx context.Context, cid string) (models.Admin, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, a := range r.s.admins {
		if a.CampusID == cid {
			return a, nil
		}
	}
	return models.Admin{}, store.ErrNotFound
}

func (r adminRepo) Upsert(ctx context.Context, a models.Admin) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.admins[a.TelegramID] = a
	return nil
}

func (r adminRepo) List(ctx context.Context) ([]models.Admin, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := make([]models.Admin, 0, len(r.s.admins))
	for _, a := range r.s.admins {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TelegramID < out[j].TelegramID })
	return out, nil
}

// ---------- S21Accounts ---------------------------------------------------

type s21AccountRepo struct{ s *Store }

func (r s21AccountRepo) Get(_ context.Context, tid int64) (s21account.S21Account, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	a, ok := r.s.s21Accounts[tid]
	if !ok {
		return s21account.S21Account{}, s21account.ErrNotFound
	}
	return a, nil
}

// List returns rows ordered by CreatedAt ASC — PickHealthy contract.
func (r s21AccountRepo) List(_ context.Context) ([]s21account.S21Account, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := make([]s21account.S21Account, 0, len(r.s.s21Accounts))
	for _, a := range r.s.s21Accounts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].TelegramID < out[j].TelegramID
	})
	return out, nil
}

func (r s21AccountRepo) Upsert(_ context.Context, a s21account.S21Account) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.s21Accounts[a.TelegramID] = a
	return nil
}

func (r s21AccountRepo) Delete(_ context.Context, tid int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	delete(r.s.s21Accounts, tid)
	return nil
}

// ---------- Groups --------------------------------------------------------

type groupRepo struct{ s *Store }

func (r groupRepo) Get(ctx context.Context, id int64) (models.Group, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	g, ok := r.s.groups[id]
	if !ok {
		return models.Group{}, store.ErrNotFound
	}
	return g, nil
}

func (r groupRepo) GetByCampus(ctx context.Context, cid string) (models.Group, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, g := range r.s.groups {
		if g.CampusID == cid {
			return g, nil
		}
	}
	return models.Group{}, store.ErrNotFound
}

func (r groupRepo) Upsert(ctx context.Context, g models.Group) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.groups[g.GroupID] = g
	return nil
}

func (r groupRepo) List(ctx context.Context) ([]models.Group, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := make([]models.Group, 0, len(r.s.groups))
	for _, g := range r.s.groups {
		out = append(out, g)
	}
	return out, nil
}

// ---------- Matches -------------------------------------------------------

type matchRepo struct{ s *Store }

func (r matchRepo) Get(ctx context.Context, gid int64, mid uint64) (models.Match, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	m, ok := r.s.matches[matchKey{gid, mid}]
	if !ok {
		return models.Match{}, store.ErrNotFound
	}
	return m, nil
}

func (r matchRepo) UpdateStatus(ctx context.Context, gid int64, mid uint64, status models.MatchStatus) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k := matchKey{gid, mid}
	m, ok := r.s.matches[k]
	if !ok {
		return store.ErrNotFound
	}
	m.Status = status
	r.s.matches[k] = m
	return nil
}

// Insert is unexported here; it's only invoked via AllocateAndInsertMatch.
func (s *Store) insertMatch(m models.Match) {
	s.matches[matchKey{m.GroupID, m.MatchID}] = m
}

// PutMatch is exposed for tests and the AllocateAndInsertMatch closure.
// It assumes the caller holds the mutex (via AllocateAndInsertMatch).
func (s *Store) PutMatch(m models.Match) {
	s.insertMatch(m)
}

// PutMatchExt is the externally callable version that takes the lock.
func (s *Store) PutMatchExt(m models.Match) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insertMatch(m)
}

func (r matchRepo) Delete(ctx context.Context, gid int64, mid uint64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k := matchKey{gid, mid}
	if _, ok := r.s.matches[k]; !ok {
		return store.ErrNotFound
	}
	delete(r.s.matches, k)
	// Also clear confirmations and undo commands for this match.
	for ck := range r.s.confirmations {
		if ck.Group == gid && ck.Match == mid {
			delete(r.s.confirmations, ck)
		}
	}
	for uk := range r.s.undos {
		if uk.Group == gid && uk.Match == mid {
			delete(r.s.undos, uk)
		}
	}
	return nil
}

func (r matchRepo) ListByGroup(ctx context.Context, gid int64) ([]models.Match, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []models.Match
	for k, m := range r.s.matches {
		if k.Group == gid {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].PlayedAt.Equal(out[j].PlayedAt) {
			return out[i].PlayedAt.Before(out[j].PlayedAt)
		}
		return out[i].MatchID < out[j].MatchID
	})
	return out, nil
}

func (r matchRepo) ListPendingExpired(ctx context.Context, before func(g models.Group) bool) ([]models.Match, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []models.Match
	for _, m := range r.s.matches {
		if m.Status != models.MatchStatusPending {
			continue
		}
		g, ok := r.s.groups[m.GroupID]
		if !ok {
			continue
		}
		if before(g) && time.Since(m.CreatedAt) > g.ConfirmationTimeout() {
			out = append(out, m)
		}
	}
	return out, nil
}

// ---------- MatchConfirmations -------------------------------------------

type confirmRepo struct{ s *Store }

func (r confirmRepo) Insert(ctx context.Context, c models.MatchConfirmation) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.confirmations[confirmKey{c.GroupID, c.MatchID, c.TelegramID}] = c
	return nil
}

func (r confirmRepo) ListForMatch(ctx context.Context, gid int64, mid uint64) ([]models.MatchConfirmation, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []models.MatchConfirmation
	for k, c := range r.s.confirmations {
		if k.Group == gid && k.Match == mid {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r confirmRepo) DeleteForMatch(ctx context.Context, gid int64, mid uint64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for k := range r.s.confirmations {
		if k.Group == gid && k.Match == mid {
			delete(r.s.confirmations, k)
		}
	}
	return nil
}

// ---------- Undo ----------------------------------------------------------

type undoRepo struct{ s *Store }

func (r undoRepo) Insert(ctx context.Context, u models.UndoCommand) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.undos[undoKey{u.GroupID, u.MatchID, u.TelegramID}] = u
	return nil
}

func (r undoRepo) Delete(ctx context.Context, gid int64, mid uint64, uid int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	k := undoKey{gid, mid, uid}
	if _, ok := r.s.undos[k]; !ok {
		return store.ErrNotFound
	}
	delete(r.s.undos, k)
	return nil
}

func (r undoRepo) DeleteForMatch(ctx context.Context, gid int64, mid uint64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for k := range r.s.undos {
		if k.Group == gid && k.Match == mid {
			delete(r.s.undos, k)
		}
	}
	return nil
}

func (r undoRepo) ListForMatch(ctx context.Context, gid int64, mid uint64) ([]models.UndoCommand, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []models.UndoCommand
	for k, u := range r.s.undos {
		if k.Group == gid && k.Match == mid {
			out = append(out, u)
		}
	}
	return out, nil
}

func (r undoRepo) ListExpired(ctx context.Context, cutoffNanos int64) ([]models.UndoCommand, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []models.UndoCommand
	for _, u := range r.s.undos {
		if u.RequestedAt.UnixNano() < cutoffNanos {
			out = append(out, u)
		}
	}
	return out, nil
}

// ---------- Settings ------------------------------------------------------

type settingsRepo struct{ s *Store }

func (r settingsRepo) Get(ctx context.Context, key string) (models.BotSetting, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	v, ok := r.s.settings[key]
	if !ok {
		return models.BotSetting{}, store.ErrNotFound
	}
	return v, nil
}

func (r settingsRepo) Set(ctx context.Context, key, value string, by int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.settings[key] = models.BotSetting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now(),
		UpdatedBy: by,
	}
	return nil
}
