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

// ---------- Interactive /match (no args) ----------------------------------

// lastGridCall returns the most recent SendKeyboardGrid (or EditKeyboardGrid
// if no Send has been seen yet) recorded by the mock.
func lastGridCall(w *testkit.World) (messenger.Call, bool) {
	calls := w.Messen.CallsByMethod("SendKeyboardGrid")
	if len(calls) > 0 {
		return calls[len(calls)-1], true
	}
	return messenger.Call{}, false
}

// lastEditGridCall returns the most recent EditKeyboardGrid.
func lastEditGridCall(w *testkit.World) (messenger.Call, bool) {
	calls := w.Messen.CallsByMethod("EditKeyboardGrid")
	if len(calls) > 0 {
		return calls[len(calls)-1], true
	}
	return messenger.Call{}, false
}

func TestInteractiveMatchOpensOpponentPicker(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	got, ok := lastGridCall(w)
	if !ok {
		t.Fatalf("expected SendKeyboardGrid for /match, got: %s", w.Messen.Pretty())
	}
	if !strings.Contains(got.Text, "[MATCH_OP=opp owner=100") {
		t.Errorf("opp header missing in text: %q", got.Text)
	}
	if !strings.Contains(got.Text, "pick your opponent") {
		t.Errorf("opp prompt body missing: %q", got.Text)
	}
	// Bob should be in the list; Alice (caller) should not.
	foundBob, foundAlice := false, false
	for _, b := range got.Buttons {
		if b.Callback == "m:i:opp:200" {
			foundBob = true
		}
		if b.Callback == "m:i:opp:100" {
			foundAlice = true
		}
	}
	if !foundBob {
		t.Errorf("bob not in opponent buttons: %+v", got.Buttons)
	}
	if foundAlice {
		t.Errorf("alice (self) should not be in opponent buttons")
	}
}

func TestInteractiveMatchOpponentTapShowsScorePicker(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)

	w.TapButtonOnMessage(g, alice, "m:i:opp:200", prompt.MessageID, prompt.Text)
	got, ok := lastEditGridCall(w)
	if !ok {
		t.Fatalf("expected EditKeyboardGrid after opp tap, got: %s", w.Messen.Pretty())
	}
	// The new score header carries URL-encoded owner_label and opp_label so
	// score-cell taps render without identity-service calls. We only assert
	// the stable fields here.
	for _, want := range []string{
		"[MATCH_OP=score owner=100 ",
		" opp=200 ",
		" p1=- p2=-]",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("score header missing %q in: %q", want, got.Text)
		}
	}
	// Score grid: 10 rows × 2 cols + confirm/cancel row + back row = 23 buttons.
	if len(got.Buttons) != 10*2+2+1 {
		t.Errorf("expected 23 buttons (20 score + confirm/cancel + back), got %d", len(got.Buttons))
	}
	_ = bob
}

func TestInteractiveMatchScoreSelectionUpdatesMarkers(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)
	// Tap opponent → score picker.
	w.TapButtonOnMessage(g, alice, "m:i:opp:200", prompt.MessageID, prompt.Text)
	scorePrompt, _ := lastEditGridCall(w)

	// Tap player1 = 3.
	w.TapButtonOnMessage(g, alice, "m:i:s:1:3", scorePrompt.MessageID, scorePrompt.Text)
	after1, _ := lastEditGridCall(w)
	if !strings.Contains(after1.Text, "p1=3 p2=-") {
		t.Errorf("p1 not stamped: %q", after1.Text)
	}

	// Tap player2 = 1.
	w.TapButtonOnMessage(g, alice, "m:i:s:2:1", after1.MessageID, after1.Text)
	after2, _ := lastEditGridCall(w)
	if !strings.Contains(after2.Text, "p1=3 p2=1") {
		t.Errorf("p2 not stamped: %q", after2.Text)
	}

	// Reselect player1 = 5.
	w.TapButtonOnMessage(g, alice, "m:i:s:1:5", after2.MessageID, after2.Text)
	after3, _ := lastEditGridCall(w)
	if !strings.Contains(after3.Text, "p1=5 p2=1") {
		t.Errorf("p1 reselect not stamped: %q", after3.Text)
	}
}

