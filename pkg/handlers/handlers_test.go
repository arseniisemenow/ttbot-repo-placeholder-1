package handlers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/testkit"
)

// ---------- /admin --------------------------------------------------------

func TestAdminBecomesNewAdmin(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.S21.SetUser("alice_login", "p", s21.Profile{Login: "alice_login", CampusID: "kazan", CampusName: "Kazan"})
	w.SendDM(alice, "/admin alice_login:p")
	w.AssertReplyContains("admin for Kazan")
	a, err := w.Store.Admins().Get(w.Ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if a.S21Login != "alice_login" || a.CampusID != "kazan" {
		t.Errorf("admin row: %+v", a)
	}
}

func TestAdminInvalidCredentials(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/admin not_real:nope")
	w.AssertReplyContains("Invalid credentials")
}

func TestAdminCampusAlreadyTaken(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").MakeAdmin("alice_login", "pw", "kazan", "Kazan")
	_ = alice
	bob := w.AddUser(200, "bobby")
	w.S21.SetUser("bob_login", "pw", s21.Profile{Login: "bob_login", CampusID: "kazan", CampusName: "Kazan"})
	w.SendDM(bob, "/admin bob_login:pw")
	w.AssertReplyContains("already has an admin")
}

// ---------- /provide_nickname --------------------------------------------

func TestProvideNicknameNoAdminFails(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/provide_nickname some_nick")
	w.AssertReplyContains("campus admin must register first")
}

func TestProvideNicknameSuccess(t *testing.T) {
	w := testkit.New(t)
	w.AddUser(50, "admin1").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	w.S21.SetUser("alice_s21", "ignored", s21.Profile{Login: "alice_s21", CampusID: "kazan", CampusName: "Kazan", CoalitionName: "Terra"})
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/provide_nickname alice_s21")
	w.AssertReplyContains("Nickname provided")
	u, err := w.Store.Users().Get(w.Ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if u.S21Nickname != "alice_s21" || u.NicknameStatus != models.NicknameStatusProvided || u.VerifiedBy != models.VerifiedByNone {
		t.Errorf("user: %+v", u)
	}
}

func TestProvideNicknameAdminTokenExpired(t *testing.T) {
	w := testkit.New(t)
	w.AddUser(50, "admin1").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	w.S21.FailNext("LookupByLogin", s21.ErrInvalidCredentials)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/provide_nickname alice_s21")
	w.AssertReplyContains("Operation aborted")
}

// ---------- /remove_nickname ---------------------------------------------

func TestRemoveNickname(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
	w.SendDM(alice, "/remove_nickname")
	w.AssertReplyContains("Nickname cleared")
	u, _ := w.Store.Users().Get(w.Ctx, 100)
	if u.NicknameStatus != models.NicknameStatusNone || u.S21Nickname != "" {
		t.Errorf("user not reset: %+v", u)
	}
	if u.DMChatID == 0 {
		t.Errorf("dm_chat_id should be preserved")
	}
}

func TestRemoveNicknameNoNickname(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/remove_nickname")
	w.AssertReplyContains("don't have a nickname")
}

// ---------- /bot_register_group + topics ---------------------------------

func TestRegisterGroupAndTopics(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	// Group join (private chat membership update -> bot welcomes admin).
	groupID := int64(-1001)
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID, MatchesTopicID: 0, StatsTopicID: 0}, admin, 0, "/bot_register_group")
	w.AssertReplyContains("Group linked to Kazan")

	// /set_matches_topic inside topic 5.
	w.ResetMessenger()
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 5, "/set_matches_topic")
	w.AssertReplyContains("Matches topic set")

	w.ResetMessenger()
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 7, "/set_stats_topic")
	w.AssertReplyContains("Stats topic set")

	g, _ := w.Store.Groups().Get(w.Ctx, groupID)
	if g.MatchesTopicID != 5 || g.StatsTopicID != 7 {
		t.Errorf("topics: %+v", g)
	}
}

// ---------- /match flow ---------------------------------------------------

func setupMatchScenario(t *testing.T) (*testkit.World, testkit.Group, testkit.User, testkit.User) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
	alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
	bob := w.AddUser(200, "bobby").SetNickname("bob_s21", "kazan", "Kazan", true)
	g = g.AddPlayer(alice.TelegramID).AddPlayer(bob.TelegramID)
	return w, g, alice, bob
}

