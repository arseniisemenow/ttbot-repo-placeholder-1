// Package models holds the domain types for ttbot.
package models

import "time"

// NicknameStatus is the lifecycle state of a user's identity.
type NicknameStatus string

const (
	NicknameStatusNone     NicknameStatus = "none"
	NicknameStatusProvided NicknameStatus = "nickname_provided"
	NicknameStatusGuest    NicknameStatus = "guest"
)

// ProvidedBy is who supplied the nickname.
type ProvidedBy string

const (
	ProvidedBySelf  ProvidedBy = "self"
	ProvidedByAdmin ProvidedBy = "admin"
)

// VerifiedBy is who verified the S21 nickname.
type VerifiedBy string

const (
	VerifiedByNone  VerifiedBy = "none"
	VerifiedByAdmin VerifiedBy = "admin"
	VerifiedByAuth  VerifiedBy = "auth"
)

// MatchStatus is a match's lifecycle state.
type MatchStatus string

const (
	MatchStatusPending  MatchStatus = "PENDING"
	MatchStatusApproved MatchStatus = "APPROVED"
	MatchStatusUndone   MatchStatus = "UNDONE"
)

// User is the global users-table row.
type User struct {
	TelegramID       int64
	TelegramUsername string
	DMChatID         int64
	S21Nickname      string
	CampusID         string
	CampusName       string
	CoalitionName    string // reserved for future use
	NicknameStatus   NicknameStatus
	ProvidedBy       ProvidedBy
	ProvidedAt       time.Time
	VerifiedBy       VerifiedBy
	VerifiedAt       time.Time
	AdminTelegramID  int64
}

// HasNickname reports whether the user is allowed to play (provided or guest).
func (u User) HasNickname() bool {
	return u.NicknameStatus == NicknameStatusProvided || u.NicknameStatus == NicknameStatusGuest
}

// IsVerified reports whether the user appears in rankings/stats.
func (u User) IsVerified() bool {
	return u.VerifiedBy != "" && u.VerifiedBy != VerifiedByNone
}

// DisplayName returns the rendering for any UI: S21 nickname for nicknamed users,
// @telegram_username for guests. Falls back to the telegram username if neither
// is set, then to a numeric ID.
func (u User) DisplayName() string {
	if u.NicknameStatus == NicknameStatusProvided && u.S21Nickname != "" {
		return u.S21Nickname
	}
	if u.TelegramUsername != "" {
		return "@" + u.TelegramUsername
	}
	return ""
}

// Player is the players-table row (per-group activation only).
type Player struct {
	GroupID     int64
	TelegramID  int64
	ActivatedAt time.Time
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
	RankingsMessageID        int64 // 0 = not posted yet
	StatsMessageID           int64 // 0 = not posted yet
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