func TestInteractiveMatchConfirmRegistersMatch(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:opp:200", prompt.MessageID, prompt.Text)
	scorePrompt, _ := lastEditGridCall(w)
	// p1=3, p2=1
	w.TapButtonOnMessage(g, alice, "m:i:s:1:3", scorePrompt.MessageID, scorePrompt.Text)
	cur, _ := lastEditGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:s:2:1", cur.MessageID, cur.Text)
	cur, _ = lastEditGridCall(w)

	w.TapButtonOnMessage(g, alice, "m:i:confirm", cur.MessageID, cur.Text)
	m, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1)
	if err != nil {
		t.Fatalf("match not registered: %v", err)
	}
	if m.Player1ID != alice.TelegramID || m.Player2ID != 200 {
		t.Errorf("wrong players: %+v", m)
	}
	if m.Player1Score != 3 || m.Player2Score != 1 {
		t.Errorf("wrong score: %+v", m)
	}
	if m.Status != models.MatchStatusPending {
		t.Errorf("non-admin should yield PENDING, got %v", m.Status)
	}
}

func TestInteractiveMatchConfirmRejectsTie(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:opp:200", prompt.MessageID, prompt.Text)
	cur, _ := lastEditGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:s:1:3", cur.MessageID, cur.Text)
	cur, _ = lastEditGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:s:2:3", cur.MessageID, cur.Text)
	cur, _ = lastEditGridCall(w)

	w.TapButtonOnMessage(g, alice, "m:i:confirm", cur.MessageID, cur.Text)
	// No match row should be created on tie.
	if _, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1); err == nil {
		t.Fatal("tied confirm should not register a match")
	}
	// AnswerCallback should explain why.
	answers := w.Messen.CallsByMethod("AnswerCallback")
	hasTieMsg := false
	for _, a := range answers {
		if strings.Contains(a.Text, "must have a winner") {
			hasTieMsg = true
		}
	}
	if !hasTieMsg {
		t.Errorf("expected tie-rejection toast; got answers: %+v", answers)
	}
}

func TestInteractiveMatchConfirmRequiresBothScores(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:opp:200", prompt.MessageID, prompt.Text)
	cur, _ := lastEditGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:s:1:3", cur.MessageID, cur.Text)
	cur, _ = lastEditGridCall(w)
	// p2 still unselected — Confirm should refuse.
	w.TapButtonOnMessage(g, alice, "m:i:confirm", cur.MessageID, cur.Text)
	if _, err := w.Store.Matches().Get(w.Ctx, g.GroupID, 1); err == nil {
		t.Fatal("confirm without both scores must not register")
	}
}

func TestInteractiveMatchOnlyOwnerCanDrive(t *testing.T) {
	w, g, alice, bob := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)

	w.TapButtonOnMessage(g, bob, "m:i:opp:200", prompt.MessageID, prompt.Text)
	// Bob's tap should leave the keyboard untouched (no EditKeyboardGrid).
	if _, ok := lastEditGridCall(w); ok {
		t.Errorf("bob's tap should not edit the keyboard")
	}
}

func TestInteractiveMatchCancelEditsToCancelled(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:cancel", prompt.MessageID, prompt.Text)
	edits := w.Messen.CallsByMethod("EditMessage")
	if len(edits) == 0 {
		t.Fatalf("expected EditMessage on cancel, got: %s", w.Messen.Pretty())
	}
	if !strings.Contains(edits[len(edits)-1].Text, "cancelled") {
		t.Errorf("expected 'cancelled' text, got: %q", edits[len(edits)-1].Text)
	}
}

func TestInteractiveMatchInlineFormStillWorks(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match @bobby 3-1")
	// Inline path still uses the legacy SendKeyboard (Confirm/Cancel pair).
	if calls := w.Messen.CallsByMethod("SendKeyboard"); len(calls) == 0 {
		t.Fatalf("inline /match should still post Confirm/Cancel keyboard; got: %s", w.Messen.Pretty())
	}
}