func TestMatchHappyPathPending(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @bobby 3-1")
	// SendKeyboard call expected (match pending).
	calls := w.Messen.CallsByMethod("SendKeyboard")
	if len(calls) != 1 {
		t.Fatalf("expected 1 SendKeyboard, got %d:\n%s", len(calls), w.Messen.Pretty())
	}
	if !strings.Contains(calls[0].Text, "Match #1 pending") {
		t.Errorf("text: %q", calls[0].Text)
	}
	// Match row should exist with status PENDING.
	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil || m.Status != models.MatchStatusPending {
		t.Errorf("match: %+v err=%v", m, err)
	}
	// Author confirmation should be pre-recorded.
	confs, _ := w.Store.MatchConfirmations().ListForMatch(w.Ctx, g.GroupID, 1)
	if len(confs) != 1 || confs[0].TelegramID != alice.TelegramID {
		t.Errorf("confirmations: %+v", confs)
	}
}

func TestMatchAdminAutoApproved(t *testing.T) {
	w, g, _, _ := setupMatchScenario(t)
	admin, _ := w.Store.Admins().Get(w.Ctx, 50)
	w.SendInGroup(g, testkit.User{W: w, TelegramID: admin.TelegramID, Username: "admin01"}, 5, "/match @alice @bobby 3-1")
	// Admin path uses SendMessage (no keyboard).
	if msgs := w.Messen.CallsByMethod("SendMessage"); len(msgs) == 0 {
		t.Fatalf("expected SendMessage, got:\n%s", w.Messen.Pretty())
	}
	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil || m.Status != models.MatchStatusApproved {
		t.Errorf("match: %+v err=%v", m, err)
	}
}

func TestMatchTieRejected(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @bobby 3-3")
	w.AssertReplyContains("Score must have a winner")
}

func TestMatchSelfPlayRejected(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @alice 3-1")
	w.AssertReplyContains("cannot play themselves")
}

func TestMatchWrongTopicSilent(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 999, "/match @bobby 3-1")
	w.AssertNoReplies()
}

// ---------- Confirm button path ------------------------------------------

func TestMatchConfirmFlow(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @bobby 3-1")
	keyboardMsg := w.Messen.CallsByMethod("SendKeyboard")[0]
	cbData := keyboardMsg.Buttons[0].Callback // confirm button
	// bob taps confirm — should approve since alice already confirmed.
	w.ResetMessenger()
	w.TapButton(g, bob, cbData, 1)
	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil || m.Status != models.MatchStatusApproved {
		t.Errorf("post-confirm match: %+v err=%v", m, err)
	}
}

func TestMatchCancelFlow(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @bobby 3-1")
	keyboardMsg := w.Messen.CallsByMethod("SendKeyboard")[0]
	cbCancel := keyboardMsg.Buttons[1].Callback
	w.ResetMessenger()
	w.TapButton(g, bob, cbCancel, 1)
	if _, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1); err == nil {
		t.Fatal("match should be deleted")
	}
}

// ---------- /undo ---------------------------------------------------------

func TestUndoBothPlayersToggle(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	// Pre-create an APPROVED match.
	w.Store.PutMatchExt(models.Match{
		GroupID:      g.GroupID,
		MatchID:      1,
		Player1ID:    alice.TelegramID,
		Player2ID:    bob.TelegramID,
		Player1Score: 3,
		Player2Score: 1,
		Status:       models.MatchStatusApproved,
		PlayedAt:     time.Now(),
		CreatedAt:    time.Now(),
	})
	w.SendInGroup(g, alice, 5, "/undo #1")
	w.AssertReplyContains("Waiting for other player")
	w.ResetMessenger()
	w.SendInGroup(g, bob, 5, "/undo #1")
	w.AssertReplyContains("undone")
	m, _ := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if m.Status != models.MatchStatusUndone {
		t.Errorf("status: %v", m.Status)
	}
}

