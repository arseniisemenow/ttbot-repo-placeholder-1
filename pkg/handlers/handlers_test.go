package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/s21"
	"github.com/arseniisemenow/ttbot-core/pkg/testkit"
)

// ---------- /login --------------------------------------------------------

func TestLoginStoresAccount(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.S21.SetUser("alice_login", "p", s21.Profile{Login: "alice_login", CampusID: "kazan", CampusName: "Kazan"})
	w.SendDM(alice, "/login")
	w.AssertReplyContains("[LOGIN_OP=set]")
	w.SendDMReply(alice, "alice_login:p", "[LOGIN_OP=set]\n\nReply with...")
	w.AssertReplyContains("logged in as alice_login")
	a, err := w.Store.S21Accounts().Get(w.Ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if a.S21Login != "alice_login" || a.CampusID != "kazan" {
		t.Errorf("account row: %+v", a)
	}
}

func TestLoginRejectsInlineArgs(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/login user:pw")
	w.AssertReplyContains("takes no arguments")
}

func TestLoginInvalidCredentials(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/login")
	w.SendDMReply(alice, "not_real:nope", "[LOGIN_OP=set]\n\nReply with...")
	w.AssertReplyContains("rejected")
}

// ---------- /logout -------------------------------------------------------

func TestLogoutTwoStepRemovesRow(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").MakeAdmin("alogin", "pw", "kazan", "Kazan")
	w.SendDM(alice, "/logout")
	w.AssertReplyContains("[LOGIN_OP=logout]")
	w.SendDMReply(alice, "confirm", "[LOGIN_OP=logout]\n\n...")
	w.AssertReplyContains("Logged out")
}

func TestLogoutRequiresLoggedIn(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/logout")
	w.AssertReplyContains("not logged in")
}

// ---------- /whoami -------------------------------------------------------

func TestWhoamiLoggedIn(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").MakeAdmin("alogin", "pw", "kazan", "Kazan")
	w.SendDM(alice, "/whoami")
	w.AssertReplyContains("alogin")
	w.AssertReplyContains("Kazan")
}

func TestWhoamiNotLoggedIn(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "/whoami")
	w.AssertReplyContains("not logged in")
}

// ---------- /bot_register_group + topics ---------------------------------

func TestRegisterGroupAndTopicsViaChatAdmin(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01")
	groupID := int64(-1001)
	// Caller is a Telegram-chat admin in the group.
	w.Messen.SetChatAdmin(groupID, admin.TelegramID, true)

	w.SendInGroup(testkit.Group{W: w, GroupID: groupID, MatchesTopicID: 0, StatsTopicID: 0}, admin, 0, "/bot_register_group")
	w.AssertReplyContains("Group linked")

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

func TestRegisterGroupRejectsNonChatAdmin(t *testing.T) {
	w := testkit.New(t)
	user := w.AddUser(100, "alice")
	groupID := int64(-1001)
	// Not setting chat-admin — IsChatAdmin returns false.
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, user, 0, "/bot_register_group")
	w.AssertReplyContains("Only group admins")
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
	calls := w.Messen.CallsByMethod("SendKeyboard")
	if len(calls) != 1 {
		t.Fatalf("expected 1 SendKeyboard, got %d:\n%s", len(calls), w.Messen.Pretty())
	}
	if !strings.Contains(calls[0].Text, "Match #1 pending") {
		t.Errorf("text: %q", calls[0].Text)
	}
	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil || m.Status != models.MatchStatusPending {
		t.Errorf("match: %+v err=%v", m, err)
	}
	confs, _ := w.Store.MatchConfirmations().ListForMatch(w.Ctx, g.GroupID, 1)
	if len(confs) != 1 || confs[0].TelegramID != alice.TelegramID {
		t.Errorf("confirmations: %+v", confs)
	}
}

