// Package handlers wires a parsed Telegram update to the bot's business
// logic. Two entrypoints exist: Dispatch (for the webhook function) and
// PeriodicJob (for the cron function).
package handlers

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/crypto"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/identity"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/notify"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
)

// Config carries configuration values read from environment.
type Config struct {
	RatingEngineDefault     string // "elo" or "glicko2" — seed for first read
	RatingPeriodDaysDefault int
	Now                     func() time.Time // injectable for tests; defaults to time.Now
	// IdentityBaseURL is the identity-service base URL. /admin uses it to
	// construct a fresh identity.Service after storing new admin creds.
	IdentityBaseURL string
	// IdentityAPIKey is ttbot's read-scope X-Api-Key for the identity service.
	// Passed through to identity.New on every Service construction. Empty
	// during the bootstrap window before the operator mints + populates the
	// env (identity-service runs in dry-run mode then).
	IdentityAPIKey string
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

	identityMu  sync.RWMutex
	identitySvc *identity.Service
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

// Identity returns the current identity-service client. Returns nil before
// any admin has run /admin (no credentials yet).
func (h *Handlers) Identity() *identity.Service {
	h.identityMu.RLock()
	defer h.identityMu.RUnlock()
	return h.identitySvc
}

// SetIdentity replaces the identity-service client. Used by /admin after new
// credentials are stored, and by the bootstrap path on the first cold start.
func (h *Handlers) SetIdentity(svc *identity.Service) {
	h.identityMu.Lock()
	defer h.identityMu.Unlock()
	h.identitySvc = svc
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
	if m == nil || m.From == nil {
		return nil
	}
	// DM-only: a forwarded message is the trigger for the participants-backfill
	// flow. Caught here before the empty-text guard below because forwarded
	// messages may carry no text (e.g. a forwarded photo) yet still tell us
	// who the original sender is via ForwardFrom.
	if m.Chat.Type == "private" {
		if m.ForwardFrom != nil {
			return h.handleForwardedAdd(ctx, m, m.ForwardFrom)
		}
		if m.ForwardSenderName != "" {
			return h.reply(ctx, m,
				"This user has hidden their account on forwards — can't read their telegram id this way.")
		}
	}
	// Stats topic is read-only by policy: the bot maintains exactly three
	// messages (ELO Rankings, Glicko-2 Rankings, combined Stats) and deletes
	// anything else that lands in there. The maintained messages are posted
	// by the bot itself and so don't arrive as updates; the check below is
	// defensive in case Telegram ever echoes them back.
	if h.deleteStatsTopicLitter(ctx, m) {
		return nil
	}
	if m.Text == "" {
		return nil
	}
	cmd, args := splitCommand(m.Text)
	if cmd == "" {
		return nil
	}
	// Any command typed in a registered group backfills the participants cache.
	// Captures members who pre-date the bot (no chat_member event was emitted
	// for them) — they get a row the first time they run any command.
	h.upsertSenderParticipant(ctx, m)
	isPrivate := m.Chat.Type == "private"
	switch cmd {
	// --- DM-only ---
	case "/start", "/help":
		if isPrivate {
			return h.handleStart(ctx, m)
		}
	case "/admin":
		if isPrivate {
			return h.handleAdmin(ctx, m, args)
		}
	case "/refresh_identity":
		if isPrivate {
			return h.handleRefreshIdentity(ctx, m)
		}
	// --- Group config (any topic of registered supergroup) ---
	case "/bot_register_group":
		return h.handleBotRegisterGroup(ctx, m)
	case "/set_matches_topic":
		return h.handleSetMatchesTopic(ctx, m)
	case "/set_stats_topic":
		return h.handleSetStatsTopic(ctx, m)
	case "/refresh_usernames":
		return h.handleRefreshUsernames(ctx, m)
	// --- Group, must be in matches topic ---
	case "/match":
		return h.handleMatch(ctx, m, args)
	case "/undo":
		return h.handleUndo(ctx, m, args)
	case "/rankings":
		return h.handleRankings(ctx, m)
	case "/stats":
		return h.handleStats(ctx, m, args)
	case "/ping":
		return h.handlePing(ctx, m)
	}
	return nil
}

// handlePing reacts to the caller's message with 👍. Its main purpose is to
// give group members a zero-context way to register themselves with the bot:
// upsertSenderParticipant has already run by the time we get here, so the row
// is in the table before this reaction is sent.
func (h *Handlers) handlePing(ctx context.Context, m *messenger.Message) error {
	return h.M.SendReaction(ctx, m.Chat.ID, m.MessageID, "👍")
}

// upsertSenderParticipant records the message sender in the per-group
// participants cache. No-op for DMs, unregistered groups, and bot accounts.
func (h *Handlers) upsertSenderParticipant(ctx context.Context, m *messenger.Message) {
	if m.Chat.Type == "private" || m.From == nil || m.From.IsBot {
		return
	}
	if _, err := h.Store.Groups().Get(ctx, m.Chat.ID); err != nil {
		return
	}
	_ = h.Store.Participants().Upsert(ctx, models.Participant{
		GroupID:          m.Chat.ID,
		TelegramID:       m.From.ID,
		TelegramUsername: m.From.Username,
		ActivatedAt:      h.Config.Now(),
	})
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

// deleteStatsTopicLitter inspects a freshly-received message and, if it
// landed in a registered group's stats topic and is not one of the three
// maintained messages, deletes it. Returns true when a deletion was attempted
// (the dispatcher then short-circuits further handling for that message).
func (h *Handlers) deleteStatsTopicLitter(ctx context.Context, m *messenger.Message) bool {
	if m == nil || m.MessageThreadID == 0 {
		return false
	}
	g, err := h.Store.Groups().Get(ctx, m.Chat.ID)
	if err != nil {
		return false
	}
	if g.StatsTopicID == 0 || m.MessageThreadID != g.StatsTopicID {
		return false
	}
	if m.MessageID == g.RankingsELOMessageID ||
		m.MessageID == g.RankingsGlickoMessageID ||
		m.MessageID == g.StatsMessageID {
		return false
	}
	_ = h.M.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
	return true
}