func TestUndoAdminInstant(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.Store.PutMatchExt(models.Match{
		GroupID:      g.GroupID,
		MatchID:      1,
		Player1ID:    alice.TelegramID,
		Player2ID:    bob.TelegramID,
		Player1Score: 3,
		Player2Score: 1,
		Status:       models.MatchStatusApproved,
		PlayedAt:     time.Now(),
		CreatedAt:    time.Now(),
	})
	w.SendInGroup(g, testkit.User{W: w, TelegramID: 50, Username: "admin01"}, 5, "/undo #1")
	w.AssertReplyContains("undone")
	m, _ := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if m.Status != models.MatchStatusUndone {
		t.Errorf("status: %v", m.Status)
	}
}

func TestUndoFromReply(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.Store.PutMatchExt(models.Match{
		GroupID:      g.GroupID,
		MatchID:      99,
		Player1ID:    alice.TelegramID,
		Player2ID:    bob.TelegramID,
		Player1Score: 3,
		Player2Score: 1,
		Status:       models.MatchStatusApproved,
		PlayedAt:     time.Now(),
		CreatedAt:    time.Now(),
	})
	// Player replies to a bot message containing "Match #99 confirmed."
	w.SendReply(g, alice, 5, "/undo", "Match #99 confirmed. ...")
	w.AssertReplyContains("Waiting for other player")
}

// ---------- /rankings + /stats -------------------------------------------

func TestRankingsAfterApprovedMatches(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.Store.PutMatchExt(models.Match{
		GroupID: g.GroupID, MatchID: 1, Player1ID: alice.TelegramID, Player2ID: bob.TelegramID,
		Player1Score: 3, Player2Score: 0, Status: models.MatchStatusApproved,
		PlayedAt: time.Now(), CreatedAt: time.Now(),
	})
	w.SendInGroup(g, alice, 5, "/rankings")
	w.AssertReplyContains("alice_s21")
	w.AssertReplyContains("bob_s21")
}

func TestStatsForCaller(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.Store.PutMatchExt(models.Match{
		GroupID: g.GroupID, MatchID: 1, Player1ID: alice.TelegramID, Player2ID: bob.TelegramID,
		Player1Score: 3, Player2Score: 0, Status: models.MatchStatusApproved,
		PlayedAt: time.Now(), CreatedAt: time.Now(),
	})
	w.SendInGroup(g, alice, 5, "/stats")
	w.AssertReplyContains("Wins: 1")
}

// ---------- /guest --------------------------------------------------------

func TestGuestCreatedByAdmin(t *testing.T) {
	w := testkit.New(t)
	adminUser := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	guestU := w.AddUser(101, "guesty")
	_ = guestU
	w.SendDM(adminUser, "/guest @guesty")
	w.AssertReplyContains("Guest created")
	u, _ := w.Store.Users().Get(w.Ctx, 101)
	if u.NicknameStatus != models.NicknameStatusGuest || !u.IsVerified() {
		t.Errorf("guest: %+v", u)
	}
}

// ---------- Periodic job --------------------------------------------------

func TestPeriodicExpiresPendingMatch(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.Store.PutMatchExt(models.Match{
		GroupID: g.GroupID, MatchID: 1, Player1ID: alice.TelegramID, Player2ID: bob.TelegramID,
		Player1Score: 3, Player2Score: 1, Status: models.MatchStatusPending,
		PlayedAt:  time.Now().Add(-25 * time.Hour),
		CreatedAt: time.Now().Add(-25 * time.Hour),
	})
	w.RunPeriodic()
	if _, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1); err == nil {
		t.Fatal("expired match should be deleted")
	}
}

// ---------- /admin auto-promote --------------------------------------

