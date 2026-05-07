// Package handlers wires a parsed Telegram update to the bot's business
// logic. Two entrypoints exist: Dispatch (for the webhook function) and
// PeriodicJob (for the cron function).
package handlers

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/crypto"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/notify"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
)

// Config carries configuration values read from environment.
type Config struct {
	RatingEngineDefault     string // "elo" or "glicko2" — seed for first read
	RatingPeriodDaysDefault int
	Now                     func() time.Time // injectable for tests; defaults to time.Now
}

// Handlers holds all dependencies. Construct with New, then call Dispatch
// or PeriodicJob.
type Handlers struct {
	Store    store.Store
	M        messenger.Messenger
	Notifier *notify.Notifier
	S21      s21.Client
	Cipher   *crypto.Cipher
	Config   Config
}

// New constructs Handlers.
func New(s store.Store, m messenger.Messenger, s21c s21.Client, cipher *crypto.Cipher, cfg Config) *Handlers {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RatingEngineDefault == "" {
		cfg.RatingEngineDefault = "elo"
	}
	if cfg.RatingPeriodDaysDefault < 1 {
		cfg.RatingPeriodDaysDefault = 1
	}
	return &Handlers{
		Store:    s,
		M:        m,
		Notifier: notify.New(m),
		S21:      s21c,
		Cipher:   cipher,
		Config:   cfg,
	}
}

// Dispatch routes a single Telegram update through the command tree.
// Errors are logged and swallowed at the top level — the webhook always returns
// 200 to Telegram.
func (h *Handlers) Dispatch(ctx context.Context, u *messenger.Update) error {
	switch {
	case u.Message != nil:
		return h.dispatchMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		return h.dispatchCallback(ctx, u.CallbackQuery)
	case u.MyChatMember != nil:
		return h.dispatchMyChatMember(ctx, u.MyChatMember)
	case u.ChatMember != nil:
		return h.dispatchChatMember(ctx, u.ChatMember)
	}
	return nil
}

func (h *Handlers) dispatchMessage(ctx context.Context, m *messenger.Message) error {
	if m.From == nil || m.Text == "" {
		return nil
	}
	cmd, args := splitCommand(m.Text)
	if cmd == "" {
		return nil
	}
	isPrivate := m.Chat.Type == "private"
	switch cmd {
	// --- DM-only ---
	case "/start", "/help":
		if isPrivate {
			return h.handleStart(ctx, m)
		}
	case "/provide_nickname":
		if isPrivate {
			return h.handleProvideNickname(ctx, m, args)
		}
	case "/remove_nickname":
		if isPrivate {
			return h.handleRemoveNickname(ctx, m)
		}
	case "/admin":
		if isPrivate {
			return h.handleAdmin(ctx, m, args)
		}
	case "/provide_nickname_user":
		if isPrivate {
			return h.handleProvideNicknameUser(ctx, m, args)
		}
	case "/verify_nickname":
		if isPrivate {
			return h.handleVerifyNickname(ctx, m, args)
		}
	case "/guest":
		if isPrivate {
			return h.handleGuest(ctx, m, args)
		}
	case "/list_users":
		if isPrivate {
			return h.handleListUsers(ctx, m)
		}
	// --- Group config (any topic of registered supergroup) ---
	case "/bot_register_group":
		return h.handleBotRegisterGroup(ctx, m)
	case "/set_matches_topic":
		return h.handleSetMatchesTopic(ctx, m)
	case "/set_stats_topic":
		return h.handleSetStatsTopic(ctx, m)
	// --- Group, must be in matches topic ---
	case "/match":
		return h.handleMatch(ctx, m, args)
	case "/undo":
		return h.handleUndo(ctx, m, args)
	case "/rankings":
		return h.handleRankings(ctx, m)
	case "/stats":
		return h.handleStats(ctx, m, args)
	}
	return nil
}

// splitCommand returns ("/foo", "rest of args"). If the command has the @bot
// suffix (Telegram convention in groups), it's stripped.
func splitCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	cmd := parts[0]
	if at := strings.Index(cmd, "@"); at >= 0 {
		cmd = cmd[:at]
	}
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return cmd, args
}

// reply is a small helper that sends a plain text reply to the message's chat
// (and the same topic, when in a forum thread).
func (h *Handlers) reply(ctx context.Context, m *messenger.Message, text string) error {
	_, err := h.M.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, text)
	if err != nil {
		log.Printf("reply: %v", err)
	}
	return err
}

// (Per-engine rendering is now handled by handlers/rankings.go's buildEngines.
// The legacy single-engine selection via bot_settings.rating_engine is no
// longer used — both engines are always rendered side by side.)
