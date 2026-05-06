// Package testkit is a high-level test framework for ttbot scenarios.
//
// Each scenario wires up a fresh memstore, a mock messenger, a mock S21, and
// the real Handlers. Tests then drive user interactions through the World
// helpers and assert on the recorded mock calls.
//
// This is a CUSTOM testing framework purpose-built for this bot. It is
// supposed to be the only thing tests need to import (besides stdlib).
package testkit

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/crypto"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/handlers"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store/memstore"
)

// World is the test fixture. Use New to construct.
type World struct {
	T        *testing.T
	Ctx      context.Context
	Store    *memstore.Store
	Messen   *messenger.Mock
	S21      *s21.Mock
	Cipher   *crypto.Cipher
	Handlers *handlers.Handlers

	clock time.Time

	// Sequence of message IDs allocated to inbound messages, so tests can
	// reference them without manual bookkeeping.
	nextMessageID int64
}

// New builds a fresh World.
func New(t *testing.T) *World {
	t.Helper()
	st := memstore.New()
	mm := messenger.NewMock()
	sm := s21.NewMock()
	c, err := crypto.NewFromKey(make32ByteKey())
	if err != nil {
		t.Fatal(err)
	}
	w := &World{
		T:      t,
		Ctx:    context.Background(),
		Store:  st,
		Messen: mm,
		S21:    sm,
		Cipher: c,
		clock:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	w.Handlers = handlers.New(st, mm, sm, c, handlers.Config{
		RatingEngineDefault:     "elo",
		RatingPeriodDaysDefault: 1,
		Now:                     w.now,
	})
	return w
}

func (w *World) now() time.Time { return w.clock }

// Advance moves the test clock forward.
func (w *World) Advance(d time.Duration) { w.clock = w.clock.Add(d) }

// SetClock sets the test clock to an absolute time.
func (w *World) SetClock(t time.Time) { w.clock = t }

// allocMessageID picks a fresh message ID for the next inbound message.
func (w *World) allocMessageID() int64 {
	w.nextMessageID++
	return w.nextMessageID
}

// ---------- Setup helpers --------------------------------------------------

// User adds a basic user row (no nickname). Returns it for further fluent calls.
type User struct {
	W            *World
	TelegramID   int64
	Username     string
	IsBot        bool
}

// AddUser registers a user (memstore upsert).
func (w *World) AddUser(telegramID int64, username string) User {
	u := User{W: w, TelegramID: telegramID, Username: username}
	if err := w.Store.Users().Upsert(w.Ctx, models.User{
		TelegramID:       telegramID,
		TelegramUsername: username,
		DMChatID:         telegramID,
		NicknameStatus:   models.NicknameStatusNone,
	}); err != nil {
		w.T.Fatal(err)
	}
	return u
}

// SetNickname marks the user as nicknamed (provided + optionally verified).
func (u User) SetNickname(s21Nickname, campusID, campusName string, verified bool) User {
	row, _ := u.W.Store.Users().Get(u.W.Ctx, u.TelegramID)
	row.S21Nickname = s21Nickname
	row.CampusID = campusID
	row.CampusName = campusName
	row.NicknameStatus = models.NicknameStatusProvided
	row.ProvidedBy = models.ProvidedBySelf
	row.ProvidedAt = u.W.now()
	if verified {
		row.VerifiedBy = models.VerifiedByAdmin
		row.VerifiedAt = u.W.now()
	} else {
		row.VerifiedBy = models.VerifiedByNone
	}
	if err := u.W.Store.Users().Upsert(u.W.Ctx, row); err != nil {
		u.W.T.Fatal(err)
	}
	return u
}

// MakeAdmin promotes the user to a campus admin (the S21 mock is configured
// with the corresponding credentials). Also marks the underlying users row as
// nicknamed-and-verified, since an authenticated S21 admin is by definition a
// verified S21 user.
func (u User) MakeAdmin(login, password, campusID, campusName string) User {
	u.W.S21.SetUser(login, password, s21.Profile{Login: login, CampusID: campusID, CampusName: campusName})
	u.W.S21.SetAdminPassword(login, password)
	enc, _ := u.W.Cipher.Encrypt(password)
	if err := u.W.Store.Admins().Upsert(u.W.Ctx, models.Admin{
		TelegramID:              u.TelegramID,
		CampusID:                campusID,
		CampusName:              campusName,
		S21Login:                login,
		S21CredentialsEncrypted: enc,
		CreatedAt:               u.W.now(),
	}); err != nil {
		u.W.T.Fatal(err)
	}
	u.SetNickname(login, campusID, campusName, true)
	return u
}

// Group is a convenience for creating a fully configured supergroup.
type Group struct {
	W              *World
	GroupID        int64
	CampusID       string
	MatchesTopicID int64
	StatsTopicID   int64
}

// AddConfiguredGroup builds a registered supergroup with both topics set.
func (w *World) AddConfiguredGroup(groupID int64, campusID, campusName string, adminTGID, matchesTopicID, statsTopicID int64) Group {
	g := models.Group{
		GroupID:                  groupID,
		CampusID:                 campusID,
		CampusName:               campusName,
		AdminTelegramID:          adminTGID,
		MatchesTopicID:           matchesTopicID,
		StatsTopicID:             statsTopicID,
		ConfirmationTimeoutHours: 24,
		CreatedAt:                w.now(),
	}
	if err := w.Store.Groups().Upsert(w.Ctx, g); err != nil {
		w.T.Fatal(err)
	}
	return Group{W: w, GroupID: groupID, CampusID: campusID, MatchesTopicID: matchesTopicID, StatsTopicID: statsTopicID}
}

// AddPlayer activates a user in a group.
func (g Group) AddPlayer(telegramID int64) Group {
	if err := g.W.Store.Players().Upsert(g.W.Ctx, models.Player{
		GroupID:     g.GroupID,
		TelegramID:  telegramID,
		ActivatedAt: g.W.now(),
	}); err != nil {
		g.W.T.Fatal(err)
	}
	return g
}

// ---------- Inbound updates ------------------------------------------------

// SendDM dispatches a private-chat text message from the given user.
func (w *World) SendDM(from User, text string) {
	w.W().Messen.Reset()
	w.dispatchMessage(messenger.Chat{ID: from.TelegramID, Type: "private"}, from, 0, text, nil)
}

// W returns the world (helper for chaining).
func (w *World) W() *World { return w }

// SendInGroup dispatches a group text message in the given group's topic.
func (w *World) SendInGroup(g Group, from User, topicID int64, text string) {
	w.dispatchMessage(messenger.Chat{ID: g.GroupID, Type: "supergroup", IsForum: true}, from, topicID, text, nil)
}

// SendReply dispatches a reply-to-bot message in the given group's topic.
func (w *World) SendReply(g Group, from User, topicID int64, text string, replyToText string) {
	reply := &messenger.Message{Text: replyToText, MessageID: 999}
	w.dispatchMessage(messenger.Chat{ID: g.GroupID, Type: "supergroup", IsForum: true}, from, topicID, text, reply)
}

// TapButton synthesizes a callback-query for the given match payload.
func (w *World) TapButton(g Group, from User, callbackData string, messageID int64) {
	upd := &messenger.Update{
		CallbackQuery: &messenger.CallbackQuery{
			ID:   fmt.Sprintf("cb-%d", time.Now().UnixNano()),
			From: &messenger.User{ID: from.TelegramID, Username: from.Username},
			Message: &messenger.Message{
				MessageID: messageID,
				Chat:      messenger.Chat{ID: g.GroupID, Type: "supergroup", IsForum: true},
			},
			Data: callbackData,
		},
	}
	if err := w.Handlers.Dispatch(w.Ctx, upd); err != nil {
		w.T.Fatalf("Dispatch: %v", err)
	}
}

// JoinGroup synthesizes a ChatMember update where someone joins the group.
func (w *World) JoinGroup(g Group, joiner User) {
	upd := &messenger.Update{
		ChatMember: &messenger.ChatMemberUpdate{
			Chat:          messenger.Chat{ID: g.GroupID, Type: "supergroup", IsForum: true},
			From:          &messenger.User{ID: joiner.TelegramID, Username: joiner.Username},
			NewChatMember: &messenger.ChatMember{User: &messenger.User{ID: joiner.TelegramID, Username: joiner.Username}, Status: "member"},
		},
	}
	if err := w.Handlers.Dispatch(w.Ctx, upd); err != nil {
		w.T.Fatalf("Dispatch: %v", err)
	}
}

func (w *World) dispatchMessage(chat messenger.Chat, from User, topicID int64, text string, reply *messenger.Message) {
	msg := &messenger.Message{
		MessageID:       w.allocMessageID(),
		Chat:            chat,
		From:            &messenger.User{ID: from.TelegramID, Username: from.Username},
		Text:            text,
		MessageThreadID: topicID,
		ReplyTo:         reply,
	}
	if err := w.Handlers.Dispatch(w.Ctx, &messenger.Update{Message: msg}); err != nil {
		w.T.Fatalf("Dispatch: %v", err)
	}
}

// RunPeriodic invokes the cron job.
func (w *World) RunPeriodic() {
	if err := w.Handlers.PeriodicJob(w.Ctx); err != nil {
		w.T.Fatalf("PeriodicJob: %v", err)
	}
}

// ---------- Assertions -----------------------------------------------------

// LastReply returns the last SendMessage call. Fails if there is none.
func (w *World) LastReply() messenger.Call {
	calls := w.Messen.CallsByMethod("SendMessage")
	if len(calls) == 0 {
		w.T.Fatalf("no SendMessage calls; transcript:\n%s", w.Messen.Pretty())
	}
	return calls[len(calls)-1]
}

// AssertReplyContains fails unless at least one SendMessage call's text
// contains the substring.
func (w *World) AssertReplyContains(substr string) {
	w.T.Helper()
	for _, c := range w.Messen.CallsByMethod("SendMessage") {
		if strings.Contains(c.Text, substr) {
			return
		}
	}
	w.T.Fatalf("no reply contains %q\ntranscript:\n%s", substr, w.Messen.Pretty())
}

// AssertLastReplyContains is the strict version: only checks the most recent
// SendMessage call.
func (w *World) AssertLastReplyContains(substr string) {
	w.T.Helper()
	got := w.LastReply()
	if !strings.Contains(got.Text, substr) {
		w.T.Fatalf("last reply does not contain %q\ngot %q\ntranscript:\n%s", substr, got.Text, w.Messen.Pretty())
	}
}

// AssertReplyEquals asserts exact text match.
func (w *World) AssertReplyEquals(text string) {
	w.T.Helper()
	got := w.LastReply()
	if got.Text != text {
		w.T.Fatalf("last reply mismatch:\nwant: %q\ngot:  %q", text, got.Text)
	}
}

// AssertNoReplies fails if any SendMessage call was made.
func (w *World) AssertNoReplies() {
	w.T.Helper()
	if calls := w.Messen.CallsByMethod("SendMessage"); len(calls) != 0 {
		w.T.Fatalf("expected no replies, got %d:\n%s", len(calls), w.Messen.Pretty())
	}
}

// ResetMessenger clears recorded calls (handy between scenario steps).
func (w *World) ResetMessenger() { w.Messen.Reset() }

// ---------- Internal -------------------------------------------------------

func make32ByteKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}