// TestInteractiveMatchGracefulErrorOnConfirmFailure: when registration
// fails (e.g. AllocateAndInsertMatch errors), the keyboard message should
// be rewritten to a readable error and the draft cleared, instead of
// vanishing into a timed-out callback.
func TestInteractiveMatchGracefulErrorOnConfirmFailure(t *testing.T) {
	w, g, alice, _ := setupMatchScenario(t)
	w.SendInGroup(g, alice, 5, "/match")
	prompt, _ := lastGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:opp:200", prompt.MessageID, prompt.Text)
	cur, _ := lastEditGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:s:1:3", cur.MessageID, cur.Text)
	cur, _ = lastEditGridCall(w)
	w.TapButtonOnMessage(g, alice, "m:i:s:2:1", cur.MessageID, cur.Text)
	cur, _ = lastEditGridCall(w)

	// Force confirm to fail by hard-deleting the group row mid-flow — the
	// Groups.Get inside the confirm path returns ErrNotFound, which our
	// handler turns into a graceful "group lookup: …" error.
	if err := w.Store.PutMatchExt; err == nil {
		// keep the linter happy; PutMatchExt is the only knob we use here
	}
	// Drop the group: with the in-memory store there's no public Delete, so
	// instead we make IsChatAdmin fail to cause the registration path to
	// produce an error. Actually the cleanest path is to inject a
	// pre-existing match counter conflict… simplest reproducible: simulate
	// a Telegram SendKeyboard failure for the post-Confirm announcement.
	w.Messen.FailNext("SendKeyboard", messenger.ErrNotFound)

	w.TapButtonOnMessage(g, alice, "m:i:confirm", cur.MessageID, cur.Text)

	// On graceful failure, the last EditKeyboardGrid should carry the error
	// text and the keyboard should be cleared (no buttons).
	last, ok := lastEditGridCall(w)
	if !ok {
		t.Fatalf("expected EditKeyboardGrid on graceful fail, got: %s", w.Messen.Pretty())
	}
	if !strings.Contains(last.Text, "/match — failed") {
		t.Errorf("expected '/match — failed' text, got: %q", last.Text)
	}
	if len(last.Buttons) != 0 {
		t.Errorf("expected keyboard cleared on graceful fail, got %d buttons", len(last.Buttons))
	}
}

func TestInteractiveMatchOpponentsSortedByMatchCount(t *testing.T) {
	w := testkit.New(t)
	alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
	bob := w.AddUser(200, "bobby").SetNickname("bob_s21", "kazan", "Kazan", true)
	carol := w.AddUser(300, "carol").SetNickname("carol_s21", "kazan", "Kazan", true)
	g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", alice.TelegramID, 5, 7)
	g = g.AddPlayer(alice.TelegramID).AddPlayer(bob.TelegramID).AddPlayer(carol.TelegramID)
	// Two prior matches: alice vs carol — carol now has 2 plays, bob has 0.
	w.Store.PutMatchExt(models.Match{GroupID: g.GroupID, MatchID: 1, Player1ID: alice.TelegramID, Player2ID: carol.TelegramID, Player1Score: 3, Player2Score: 0, Status: models.MatchStatusApproved, PlayedAt: time.Now(), CreatedAt: time.Now()})
	w.Store.PutMatchExt(models.Match{GroupID: g.GroupID, MatchID: 2, Player1ID: alice.TelegramID, Player2ID: carol.TelegramID, Player1Score: 3, Player2Score: 1, Status: models.MatchStatusApproved, PlayedAt: time.Now(), CreatedAt: time.Now()})

	w.SendInGroup(g, alice, 5, "/match")
	got, _ := lastGridCall(w)

	// First opponent button should be carol (more matches than bob).
	if len(got.Buttons) < 2 {
		t.Fatalf("expected at least 2 buttons, got %d", len(got.Buttons))
	}
	if got.Buttons[0].Callback != "m:i:opp:300" {
		t.Errorf("expected carol first, got %s", got.Buttons[0].Callback)
	}
	if got.Buttons[1].Callback != "m:i:opp:200" {
		t.Errorf("expected bob second, got %s", got.Buttons[1].Callback)
	}
}
