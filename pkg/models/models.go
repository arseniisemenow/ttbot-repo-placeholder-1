// Package models holds the domain types for ttbot.
package models

import "time"

// MatchStatus is a match's lifecycle state.
type MatchStatus string

const (
	MatchStatusPending  MatchStatus = "PENDING"
	MatchStatusApproved MatchStatus = "APPROVED"
	MatchStatusUndone   MatchStatus = "UNDONE"
)

// Participant is one row of the per-group username-cache table. Populated
// from Telegram chat_member events when a user joins a registered group, and
// consulted when /match @username needs to resolve telegram_username →
// telegram_id within this group.
//
// Telegram has no public "get user by username" endpoint; this table is the
// bot's only mechanism for `@alice` lookups in /match. It is intentionally
// per-group scoped: the same @username may be claimed by different people in
// different chats, and a single chat is a stable disambiguation context.
type Participant struct {
	GroupID          int64
	TelegramID       int64
	TelegramUsername string
	ActivatedAt      time.Time
}

// Admin is a campus admin with encrypted S21 credentials.
type Admin struct {
	TelegramID              int64
	CampusID                string
	CampusName              string
	S21Login                string
	S21CredentialsEncrypted string
	CreatedAt               time.Time
}

// Group is a registered supergroup linked to a campus.
type Group struct {
	GroupID                  int64
	CampusID                 string
	CampusName               string
	AdminTelegramID          int64
	MatchesTopicID           int64 // 0 = unset
	StatsTopicID             int64 // 0 = unset
	RankingsMessageID        int64 // DEPRECATED orphan: legacy single-engine rankings message. Refresh deletes it and zeroes the field.
	StatsMessageID           int64 // Combined ELO + Glicko-2 stats message (the only pinned message in the stats topic). 0 = not posted yet.
	RankingsELOMessageID     int64 // ELO rankings message (not pinned). 0 = not posted yet.
	RankingsGlickoMessageID  int64 // Glicko-2 rankings message (not pinned). 0 = not posted yet.
	StatsELOMessageID        int64 // DEPRECATED orphan: per-engine ELO stats message. Refresh deletes it.
	StatsGlickoMessageID     int64 // DEPRECATED orphan: per-engine Glicko-2 stats message. Refresh deletes it.
	ConfirmationTimeoutHours uint32
	CreatedAt                time.Time
}

// FullyConfigured reports whether both required topics are set.
func (g Group) FullyConfigured() bool {
	return g.MatchesTopicID != 0 && g.StatsTopicID != 0
}

// ConfirmationTimeout returns the configured timeout, defaulting to 24h.
func (g Group) ConfirmationTimeout() time.Duration {
	if g.ConfirmationTimeoutHours == 0 {
		return 24 * time.Hour
	}
	return time.Duration(g.ConfirmationTimeoutHours) * time.Hour
}

// Match is one row in the matches table.
type Match struct {
	GroupID      int64
	MatchID      uint64
	Player1ID    int64
	Player2ID    int64
	Player1Score uint32
	Player2Score uint32
	RegisteredBy int64
	Status       MatchStatus
	PlayedAt     time.Time
	CreatedAt    time.Time
}

// Winner returns the telegram_id of the winner (0 for ties — not allowed but defensive).
func (m Match) Winner() int64 {
	switch {
	case m.Player1Score > m.Player2Score:
		return m.Player1ID
	case m.Player2Score > m.Player1Score:
		return m.Player2ID
	default:
		return 0
	}
}

// Loser returns the telegram_id of the loser (0 on tie).
func (m Match) Loser() int64 {
	switch {
	case m.Player1Score > m.Player2Score:
		return m.Player2ID
	case m.Player2Score > m.Player1Score:
		return m.Player1ID
	default:
		return 0
	}
}

// MatchConfirmation is one row in match_confirmations.
type MatchConfirmation struct {
	GroupID     int64
	MatchID     uint64
	TelegramID  int64
	ConfirmedAt time.Time
}

// UndoCommand is a pending /undo request.
type UndoCommand struct {
	GroupID     int64
	MatchID     uint64
	TelegramID  int64
	RequestedAt time.Time
}

// BotSetting is one row in bot_settings.
type BotSetting struct {
	Key       string
	Value     string
	UpdatedAt time.Time
	UpdatedBy int64
}

// Settings keys.
const (
	SettingRatingEngine     = "rating_engine"
	SettingRatingPeriodDays = "rating_period_days"
)

// Rating engine values.
const (
	EngineELO     = "elo"
	EngineGlicko2 = "glicko2"
)
