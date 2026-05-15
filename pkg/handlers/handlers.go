// Package handlers wires a parsed Telegram update to the bot's business
// logic. Two entrypoints exist: Dispatch (for the webhook function) and
// PeriodicJob (for the cron function).
package handlers

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	s21account "github.com/arseniisemenow/s21-account-go"
	identityclient "github.com/arseniisemenow/s21-identity-client-go"

	"github.com/arseniisemenow/ttbot-core/pkg/crypto"
	"github.com/arseniisemenow/ttbot-core/pkg/identity"
	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/notify"
	"github.com/arseniisemenow/ttbot-core/pkg/s21"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// Config carries configuration values read from environment.
type Config struct {
	RatingEngineDefault     string // "elo" or "glicko2" — seed for first read
	RatingPeriodDaysDefault int
	Now                     func() time.Time // injectable for tests; defaults to time.Now
	// IdentityBaseURL is the identity-service base URL. Per-call identity
	// clients are constructed from the oldest healthy s21_accounts row's
	// credentials (see withIdentity).
	IdentityBaseURL string
	// IdentityAPIKey is ttbot's read-scope X-Api-Key for the identity service.
	// Empty during the bootstrap window before the operator mints + populates
	// the env (identity-service runs in dry-run mode then).
	IdentityAPIKey string
}

// s21NickCacheTTL is how long a cached S21 nickname (or cached "no
// nickname") record is considered fresh before the next access refetches
// from the identity service. Long because S21 nicknames change extremely
// rarely (only when a user runs /provide_nickname in the identity bot)
// and the displayed string is purely cosmetic.
const s21NickCacheTTL = 7 * 24 * time.Hour

// Handlers holds all dependencies. Construct with New, then call Dispatch
// or PeriodicJob.
type Handlers struct {
	Store    store.Store
	M        messenger.Messenger
	Notifier *notify.Notifier
	S21      s21.Client
	Cipher   *crypto.Cipher
	Config   Config

	// S21Nicks is the process-wide S21-nickname cache. One instance shared
	// across all handlers (playerLabel, displayFor, hasNickname,
	// match_interactive, …). Lazy refresh on read.
	S21Nicks *identity.S21NickCache

	// matchDrafts holds in-flight /match interactive flows keyed by
	// "<chat_id>:<message_id>". Authoritative state for fast-click safety;
	// the message-text header is a mirror that survives cold starts.
	matchDraftsMu sync.RWMutex
	matchDrafts   map[string]*matchDraft
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
		Store:       s,
		M:           m,
		Notifier:    notify.New(m),
		S21:         s21c,
		Cipher:      cipher,
		Config:      cfg,
		S21Nicks:    identity.NewS21NickCache(s21NickCacheTTL, cfg.Now),
		matchDrafts: map[string]*matchDraft{},
	}
}

// withIdentity runs `fn` against an identity.Service built from a healthy
// s21_accounts row's stored credentials. On identityclient.ErrInvalidS21Token
// from `fn` (or from any nested call), the shared package's PickHealthy
// marks that row bad and retries with the next healthy row.
//
// Callers should treat the returned s21account.ErrNoHealthy as "the bot has
// no working S21 logins right now" and surface a user-friendly message.
//
// Each invocation builds a fresh identity.Service: the in-memory cache lives
// for the duration of one operation. That's fine — typical operations make
// at most O(N players) lookups, and the per-row creds-failure cron keeps the
// healthy set small. No cross-request global cache is intentional: a stale
// nickname change in the identity bot is reflected on the next operation.
func (h *Handlers) withIdentity(ctx context.Context, fn func(svc *identity.Service) error) error {
	if h.Config.IdentityBaseURL == "" {
		return errors.New("identity base URL not configured")
	}
	return s21account.PickHealthy(ctx, h.Store.S21Accounts(), h.Cipher, h.Config.Now(),
		func(login, password string) error {
			svc := identity.New(h.Config.IdentityBaseURL, login, password, h.Config.IdentityAPIKey)
			err := fn(svc)
			if errors.Is(err, identityclient.ErrInvalidS21Token) {
				return s21account.ErrInvalidCredentials
			}
			return err
		})
}

// tryIdentity is the fire-and-forget variant of withIdentity used by
// display-only helpers (playerLabel, displayFor, hasNickname). Any error
// from fn — including "no healthy login" — is silently swallowed, since
// the caller has a non-identity fallback (Telegram @username or numeric id).
// fn should still return errors verbatim so withIdentity can mark a bad
// row and retry with the next.
func (h *Handlers) tryIdentity(ctx context.Context, fn func(svc *identity.Service) error) {
	_ = h.withIdentity(ctx, fn)
}

// Dispatch routes a single Telegram update through the command tree.
// Errors are logged and swallowed at the top level — the webhook always
// returns 200 to Telegram.
func (h *Handlers) Dispatch(ctx context.Context, u *messenger.Update) error {
	// /match-flow perf instrumentation: gated on TTBOT_MATCH_PERF_LOG=1.
	// We log on /match command messages and on the m:i:* callback path
	// (interactive picker taps) — covers the first reply and every tap
	// inside the same flow.
	var tDispatch time.Time
	logMatch := false
	if matchPerfLogEnabled && u.Message != nil && strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/match") {
		logMatch = true
		tDispatch = time.Now()
		from := int64(0)
		if u.Message.From != nil {
			from = u.Message.From.ID
		}
		perfLog("dispatch.enter kind=message chat=%d user=%d thread=%d",
			u.Message.Chat.ID, from, u.Message.MessageThreadID)
	}
	if logMatch {
		defer func() { perfLog("dispatch.exit total=%v", time.Since(tDispatch)) }()
	}
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
	tLitter := time.Now()
	litterHit := h.deleteStatsTopicLitter(ctx, m)
	litterDur := time.Since(tLitter)
	if litterHit {
		return nil
	}
	// DM-only force-reply detectors run BEFORE the command switch — reply
	// text doesn't start with `/`.
	if m.Chat.Type == "private" {
		if isLoginReply(m) {
			return h.handleLoginReply(ctx, m)
		}
		if isLogoutReply(m) {
			return h.handleLogoutReply(ctx, m)
		}
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
	tUpsert := time.Now()
	h.upsertSenderParticipant(ctx, m)
	upsertDur := time.Since(tUpsert)
	if cmd == "/match" {
		perfLog("dispatch.pre dur=%v (litter=%v upsertSender=%v)",
			litterDur+upsertDur, litterDur, upsertDur)
	}
	isPrivate := m.Chat.Type == "private"
	switch cmd {
	// --- DM-only ---
	case "/start", "/help":
		if isPrivate {
			return h.handleStart(ctx, m)
		}
	case "/login":
		if isPrivate {
			return h.handleLogin(ctx, m, args)
		}
	case "/logout":
		if isPrivate {
			return h.handleLogout(ctx, m)
		}
	case "/whoami":
		if isPrivate {
			return h.handleWhoami(ctx, m)
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