func TestAdminAutoPromotesUserRow(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.S21.SetUser("alice_login", "p", s21.Profile{Login: "alice_login", CampusID: "kazan", CampusName: "Kazan", CoalitionName: "Terra"})

	w.SendDM(alice, "/admin alice_login:p")
	w.AssertReplyContains("admin for Kazan")
	w.AssertReplyContains("automatically")

	u, err := w.Store.Users().Get(w.Ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if u.S21Nickname != "alice_login" {
		t.Errorf("nickname not auto-set: %q", u.S21Nickname)
	}
	if u.NicknameStatus != models.NicknameStatusProvided {
		t.Errorf("nickname_status: %q", u.NicknameStatus)
	}
	if u.VerifiedBy != models.VerifiedByAuth {
		t.Errorf("verified_by: %q want %q", u.VerifiedBy, models.VerifiedByAuth)
	}
	if u.CoalitionName != "Terra" {
		t.Errorf("coalition: %q", u.CoalitionName)
	}
}

func TestAdminLoginRotationUpdatesNickname(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").MakeAdmin("old_login", "p", "kazan", "Kazan")
	// Rotate to a new login (same campus).
	w.S21.SetUser("new_login", "p2", s21.Profile{Login: "new_login", CampusID: "kazan", CampusName: "Kazan"})
	w.S21.SetAdminPassword("new_login", "p2")

	w.SendDM(alice, "/admin new_login:p2")
	u, _ := w.Store.Users().Get(w.Ctx, 100)
	if u.S21Nickname != "new_login" {
		t.Errorf("rotated nickname: %q", u.S21Nickname)
	}
	if u.VerifiedBy != models.VerifiedByAuth {
		t.Errorf("verified_by after rotation: %q", u.VerifiedBy)
	}
}

func TestProvideNicknameRejectedForAdmin(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").MakeAdmin("alice_login", "p", "kazan", "Kazan")
	w.SendDM(alice, "/provide_nickname some_other_nick")
	w.AssertReplyContains("You're an admin")
	// Nickname must not change.
	u, _ := w.Store.Users().Get(w.Ctx, 100)
	if u.S21Nickname == "some_other_nick" {
		t.Error("admin nickname was overwritten")
	}
}

func TestRemoveNicknameRejectedForAdmin(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").MakeAdmin("alice_login", "p", "kazan", "Kazan")
	w.SendDM(alice, "/remove_nickname")
	w.AssertReplyContains("You're an admin")
	u, _ := w.Store.Users().Get(w.Ctx, 100)
	if u.NicknameStatus != models.NicknameStatusProvided {
		t.Errorf("admin nickname was cleared: %+v", u)
	}
}

// ---------- /set_stats_topic posts placeholders ----------------------

func TestSetStatsTopicPostsPlaceholders(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	groupID := int64(-1001)
	// Register the group first.
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 0, "/bot_register_group")
	w.ResetMessenger()
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 5, "/set_matches_topic")
	w.ResetMessenger()
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 7, "/set_stats_topic")

	// Expect: SendMessage(rankings) + PinMessage + SendMessage(stats) + PinMessage + reply.
	sends := w.Messen.CallsByMethod("SendMessage")
	if len(sends) < 3 {
		t.Fatalf("expected >=3 SendMessage calls (rankings, stats, reply), got %d:\n%s", len(sends), w.Messen.Pretty())
	}
	pins := w.Messen.CallsByMethod("PinMessage")
	if len(pins) != 2 {
		t.Errorf("expected 2 pin calls, got %d", len(pins))
	}
	g, _ := w.Store.Groups().Get(w.Ctx, groupID)
	if g.RankingsMessageID == 0 || g.StatsMessageID == 0 {
		t.Errorf("placeholder IDs not stored: %+v", g)
	}
}

// ---------- Admin participant Confirm auto-approves ------------------

func TestAdminParticipantConfirmAutoApproves(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
	alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
	g = g.AddPlayer(alice.TelegramID).AddPlayer(admin.TelegramID)

	// Non-admin alice creates match: alice vs admin.
	w.SendInGroup(g, alice, 5, "/match @admin01 3-1")
	keyboardMsg := w.Messen.CallsByMethod("SendKeyboard")
	if len(keyboardMsg) == 0 {
		t.Fatalf("no SendKeyboard:\n%s", w.Messen.Pretty())
	}
	cbConfirm := keyboardMsg[0].Buttons[0].Callback

	w.ResetMessenger()
	// Admin taps confirm — should immediately approve, even though only one
	// confirmation has been recorded (admin's). Author alice was pre-confirmed
	// but admin's tap alone is the approval.
	w.TapButton(g, admin, cbConfirm, 1)

	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil || m.Status != models.MatchStatusApproved {
		t.Errorf("admin tap should approve, got status=%v err=%v", m.Status, err)
	}
}

// Guard: dispatcher should not panic on private chats with unrelated text.
func TestDispatcherIgnoresUnknownText(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "hello")
	w.AssertNoReplies()
}

// Make sure ctx.Value usage above doesn't panic — replace with simpler test.
var _ = context.Background()
