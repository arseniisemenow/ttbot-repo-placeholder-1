// Package store defines the storage interfaces. Two implementations live in
// sub-packages: memstore (in-memory, used by tests) and ydbstore (real YDB,
// used in production).
package store

import (
	"context"
	"errors"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
)

// ErrNotFound is the sentinel returned when a single-row read finds nothing.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert/update collides with an existing row
// in a way the caller should handle (e.g. duplicate match counter).
var ErrConflict = errors.New("store: conflict")

// Store is the union of every repository the bot uses. Production code wires
// up one implementation at startup.
type Store interface {
	Users() UserRepo
	Players() PlayerRepo
	Admins() AdminRepo
	Groups() GroupRepo
	Matches() MatchRepo
	MatchConfirmations() MatchConfirmationRepo
	UndoCommands() UndoRepo
	Settings() SettingsRepo

	// Atomically allocates and returns the next match_id for the group, then
	// invokes fn with that ID under a serializable transaction. Returning an
	// error from fn rolls back. The bot uses this for /match registration.
	AllocateAndInsertMatch(ctx context.Context, groupID int64, fn func(matchID uint64) error) (uint64, error)

	Close() error
}

// UserRepo persists global user identity.
type UserRepo interface {
	Get(ctx context.Context, telegramID int64) (models.User, error)
	GetByTelegramUsername(ctx context.Context, username string) (models.User, error)
	GetByS21Nickname(ctx context.Context, nickname string) (models.User, error)
	Upsert(ctx context.Context, u models.User) error
	// Reset clears nickname-related fields (used by /remove_nickname).
	Reset(ctx context.Context, telegramID int64) error
	List(ctx context.Context) ([]models.User, error)
}

// PlayerRepo persists per-group player activations.
type PlayerRepo interface {
	Get(ctx context.Context, groupID, telegramID int64) (models.Player, error)
	Upsert(ctx context.Context, p models.Player) error
	ListByGroup(ctx context.Context, groupID int64) ([]models.Player, error)
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
