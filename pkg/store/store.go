// Package store defines the storage interfaces. Two implementations live in
// sub-packages: memstore (in-memory, used by tests) and ydbstore (real YDB,
// used in production).
package store

import (
	"context"
	"errors"

	"github.com/arseniisemenow/ttbot-core/pkg/models"
)

// ErrNotFound is the sentinel returned when a single-row read finds nothing.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert/update collides with an existing row
// in a way the caller should handle (e.g. duplicate match counter).
var ErrConflict = errors.New("store: conflict")

// Store is the union of every repository the bot uses. Production code wires
// up one implementation at startup.
type Store interface {
	Participants() ParticipantRepo
	Admins() AdminRepo
	Groups() GroupRepo
	Matches() MatchRepo
	MatchConfirmations() MatchConfirmationRepo
	UndoCommands() UndoRepo
	Settings() SettingsRepo

	// AllocateAndInsertMatch allocates the next match_id for the group, asks
	// `build` to populate the match row with that ID baked in, and inserts
	// the row + bumps the counter inside one SerializableReadWrite tx.
	AllocateAndInsertMatch(ctx context.Context, groupID int64, build func(matchID uint64) models.Match) (uint64, error)

	Close() error
}

// ParticipantRepo persists the per-group username cache used for /match
// @username resolution. One row per (group_id, telegram_id) — same telegram
// user in two groups produces two rows. Populated by Telegram chat_member
// events and read by /match (and any other handler that needs a display
// name when identity has no nickname).
type ParticipantRepo interface {
	Get(ctx context.Context, groupID, telegramID int64) (models.Participant, error)
	GetByUsername(ctx context.Context, groupID int64, telegramUsername string) (models.Participant, error)
	Upsert(ctx context.Context, p models.Participant) error
	// ListByGroup returns every participant cached for a single group. Used
	// by the username-refresh flow (cron + /refresh_usernames).
	ListByGroup(ctx context.Context, groupID int64) ([]models.Participant, error)
}

// AdminRepo persists campus admins.
type AdminRepo interface {
	Get(ctx context.Context, telegramID int64) (models.Admin, error)
	GetByCampus(ctx context.Context, campusID string) (models.Admin, error)
	Upsert(ctx context.Context, a models.Admin) error
	List(ctx context.Context) ([]models.Admin, error)
}

// GroupRepo persists registered supergroups.
type GroupRepo interface {
	Get(ctx context.Context, groupID int64) (models.Group, error)
	GetByCampus(ctx context.Context, campusID string) (models.Group, error)
	Upsert(ctx context.Context, g models.Group) error
	// List returns every registered group. Used by the DM-forward backfill
	// flow to enumerate which groups a DMing admin may target.
	List(ctx context.Context) ([]models.Group, error)
}

// MatchRepo persists match history.
type MatchRepo interface {
	Get(ctx context.Context, groupID int64, matchID uint64) (models.Match, error)
	UpdateStatus(ctx context.Context, groupID int64, matchID uint64, status models.MatchStatus) error
	Delete(ctx context.Context, groupID int64, matchID uint64) error
	ListByGroup(ctx context.Context, groupID int64) ([]models.Match, error)
	ListPendingExpired(ctx context.Context, before func(g models.Group) bool) ([]models.Match, error)
	// ListByGroupApprovedAndUndone is used by rating recompute paths to
	// distinguish APPROVED (counts) from UNDONE (excluded).
}

// MatchConfirmationRepo tracks inline-button confirmations.
type MatchConfirmationRepo interface {
	Insert(ctx context.Context, c models.MatchConfirmation) error
	ListForMatch(ctx context.Context, groupID int64, matchID uint64) ([]models.MatchConfirmation, error)
	DeleteForMatch(ctx context.Context, groupID int64, matchID uint64) error
}

// UndoRepo tracks pending undo requests.
type UndoRepo interface {
	Insert(ctx context.Context, u models.UndoCommand) error
	Delete(ctx context.Context, groupID int64, matchID uint64, telegramID int64) error
	DeleteForMatch(ctx context.Context, groupID int64, matchID uint64) error
	ListForMatch(ctx context.Context, groupID int64, matchID uint64) ([]models.UndoCommand, error)
	ListExpired(ctx context.Context, olderThan int64) ([]models.UndoCommand, error) // olderThan = unix nanos cutoff
}

// SettingsRepo holds the bot_settings KV.
type SettingsRepo interface {
	Get(ctx context.Context, key string) (models.BotSetting, error)
	Set(ctx context.Context, key, value string, updatedBy int64) error
}