func TestMatchAdminAutoApproved(t *testing.T) {
	w, g, _, _ := setupMatchScenario(t)
	admin, _ := w.Store.Admins().Get(w.Ctx, 50)
	w.SendInGroup(g, testkit.User{W: w, TelegramID: admin.TelegramID, Username: "admin01"}, 5, "/match @alice @bobby 3-1")
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

// ---------- Bare-nickname resolution --------------------------------------

func TestMatchBareNicknameResolvesViaIdentity(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match bob_s21 3-1")
	calls := w.Messen.CallsByMethod("SendKeyboard")
	if len(calls) != 1 {
		t.Fatalf("expected 1 SendKeyboard, got %d:\n%s", len(calls), w.Messen.Pretty())
	}
	if !strings.Contains(calls[0].Text, "bob_s21") {
		t.Errorf("expected display name from identity service, got: %q", calls[0].Text)
	}
}

func TestMatchBareNicknameNotFound(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match no_such_nick 3-1")
	w.AssertReplyContains("not registered")
	w.AssertReplyContains("school_21_identity_bot")
}

func TestMatchBareNicknameDuplicateNotesCount(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	// A second account claims the same nickname as bob.
	w.AddUser(201, "twin").SetNickname("bob_s21", "kazan", "Kazan", true)
	w.SendInGroup(g, alice, 5, "/match bob_s21 3-1")
	calls := w.Messen.CallsByMethod("SendKeyboard")
	if len(calls) != 1 {
		t.Fatalf("expected 1 SendKeyboard, got %d:\n%s", len(calls), w.Messen.Pretty())
	}
	if !strings.Contains(calls[0].Text, "claimed by 2 telegram accounts") {
		t.Errorf("expected duplicate note in: %q", calls[0].Text)
	}
}

// ---------- Confirm button path ------------------------------------------

func TestMatchConfirmFlow(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @bobby 3-1")
	keyboardMsg := w.Messen.CallsByMethod("SendKeyboard")[0]
	cbData := keyboardMsg.Buttons[0].Callback
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

func TestUndoChatAdminInstant(t *testing.T) {
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
	// The admin user (50) was marked chat-admin in AddConfiguredGroup.
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

// ---------- /set_stats_topic posts placeholders ----------------------

func TestSetStatsTopicPostsPlaceholders(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	groupID := int64(-1001)
	w.Messen.SetChatAdmin(groupID, admin.TelegramID, true)
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 0, "/bot_register_group")
	w.ResetMessenger()
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 5, "/set_matches_topic")
	w.ResetMessenger()
	w.SendInGroup(testkit.Group{W: w, GroupID: groupID}, admin, 7, "/set_stats_topic")

	sends := w.Messen.CallsByMethod("SendMessage")
	if len(sends) < 4 {
		t.Fatalf("expected >=4 SendMessage calls (3 placeholders + reply), got %d:\n%s", len(sends), w.Messen.Pretty())
	}
	pins := w.Messen.CallsByMethod("PinMessage")
	if len(pins) != 1 {
		t.Errorf("expected 1 pin call (only the combined stats message), got %d", len(pins))
	}
	g, _ := w.Store.Groups().Get(w.Ctx, groupID)
	if g.RankingsELOMessageID == 0 || g.RankingsGlickoMessageID == 0 || g.StatsMessageID == 0 {
		t.Errorf("maintained message IDs not stored: %+v", g)
	}
}

func TestMaintainedMessageRecreatedWhenDeleted(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
	alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
	bobby := w.AddUser(200, "bobby").SetNickname("bob_s21", "kazan", "Kazan", true)
	g = g.AddPlayer(alice.TelegramID).AddPlayer(bobby.TelegramID)

	w.SendInGroup(g, testkit.User{W: w, TelegramID: admin.TelegramID, Username: "admin01"}, 5, "/match @alice @bobby 3-1")
	gBefore, _ := w.Store.Groups().Get(w.Ctx, g.GroupID)
	if gBefore.RankingsELOMessageID == 0 || gBefore.RankingsGlickoMessageID == 0 || gBefore.StatsMessageID == 0 {
		t.Fatalf("maintained messages not all posted: %+v", gBefore)
	}

	w.Messen.FailNext("EditMessage", messenger.ErrNotFound)
	beforeSends := len(w.Messen.CallsByMethod("SendMessage"))
	w.SendInGroup(g, testkit.User{W: w, TelegramID: admin.TelegramID, Username: "admin01"}, 5, "/match @alice @bobby 3-2")
	afterSends := len(w.Messen.CallsByMethod("SendMessage"))

	if afterSends-beforeSends < 2 {
		t.Fatalf("expected the vanished maintained message to be re-posted (>=2 new sends after the failure); got %d new sends:\n%s",
			afterSends-beforeSends, w.Messen.Pretty())
	}

	gAfter, _ := w.Store.Groups().Get(w.Ctx, g.GroupID)
	rotated := gAfter.RankingsELOMessageID != gBefore.RankingsELOMessageID ||
		gAfter.RankingsGlickoMessageID != gBefore.RankingsGlickoMessageID ||
		gAfter.StatsMessageID != gBefore.StatsMessageID
	if !rotated {
		t.Errorf("expected one of the maintained message IDs to change after re-post; before=%+v after=%+v", gBefore, gAfter)
	}
}

func TestRefreshDeletesOrphanMessages(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
	gRow, _ := w.Store.Groups().Get(w.Ctx, g.GroupID)
	gRow.RankingsMessageID = 9001
	gRow.StatsELOMessageID = 9002
	gRow.StatsGlickoMessageID = 9003
	_ = w.Store.Groups().Upsert(w.Ctx, gRow)

	alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
	bobby := w.AddUser(200, "bobby").SetNickname("bob_s21", "kazan", "Kazan", true)
	g = g.AddPlayer(alice.TelegramID).AddPlayer(bobby.TelegramID)
	w.SendInGroup(g, testkit.User{W: w, TelegramID: admin.TelegramID, Username: "admin01"}, 5,
		"/match @alice @bobby 3-1")

	deletes := w.Messen.CallsByMethod("DeleteMessage")
	got := map[int64]bool{}
	for _, c := range deletes {
		got[c.MessageID] = true
	}
	for _, want := range []int64{9001, 9002, 9003} {
		if !got[want] {
			t.Errorf("orphan %d not deleted; deletes=%+v", want, deletes)
		}
	}
	gAfter, _ := w.Store.Groups().Get(w.Ctx, g.GroupID)
	if gAfter.RankingsMessageID != 0 || gAfter.StatsELOMessageID != 0 || gAfter.StatsGlickoMessageID != 0 {
		t.Errorf("orphan IDs not zeroed: %+v", gAfter)
	}
}

// ---------- Admin participant Confirm auto-approves ------------------

func TestAdminParticipantConfirmAutoApproves(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	admin.SetNickname("admin_s21", "kazan", "Kazan", true)
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
	w.TapButton(g, admin, cbConfirm, 1)

	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil || m.Status != models.MatchStatusApproved {
		t.Errorf("admin tap should approve, got status=%v err=%v", m.Status, err)
	}
}

// ---------- Stats-topic janitor --------------------------------------

func TestStatsTopicLitterDeleted(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
	stranger := w.AddUser(999, "stranger0")
	w.SendInGroup(g, stranger, g.StatsTopicID, "lol")
	deletes := w.Messen.CallsByMethod("DeleteMessage")
	if len(deletes) != 1 {
		t.Fatalf("expected 1 DeleteMessage, got %d:\n%s", len(deletes), w.Messen.Pretty())
	}
}

func TestStatsTopicLitterNotDeletedInOtherTopic(t *testing.T) {
	w := testkit.New(t)
	admin := w.AddUser(50, "admin01").MakeAdmin("a_login", "pw", "kazan", "Kazan")
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
	stranger := w.AddUser(999, "stranger0")
	w.SendInGroup(g, stranger, g.MatchesTopicID, "hello")
	deletes := w.Messen.CallsByMethod("DeleteMessage")
	if len(deletes) != 0 {
		t.Errorf("matches-topic message should not be deleted; got %+v", deletes)
	}
}

func TestStatsTopicLitterIgnoredInUnregisteredGroup(t *testing.T) {
	w := testkit.New(t)
	stranger := w.AddUser(999, "stranger0")
	w.SendInGroup(testkit.Group{W: w, GroupID: -1234, MatchesTopicID: 9, StatsTopicID: 8}, stranger, 8, "hello")
	deletes := w.Messen.CallsByMethod("DeleteMessage")
	if len(deletes) != 0 {
		t.Errorf("unregistered-group message should not be deleted; got %+v", deletes)
	}
}

func TestDispatcherIgnoresUnknownText(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice")
	w.SendDM(alice, "hello")
	w.AssertNoReplies()
}
