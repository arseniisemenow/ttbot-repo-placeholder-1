// Package testkit is a high-level test framework for ttbot scenarios.
//
// Each scenario wires up a fresh memstore, a mock messenger, a mock S21, a
// fake identity-service HTTP server, and the real Handlers. Tests then drive
// user interactions through the World helpers and assert on the recorded
// mock calls.
package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/crypto"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/handlers"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/identity"
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

	// IdentityFake is an httptest server that emulates the identity service
	// over the same HTTP wire protocol as the production SDK. Tests interact
	// with it through (User).SetNickname / (World).IdentityHits.
	IdentityFake *fakeIdentity

	clock time.Time

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
	fake := newFakeIdentity()
	t.Cleanup(fake.Close)

	w := &World{
		T:            t,
		Ctx:          context.Background(),
		Store:        st,
		Messen:       mm,
		S21:          sm,
		Cipher:       c,
		IdentityFake: fake,
		clock:        time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	w.Handlers = handlers.New(st, mm, sm, c, handlers.Config{
		RatingEngineDefault:     "elo",
		RatingPeriodDaysDefault: 1,
		Now:                     w.now,
		IdentityBaseURL:         fake.URL(),
	})
	// Preconfigure identity client so tests can SetNickname before any /admin runs.
	w.Handlers.SetIdentity(identity.New(fake.URL(), "tk-login", "tk-password"))
	return w
}

func (w *World) now() time.Time { return w.clock }

// Advance moves the test clock forward.
func (w *World) Advance(d time.Duration) { w.clock = w.clock.Add(d) }

// SetClock sets the test clock to an absolute time.
func (w *World) SetClock(t time.Time) { w.clock = t }

// IdentityFlushCache invalidates the identity-client cache so subsequent
// lookups go back through the fake HTTP server. Mirrors the production
// /refresh_identity behavior.
func (w *World) IdentityFlushCache() {
	if svc := w.Handlers.Identity(); svc != nil {
		svc.Flush()
	}
}

// allocMessageID picks a fresh message ID for the next inbound message.
func (w *World) allocMessageID() int64 {
	w.nextMessageID++
	return w.nextMessageID
}

// ---------- Setup helpers --------------------------------------------------

// User adds a basic user row (no nickname). Returns it for further fluent calls.
type User struct {
	W          *World
	TelegramID int64
	Username   string
	IsBot      bool
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

// SetNickname binds the user's telegram_id → s21Nickname in the fake identity
// service. This replaces the previous users-table side-effect approach.
func (u User) SetNickname(s21Nickname, campusID, campusName string, _ bool) User {
	u.W.IdentityFake.Put(identity.User{
		TelegramID: u.TelegramID,
		Nickname:   s21Nickname,
		CampusID:   campusID,
		CampusName: campusName,
		Found:      true,
	})
	u.W.IdentityFlushCache()
	return u
}

// MakeAdmin promotes the user to a campus admin and writes the encrypted-creds
// row to the admins table. Does NOT touch the identity service — admins'
// "registered S21 user" status is independent of their admin role under the
// new design.
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
	// Whoever the test names as "the admin" is also a Telegram-chat admin in
	// the mock messenger.
	w.Messen.SetChatAdmin(groupID, adminTGID, true)
	return Group{W: w, GroupID: groupID, CampusID: campusID, MatchesTopicID: matchesTopicID, StatsTopicID: statsTopicID}
}

// SetGroupAdmin marks a user as a Telegram-chat administrator of a group in
// the mock messenger.
func (g Group) SetGroupAdmin(u User, isAdmin bool) Group {
	g.W.Messen.SetChatAdmin(g.GroupID, u.TelegramID, isAdmin)
	return g
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

// ---------- Fake identity HTTP server --------------------------------------

// fakeIdentity is a minimal in-memory implementation of the identity service.
// It speaks the same wire format as the production service so tests exercise
// the real SDK (identityclient) over HTTP. Only the read endpoints used by
// ttbot are implemented; writes return 405.
type fakeIdentity struct {
	server *httptest.Server

	mu       sync.Mutex
	byTID    map[int64]identity.User
	hits     map[string]int // path → call count
}

// fakeUserDTO is the on-wire JSON shape expected by identityclient.
type fakeUserDTO struct {
	TelegramID    int64     `json:"telegram_id"`
	Nickname      string    `json:"nickname"`
	CampusID      string    `json:"campus_id"`
	CampusName    string    `json:"campus_name"`
	CoalitionName string    `json:"coalition_name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func newFakeIdentity() *fakeIdentity {
	f := &fakeIdentity{
		byTID: map[int64]identity.User{},
		hits:  map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/users/by_telegram/", f.handleByTelegram)
	mux.HandleFunc("/users/by_nickname/", f.handleByNickname)
	f.server = httptest.NewServer(mux)
	return f
}

// URL returns the server base URL (no trailing slash).
func (f *fakeIdentity) URL() string { return f.server.URL }

// Close shuts the server down.
func (f *fakeIdentity) Close() { f.server.Close() }

// Put inserts/updates a record.
func (f *fakeIdentity) Put(u identity.User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byTID[u.TelegramID] = u
}

// Hits returns a snapshot of per-endpoint hit counts.
func (f *fakeIdentity) Hits() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.hits))
	for k, v := range f.hits {
		out[k] = v
	}
	return out
}

func (f *fakeIdentity) bump(path string) {
	f.mu.Lock()
	f.hits[path]++
	f.mu.Unlock()
}

func (f *fakeIdentity) handleByTelegram(w http.ResponseWriter, r *http.Request) {
	f.bump("by_telegram")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/users/by_telegram/")
	tid, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	u, ok := f.byTID[tid]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}`))
		return
	}
	dto := fakeUserDTO{
		TelegramID: u.TelegramID, Nickname: u.Nickname,
		CampusID: u.CampusID, CampusName: u.CampusName, CoalitionName: u.CoalitionName,
	}
	writeJSON(w, http.StatusOK, dto)
}

func (f *fakeIdentity) handleByNickname(w http.ResponseWriter, r *http.Request) {
	f.bump("by_nickname")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/users/by_nickname/")
	nick, err := url.PathUnescape(raw)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	var dtos []fakeUserDTO
	for _, u := range f.byTID {
		if strings.EqualFold(u.Nickname, nick) {
			dtos = append(dtos, fakeUserDTO{
				TelegramID: u.TelegramID, Nickname: u.Nickname,
				CampusID: u.CampusID, CampusName: u.CampusName, CoalitionName: u.CoalitionName,
			})
		}
	}
	f.mu.Unlock()
	// Deterministic ordering by telegram_id (test contract: pick "earliest").
	sort.Slice(dtos, func(i, j int) bool { return dtos[i].TelegramID < dtos[j].TelegramID })
	writeJSON(w, http.StatusOK, struct {
		Users []fakeUserDTO `json:"users"`
	}{Users: dtos})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// ---------- Internal -------------------------------------------------------

func make32ByteKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}
